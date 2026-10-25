package db

import (
	"context"
	"strings"
	"testing"
)

func searchTitles(t *testing.T, r *BookRepo, q string, uid int64) ([]string, int) {
	t.Helper()
	books, total, err := r.SearchImported(context.Background(), q, uid, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range books {
		out = append(out, b.Title)
	}
	return out, total
}

func TestSearchImported(t *testing.T) {
	database, author, _ := openTestDB(t)
	ctx := context.Background()
	r := NewBookRepo(database)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// imported makes a downloadable book owned by owner (0 = unowned).
	imported := func(title string, owner int64) int64 {
		t.Helper()
		b := secondBook(t, database, author.ID, title)
		if err := r.SetFilePath(ctx, b.ID, "/lib/"+strings.ReplaceAll(title, " ", "_")+".epub"); err != nil {
			t.Fatal(err)
		}
		exec("UPDATE books SET status='imported' WHERE id=?", b.ID)
		if owner != 0 {
			exec("UPDATE books SET owner_user_id=? WHERE id=?", owner, b.ID)
		} else {
			exec("UPDATE books SET owner_user_id=NULL WHERE id=?", b.ID)
		}
		return b.ID
	}
	var u1, u2 int64 = 101, 102
	for _, id := range []int64{u1, u2} {
		exec("INSERT INTO users (id, username, password_hash) VALUES (?, ?, 'x')", id, "u"+string(rune('0'+id%10)))
	}

	imported("Dragon Atlas One", u1)
	imported("Dragon Atlas Two", u2)
	imported("Dragon Atlas Shared", 0)
	imported("Müller Chronicle", 0)

	// A wanted book that owns a file: In Library on the Books page, but not
	// downloadable from a catalogue, so it must be neither listed nor counted.
	w := secondBook(t, database, author.ID, "Dragon Atlas Wanted")
	if err := r.AddBookFile(ctx, w.ID, "ebook", "/lib/wanted.epub"); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE books SET status='wanted', owner_user_id=NULL WHERE id=?", w.ID)

	// An imported row with no path is not downloadable either.
	np := secondBook(t, database, author.ID, "Dragon Atlas Pathless")
	exec("UPDATE books SET status='imported', owner_user_id=NULL WHERE id=?", np.ID)

	t.Run("unscoped sees every downloadable match only", func(t *testing.T) {
		got, total := searchTitles(t, r, "dragon atlas", 0)
		if total != 3 || len(got) != 3 {
			t.Fatalf("got %v total %d", got, total)
		}
		for _, g := range got {
			if strings.Contains(g, "Wanted") || strings.Contains(g, "Pathless") {
				t.Errorf("non downloadable book listed: %v", got)
			}
		}
	})
	t.Run("owner scoping keeps own and unowned rows", func(t *testing.T) {
		got, total := searchTitles(t, r, "dragon atlas", u1)
		joined := strings.Join(got, "|")
		if total != 2 || !strings.Contains(joined, "One") || !strings.Contains(joined, "Shared") || strings.Contains(joined, "Two") {
			t.Errorf("u1 got %v total %d", got, total)
		}
		got, total = searchTitles(t, r, "dragon atlas", u2)
		joined = strings.Join(got, "|")
		if total != 2 || !strings.Contains(joined, "Two") || !strings.Contains(joined, "Shared") || strings.Contains(joined, "One") {
			t.Errorf("u2 got %v total %d", got, total)
		}
	})
	t.Run("empty and punctuation queries match nothing", func(t *testing.T) {
		for _, q := range []string{"", "   ", "???"} {
			if got, total := searchTitles(t, r, q, 0); len(got) != 0 || total != 0 {
				t.Errorf("q=%q got %v total %d", q, got, total)
			}
		}
	})
	t.Run("accent folding", func(t *testing.T) {
		for _, q := range []string{"muller", "MÜLLER", "Müller"} {
			if got, _ := searchTitles(t, r, q, 0); len(got) != 1 || got[0] != "Müller Chronicle" {
				t.Errorf("q=%q got %v", q, got)
			}
		}
	})
}
