package metadata

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// tagProvider answers every lookup with records stamped with its own name, so
// a test can tell which provider set produced a result. Description, image
// and series titles all carry the tag.
type tagProvider struct {
	name string
	// block, when set, holds GetBook until it is closed; started is closed
	// once GetBook has been entered.
	block   chan struct{}
	started chan struct{}
	once    sync.Once
}

func (p *tagProvider) Name() string { return p.name }

func (p *tagProvider) tag() string { return "tag:" + p.name }

func (p *tagProvider) SearchAuthors(_ context.Context, query string) ([]models.Author, error) {
	return []models.Author{{Name: query + " " + p.name, MetadataProvider: p.name}}, nil
}

func (p *tagProvider) SearchBooks(_ context.Context, query string) ([]models.Book, error) {
	return []models.Book{{
		Title:       query,
		Description: strings.Repeat("long enough to win the merge ", 3) + p.tag(),
		ImageURL:    p.tag(),
	}}, nil
}

func (p *tagProvider) GetAuthor(context.Context, string) (*models.Author, error) { return nil, nil }

func (p *tagProvider) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	if p.started != nil {
		p.once.Do(func() { close(p.started) })
	}
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// No description, so GetBook asks the enrichers: the enrichment step runs
	// after the primary's answer, which is the window a mixed set would show.
	return &models.Book{ForeignID: foreignID, Title: "Book " + foreignID, ImageURL: p.tag()}, nil
}

func (p *tagProvider) GetEditions(context.Context, string) ([]models.Edition, error) {
	return nil, nil
}

func (p *tagProvider) GetBookByISBN(_ context.Context, isbn string) (*models.Book, error) {
	return &models.Book{Title: "ISBN " + isbn, ImageURL: p.tag(), MetadataProvider: p.name}, nil
}

func (p *tagProvider) SearchSeries(_ context.Context, query string, _ int) ([]SeriesSearchResult, error) {
	return []SeriesSearchResult{{ForeignID: p.name + ":" + query, Title: p.tag()}}, nil
}

func (p *tagProvider) GetSeriesCatalog(_ context.Context, foreignID string) (*SeriesCatalog, error) {
	return &SeriesCatalog{ForeignID: foreignID, Title: p.tag()}, nil
}

// providerSetFixture is two disjoint provider sets: every name in one is
// absent from the other, so any result can be traced to exactly one set.
type providerSetFixture struct {
	sets  [2][]*tagProvider
	owner map[string]int // tag or provider name -> set index
}

func newProviderSetFixture() *providerSetFixture {
	f := &providerSetFixture{owner: map[string]int{}}
	for i, names := range [][]string{{"alpha", "beta", "betaprime"}, {"gamma", "delta"}} {
		for _, name := range names {
			p := &tagProvider{name: name}
			f.sets[i] = append(f.sets[i], p)
			f.owner[name] = i
			f.owner[p.tag()] = i
		}
	}
	return f
}

func (f *providerSetFixture) install(t testing.TB, a *Aggregator, i int) {
	t.Helper()
	set := f.sets[i]
	enrichers := make([]Provider, 0, len(set)-1)
	for _, p := range set[1:] {
		enrichers = append(enrichers, p)
	}
	if err := a.Reconfigure(set[0], enrichers...); err != nil {
		t.Errorf("Reconfigure(set %d): %v", i, err)
	}
}

func (f *providerSetFixture) names(i int) []string {
	var out []string
	for _, p := range f.sets[i] {
		out = append(out, p.name)
	}
	return out
}

// sameSet returns an error unless every tag or name in seen belongs to the
// same provider set. Empty strings are ignored.
func (f *providerSetFixture) sameSet(op string, seen ...string) error {
	set := -1
	for _, s := range seen {
		if s == "" {
			continue
		}
		i, ok := f.owner[s]
		if !ok {
			return fmt.Errorf("%s: %q is from no known provider", op, s)
		}
		if set == -1 {
			set = i
		} else if set != i {
			return fmt.Errorf("%s: result mixes provider sets: %q", op, seen)
		}
	}
	return nil
}

func newLiveTestAggregator(f *providerSetFixture, initial int) *Aggregator {
	a := NewAggregator(f.sets[initial][0])
	if err := a.Reconfigure(f.sets[initial][0], toProviders(f.sets[initial][1:])...); err != nil {
		panic(err)
	}
	return a
}

func toProviders(ps []*tagProvider) []Provider {
	out := make([]Provider, len(ps))
	for i, p := range ps {
		out[i] = p
	}
	return out
}

// TestAggregatorReconfigureRace is #1771's race test: lookups of every kind
// run while another goroutine flips the provider set back and forth. Under
// -race it proves the swap is free of data races; on any run it proves every
// result came from one set, never a primary from one and an enricher or an
// outcome from the other.
func TestAggregatorReconfigureRace(t *testing.T) {
	f := newProviderSetFixture()
	a := newLiveTestAggregator(f, 0)
	ctx := context.Background()

	var stop atomic.Bool
	var swaps atomic.Int64
	var flipper sync.WaitGroup
	flipper.Add(1)
	go func() {
		defer flipper.Done()
		for i := 0; !stop.Load(); i++ {
			f.install(t, a, i%2)
			swaps.Add(1)
		}
	}()

	const workers, rounds = 8, 60
	errs := make(chan error, workers*rounds*6)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				q := fmt.Sprintf("q%d", (w+r)%5) // repeats, so cache hits are checked too

				authors, outcome, err := a.SearchAuthorsWithOutcome(ctx, q)
				if err != nil {
					errs <- err
					continue
				}
				seen := append([]string{outcome.Primary}, outcome.Answered...)
				for _, au := range authors {
					seen = append(seen, au.MetadataProvider)
				}
				if e := f.sameSet("SearchAuthorsWithOutcome", seen...); e != nil {
					errs <- e
				}

				books, outcome, err := a.SearchBooksWithOutcome(ctx, q)
				if err != nil {
					errs <- err
					continue
				}
				seen = append([]string{outcome.Primary}, outcome.Answered...)
				for _, b := range books {
					seen = append(seen, b.ImageURL)
				}
				if e := f.sameSet("SearchBooksWithOutcome", seen...); e != nil {
					errs <- e
				}

				book, err := a.GetBook(ctx, "W"+q)
				if err != nil || book == nil {
					errs <- fmt.Errorf("GetBook: %v, %w", book, err)
					continue
				}
				// The image is the primary's; the description is the
				// enricher's, fetched after the primary answered.
				descTag := book.Description[strings.LastIndex(book.Description, " ")+1:]
				if e := f.sameSet("GetBook", book.ImageURL, descTag); e != nil {
					errs <- e
				}

				isbn := fmt.Sprintf("978000000000%d", (w+r)%5)
				ib, outcome, err := a.GetBookByISBNWithOutcome(ctx, isbn)
				if err != nil {
					errs <- err
					continue
				}
				seen = append([]string{outcome.Primary}, outcome.Answered...)
				if ib != nil {
					seen = append(seen, ib.ImageURL)
				}
				if e := f.sameSet("GetBookByISBNWithOutcome", seen...); e != nil {
					errs <- e
				}

				enrich := &models.Book{Title: q}
				a.EnrichMissingCovers(ctx, []models.Book{*enrich})
				if series, err := a.SearchSeries(ctx, q, 5); err != nil || len(series) == 0 {
					errs <- fmt.Errorf("SearchSeries: %v, %w", series, err)
				} else if e := f.sameSet("SearchSeries", series[0].Title); e != nil {
					errs <- e
				}

				names := a.ProviderNames()
				if !slices.Equal(names, f.names(0)) && !slices.Equal(names, f.names(1)) {
					errs <- fmt.Errorf("ProviderNames = %v, neither set", names)
				}
			}
		}(w)
	}
	wg.Wait()
	stop.Store(true)
	flipper.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if n < 10 {
			t.Error(err)
		}
		n++
	}
	if n > 0 {
		t.Fatalf("%d inconsistent lookups", n)
	}
	if swaps.Load() < 2 {
		t.Fatalf("only %d reconfigurations ran during the lookups", swaps.Load())
	}
	t.Logf("%d reconfigurations during %d lookup rounds", swaps.Load(), workers*rounds)
}

// TestAggregatorReconfigureInFlightLookupFinishesOnOldSet holds a lookup
// inside the old primary, swaps the set, and checks the held lookup finishes
// entirely on the old set (its enrichment included, which runs after the
// swap) while a new lookup already uses the new set.
func TestAggregatorReconfigureInFlightLookupFinishesOnOldSet(t *testing.T) {
	f := newProviderSetFixture()
	slow := f.sets[0][0]
	slow.block = make(chan struct{})
	slow.started = make(chan struct{})
	a := newLiveTestAggregator(f, 0)
	ctx := context.Background()

	done := make(chan *models.Book, 1)
	go func() {
		b, err := a.GetBook(ctx, "Wslow")
		if err != nil {
			t.Errorf("in flight GetBook: %v", err)
		}
		done <- b
	}()
	<-slow.started
	f.install(t, a, 1)

	fresh, err := a.GetBook(ctx, "Wfresh")
	if err != nil || fresh == nil || fresh.ImageURL != f.sets[1][0].tag() {
		t.Fatalf("new lookup after Reconfigure = %+v, %v; want the new primary's", fresh, err)
	}

	close(slow.block)
	var old *models.Book
	select {
	case old = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("in flight lookup did not finish")
	}
	if old == nil || old.ImageURL != slow.tag() {
		t.Fatalf("in flight lookup = %+v, want the old primary's record", old)
	}
	descTag := old.Description[strings.LastIndex(old.Description, " ")+1:]
	if i, ok := f.owner[descTag]; !ok || i != 0 {
		t.Errorf("in flight lookup was enriched with %q, want one of the old set's enrichers", old.Description)
	}
}

type typedNilProvider struct{ tagProvider }

// TestAggregatorReconfigureRejectsInvalidSet: a nil primary, a nil enricher
// (plain or typed), or a duplicate provider name is refused with
// ErrInvalidProviderSet, and the previous set keeps serving.
func TestAggregatorReconfigureRejectsInvalidSet(t *testing.T) {
	f := newProviderSetFixture()
	a := newLiveTestAggregator(f, 0)
	ctx := context.Background()
	gamma, delta := f.sets[1][0], f.sets[1][1]
	var typedNil *typedNilProvider

	for name, call := range map[string]func() error{
		"nil primary":        func() error { return a.Reconfigure(nil, delta) },
		"typed nil primary":  func() error { return a.Reconfigure(typedNil, delta) },
		"nil enricher":       func() error { return a.Reconfigure(gamma, delta, nil) },
		"typed nil enricher": func() error { return a.Reconfigure(gamma, typedNil) },
		"duplicate name":     func() error { return a.Reconfigure(gamma, delta, &tagProvider{name: "delta"}) },
		"primary repeated":   func() error { return a.Reconfigure(gamma, gamma) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrInvalidProviderSet) {
				t.Fatalf("Reconfigure error = %v, want ErrInvalidProviderSet", err)
			}
			if got := a.ProviderNames(); !slices.Equal(got, f.names(0)) {
				t.Errorf("provider set after refused Reconfigure = %v, want the old %v", got, f.names(0))
			}
			b, err := a.GetBook(ctx, "Wkept")
			if err != nil || b == nil || b.ImageURL != f.sets[0][0].tag() {
				t.Errorf("lookup after refused Reconfigure = %+v, %v; want the old primary", b, err)
			}
		})
	}
	var nilAgg *Aggregator
	if err := nilAgg.Reconfigure(gamma); !errors.Is(err, ErrInvalidProviderSet) {
		t.Errorf("nil Aggregator Reconfigure = %v", err)
	}
}

// TestAggregatorReconfigureDoesNotServeOldPrimaryFromCache primes every cache
// that holds a result built from more than one provider, switches the
// primary, and checks the next lookup answers from the new set rather than
// returning what the old primary left behind (#1771, cf. #2869).
func TestAggregatorReconfigureDoesNotServeOldPrimaryFromCache(t *testing.T) {
	ctx := context.Background()
	lookups := map[string]func(*Aggregator) string{
		"GetBook": func(a *Aggregator) string {
			b, _ := a.GetBook(ctx, "Wcached")
			return b.ImageURL + " " + b.Description
		},
		"GetBookByISBN": func(a *Aggregator) string {
			b, _ := a.GetBookByISBN(ctx, "9780000000001")
			if b == nil {
				return ""
			}
			return b.ImageURL
		},
		"SearchSeries": func(a *Aggregator) string {
			r, _ := a.SearchSeries(ctx, "cached", 5)
			return r[0].Title
		},
		"GetSeriesCatalog": func(a *Aggregator) string {
			c, _ := a.GetSeriesCatalog(ctx, "series-1")
			return c.Title
		},
		"SearchBooks": func(a *Aggregator) string {
			bs, _ := a.SearchBooks(ctx, "cached")
			var tags []string
			for _, b := range bs {
				tags = append(tags, b.ImageURL)
			}
			return strings.Join(tags, " ")
		},
	}
	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			f := newProviderSetFixture()
			a := newLiveTestAggregator(f, 0)
			before := lookup(a)
			if !strings.Contains(before, f.sets[0][0].tag()) && !strings.Contains(before, f.sets[0][1].tag()) {
				t.Fatalf("priming lookup = %q, want set 0's records", before)
			}
			f.install(t, a, 1)
			after := lookup(a)
			for _, p := range f.sets[0] {
				if strings.Contains(after, p.tag()) {
					t.Errorf("after Reconfigure the lookup returned %q, a cached answer from the old set", after)
				}
			}
			if !strings.Contains(after, "tag:gamma") && !strings.Contains(after, "tag:delta") {
				t.Errorf("after Reconfigure the lookup returned %q, want the new set's records", after)
			}
		})
	}
}

// plainProvider is a provider with no author catalogue of its own, so its
// works come from a search when it is the primary and from nowhere when it
// is an enricher.
type plainProvider struct{ tagProvider }

type worksTagProvider struct{ tagProvider }

func (p *worksTagProvider) GetAuthorWorks(context.Context, string) ([]models.Book, error) {
	return []models.Book{{Title: "works of " + p.name}}, nil
}

// TestAggregatorReconfigurePromotedProviderGetsItsOwnWorks covers the raw
// author works cache: the same provider answers differently as an enricher
// (nothing) and as the primary (a search), so after it is promoted the empty
// enricher answer must not be served.
func TestAggregatorReconfigurePromotedProviderGetsItsOwnWorks(t *testing.T) {
	ctx := context.Background()
	plain := &plainProvider{tagProvider{name: "openlibrary"}}
	other := &worksTagProvider{tagProvider{name: "hardcover"}}
	a := NewAggregator(other, plain)

	if _, err := a.GetAuthorWorks(ctx, "OL1A"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reconfigure(plain, other); err != nil {
		t.Fatal(err)
	}
	works, err := a.GetAuthorWorks(ctx, "OL1A")
	if err != nil {
		t.Fatal(err)
	}
	if len(works) == 0 {
		t.Fatal("promoted primary served the empty works it returned as an enricher")
	}
}

// TestAggregatorDelegatesLoadCoreOnce holds the public surface to the shape
// that makes a mixed lookup impossible: every exported lookup on Aggregator
// is a single call of the same method on a.load(), and nothing outside
// aggregator_live.go loads the provider set at all. A delegate with logic of
// its own, or a helper reaching for cur again halfway through an operation,
// fails here before it can fail in production.
func TestAggregatorDelegatesLoadCoreOnce(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	coreMethods := map[string]bool{}
	delegates := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if path != "aggregator_live.go" {
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "cur" || sel.Sel.Name == "load") {
					t.Errorf("%s: reads the live provider set (.%s); only aggregator_live.go may",
						fset.Position(sel.Pos()), sel.Sel.Name)
				}
				return true
			})
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			switch star.X.(*ast.Ident).Name {
			case "aggregatorCore":
				coreMethods[fn.Name.Name] = true
			case "Aggregator":
				switch fn.Name.Name {
				case "Reconfigure", "ProviderNames", "SetAuthorEnrichers", "AuthorEnricherNames", "WithAudnexClient", "WithAudibleCatalogue":
					continue
				}
				delegates[fn.Name.Name] = true
				if !isLoadOnceDelegate(fn) {
					t.Errorf("%s: (*Aggregator).%s must be exactly `return a.load().%s(<its parameters>)`",
						fset.Position(fn.Pos()), fn.Name.Name, fn.Name.Name)
				}
			}
		}
	}
	for name := range coreMethods {
		if !delegates[name] {
			t.Errorf("aggregatorCore.%s has no Aggregator delegate", name)
		}
	}
	if len(delegates) < 25 {
		t.Fatalf("found only %d delegates; the guard is not seeing the code", len(delegates))
	}
}

func isLoadOnceDelegate(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	var call *ast.CallExpr
	switch st := fn.Body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(st.Results) != 1 {
			return false
		}
		call, _ = st.Results[0].(*ast.CallExpr)
	case *ast.ExprStmt:
		call, _ = st.X.(*ast.CallExpr)
	}
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != fn.Name.Name {
		return false
	}
	load, ok := sel.X.(*ast.CallExpr)
	if !ok || len(load.Args) != 0 {
		return false
	}
	loadSel, ok := load.Fun.(*ast.SelectorExpr)
	if !ok || loadSel.Sel.Name != "load" {
		return false
	}
	recv := fn.Recv.List[0].Names[0].Name
	if id, ok := loadSel.X.(*ast.Ident); !ok || id.Name != recv {
		return false
	}
	var params []string
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			params = append(params, n.Name)
		}
	}
	var args []string
	for _, arg := range call.Args {
		id, ok := arg.(*ast.Ident)
		if !ok {
			return false
		}
		args = append(args, id.Name)
	}
	return reflect.DeepEqual(params, args) || (len(params) == 0 && len(args) == 0)
}
