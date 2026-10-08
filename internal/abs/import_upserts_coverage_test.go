package abs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func covImpManualItem(foreignID, name string) NormalizedLibraryItem {
	item := sampleABSItem()
	item.Authors = []NormalizedAuthor{{ID: "author-abs-weir", Name: "A. Weir"}}
	item.ResolvedAuthorForeignID = foreignID
	item.ResolvedAuthorName = name
	return item
}

func TestCovImpResolveManualAuthor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := covImpConfig("lib-books")
	dry := cfg
	dry.DryRun = true

	t.Run("requires resolution", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		_, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL1A", ""), nil)
		if err == nil || !strings.Contains(err.Error(), "resolved author requires") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("matcher load fails", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.exec(t, "ALTER TABLE authors RENAME TO covimp_authors_gone")
		if _, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL1A", "Andy Weir"), nil); err == nil {
			t.Fatal("resolveManualAuthor with broken authors table returned nil error")
		}
	})

	t.Run("existing by foreign id", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		existing := covImpCreateAuthor(t, env, "OL1A", "Andy Weir")

		// Dry run links without writing.
		got, created, by, _, err := env.importer.resolveManualAuthor(ctx, dry, 0, covImpManualItem("OL1A", "Andy Weir"), nil)
		if err != nil || created || by != "manual_author" || got.ID != existing.ID {
			t.Fatalf("dry = %+v %v %q %v", got, created, by, err)
		}
		if n := env.count(t, "SELECT COUNT(*) FROM abs_provenance"); n != 0 {
			t.Fatalf("provenance rows = %d, dry run must not write", n)
		}

		got, created, by, _, err = env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL1A", "Andy Weir"), nil)
		if err != nil || created || by != "manual_author" || got.ID != existing.ID {
			t.Fatalf("apply = %+v %v %q %v", got, created, by, err)
		}
		link, err := env.provenance.GetByExternal(ctx, DefaultSourceID, "lib-books", entityTypeAuthor, "author-abs-weir")
		if err != nil || link == nil || link.LocalID != existing.ID {
			t.Fatalf("provenance = %+v %v, want linked to existing author", link, err)
		}
		aliases, _ := env.aliases.ListByAuthor(ctx, existing.ID)
		if len(aliases) != 1 || aliases[0].Name != "A. Weir" {
			t.Fatalf("aliases = %+v, want the ABS spelling recorded", aliases)
		}
	})

	t.Run("existing by foreign id write failures", func(t *testing.T) {
		t.Parallel()
		for _, table := range []string{"author_identifiers", "abs_provenance"} {
			env := covImpNewEnv(t)
			covImpCreateAuthor(t, env, "OL1A", "Andy Weir")
			env.failOn(t, "covimp_"+table, "INSERT", table, "")
			if _, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL1A", "Andy Weir"), nil); err == nil || !strings.Contains(err.Error(), "covimp boom") {
				t.Fatalf("%s: err = %v, want trigger error", table, err)
			}
		}
	})

	t.Run("ambiguous by name", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		covImpCreateAuthor(t, env, "OL-A", "Andy Weir")
		covImpCreateAuthor(t, env, "OL-B", "andy weir")
		_, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-NEW", "Andy Weir"), nil)
		var reviewErr reviewRequiredError
		if !errors.As(err, &reviewErr) || reviewErr.Reason != reviewReasonAmbiguousAuthor {
			t.Fatalf("err = %v, want ambiguous author review", err)
		}
	})

	t.Run("existing by name adopts identity", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		existing := &models.Author{ForeignID: absForeignID("author", "lib-books", "x"), Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: providerAudiobookshelf, Monitored: true}
		if err := env.authors.Create(ctx, existing); err != nil {
			t.Fatal(err)
		}
		got, created, by, _, err := env.importer.resolveManualAuthor(ctx, dry, 0, covImpManualItem("OL-UP", "Andy Weir"), nil)
		if err != nil || created || by != "manual_author" || got.ID != existing.ID {
			t.Fatalf("dry = %+v %v %q %v", got, created, by, err)
		}
		if a, _ := env.authors.GetByID(ctx, existing.ID); a.ForeignID != existing.ForeignID {
			t.Fatalf("dry run changed foreign id to %q", a.ForeignID)
		}

		got, _, _, _, err = env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-UP", "Andy Weir"), nil)
		if err != nil || got.ID != existing.ID {
			t.Fatalf("apply = %+v %v", got, err)
		}
		reloaded, _ := env.authors.GetByID(ctx, existing.ID)
		if reloaded.ForeignID != "OL-UP" {
			t.Fatalf("foreign id = %q, want the ABS placeholder replaced by OL-UP", reloaded.ForeignID)
		}
		if ident, _ := env.authors.GetAuthorIdentifier(ctx, "OL-UP"); ident == nil || ident.AuthorID != existing.ID {
			t.Fatalf("identifier = %+v, want OL-UP bound to the author", ident)
		}
	})

	t.Run("existing by name write failures", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ op, table string }{
			{"INSERT", "author_identifiers"},
			{"UPDATE", "authors"},
			{"INSERT", "abs_provenance"},
		} {
			env := covImpNewEnv(t)
			existing := &models.Author{ForeignID: "", Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: providerAudiobookshelf, Monitored: true}
			if err := env.authors.Create(ctx, existing); err != nil {
				t.Fatal(err)
			}
			env.failOn(t, "covimp_"+tc.table, tc.op, tc.table, "")
			if _, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-UP", "Andy Weir"), nil); err == nil || !strings.Contains(err.Error(), "covimp boom") {
				t.Fatalf("%s %s: err = %v, want trigger error", tc.op, tc.table, err)
			}
		}
	})

	t.Run("creates from metadata", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.importer.WithMetadata(metadata.NewAggregator(&stubABSMetadataProvider{authors: map[string]*models.Author{
			"OL-NEW": {ForeignID: "OL-NEW", Description: "From upstream"},
		}}))
		got, _, _, _, err := env.importer.resolveManualAuthor(ctx, dry, 0, covImpManualItem("OL-NEW", "Brand New"), nil)
		if err != nil || got.Name != "Brand New" || got.ID != 0 {
			t.Fatalf("dry create = %+v %v, want unsaved author", got, err)
		}
		if n := env.count(t, "SELECT COUNT(*) FROM authors"); n != 0 {
			t.Fatalf("authors = %d, dry run must not create", n)
		}

		got, created, by, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-NEW", "Brand New"), nil)
		if err != nil || !created || by != "manual_author" || got.ID == 0 {
			t.Fatalf("create = %+v %v %q %v", got, created, by, err)
		}
		saved, _ := env.authors.GetByID(ctx, got.ID)
		if saved.Name != "Brand New" || saved.Description != "From upstream" || saved.ForeignID != "OL-NEW" || saved.MonitorNewItems != models.AuthorMonitorNewItemsNone {
			t.Fatalf("saved = %+v, want upstream profile with the resolved name and import policy", saved)
		}
	})

	// A manual mapping to a non OpenLibrary id with no metadata to fetch it
	// from must be labelled with the provider the id belongs to, not
	// openlibrary (#2117).
	t.Run("creates without metadata labels the id's provider", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		got, created, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("dnb:gnd:118540238", "Brand New"), nil)
		if err != nil || !created {
			t.Fatalf("create = %+v %v %v", got, created, err)
		}
		saved, _ := env.authors.GetByID(ctx, got.ID)
		if saved.MetadataProvider != "dnb" {
			t.Fatalf("metadata_provider = %q for %s, want dnb", saved.MetadataProvider, saved.ForeignID)
		}
	})

	t.Run("create failures", func(t *testing.T) {
		t.Parallel()
		for _, table := range []string{"authors", "abs_provenance"} {
			env := covImpNewEnv(t)
			env.failOn(t, "covimp_"+table, "INSERT", table, "")
			if _, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-NEW", "Brand New"), nil); err == nil || !strings.Contains(err.Error(), "covimp boom") {
				t.Fatalf("%s: err = %v, want trigger error", table, err)
			}
		}
	})
}

func TestCovImpImportUtils(t *testing.T) {
	t.Parallel()
	if got := authorExternalID(NormalizedAuthor{Name: "Andy Weir"}); !strings.HasPrefix(got, "name:") {
		t.Fatalf("authorExternalID without id = %q, want name-derived", got)
	}
	if got := parseABSDate("", "abc"); got != nil {
		t.Fatalf("parseABSDate bad year = %v", got)
	}
	if got := parseABSDate("not a date", "1999"); got == nil || got.Year() != 1999 {
		t.Fatalf("parseABSDate year fallback = %v", got)
	}
	if got := parseABSDate("2020-3-4", ""); got == nil || got.Month() != 3 || got.Day() != 4 {
		t.Fatalf("parseABSDate short layout = %v", got)
	}
	for _, tc := range []struct{ cur, next, want string }{
		{"", models.MediaTypeEbook, models.MediaTypeEbook},
		{models.MediaTypeEbook, models.MediaTypeEbook, models.MediaTypeEbook},
		{models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth},
		{models.MediaTypeAudiobook, models.MediaTypeBoth, models.MediaTypeBoth},
		{models.MediaTypeEbook, "mystery", models.MediaTypeEbook},
	} {
		if got := mergeMediaType(tc.cur, tc.next); got != tc.want {
			t.Fatalf("mergeMediaType(%q,%q) = %q, want %q", tc.cur, tc.next, got, tc.want)
		}
	}
	if got := deriveEditionFormats(NormalizedLibraryItem{}); len(got) != 1 || got[0] != models.MediaTypeAudiobook {
		t.Fatalf("deriveEditionFormats(empty) = %v, want audiobook fallback", got)
	}
	if got := cleanStrings([]string{" a ", "", "A", "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("cleanStrings = %v", got)
	}
	if primaryAuthorName(NormalizedLibraryItem{}) != "" {
		t.Fatal("primaryAuthorName of authorless item must be empty")
	}
	ids := itemFileIDs(NormalizedLibraryItem{
		AudioFiles: []NormalizedAudioFile{{Path: " /a.m4b "}, {}},
		EbookPath:  "/b.epub",
	})
	if strings.Join(ids, ",") != "audio:/a.m4b,ebook:/b.epub" {
		t.Fatalf("itemFileIDs = %v", ids)
	}
	if firstNonEmpty("", "  ") != "" {
		t.Fatal("firstNonEmpty of blanks must be empty")
	}
}
