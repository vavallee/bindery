package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// legacyActiveSQL is the statement ListActive ran before it became two selects:
// one WHERE with an OR, the imported state bound as an argument. The
// equivalence test below runs it beside the current query.
const legacyActiveSQL = "SELECT id FROM downloads WHERE (status <> ? OR COALESCE(imported_at, completed_at, added_at) >= ?)"

// The two selects name the imported state as SQL text, because a bound value
// keeps SQLite off the partial index. This fails if the constant is ever
// renamed without them.
func TestActiveQueueSelectsMatchStateImported(t *testing.T) {
	lit := "'" + string(models.StateImported) + "'"
	if !strings.Contains(activeOpenSelect, "status <> "+lit) {
		t.Errorf("activeOpenSelect does not exclude %s: %s", lit, activeOpenSelect)
	}
	if !strings.Contains(activeRecentSelect, "status = "+lit) {
		t.Errorf("activeRecentSelect does not select %s: %s", lit, activeRecentSelect)
	}
}

// TestListActive_SameRowsAndOrderAsSingleStatement fills a table with every
// state at several ages, with and without the imported_at and completed_at
// stamps, split over two owners and no owner, several rows sharing one
// added_at, and checks that ListActive and ListActiveByUser return exactly the
// rows the old single statement selected, newest first and highest id first
// within one instant.
func TestListActive_SameRowsAndOrderAsSingleStatement(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (7, 'a', 'x', 'user'), (8, 'b', 'x', 'user')`); err != nil {
		t.Fatal(err)
	}
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	bound := now.Add(-7 * 24 * time.Hour)

	// 'queued' is the column default and not a state the code writes any more;
	// a row left in it must still be listed.
	states := append(models.AllStates(), models.DownloadState("queued"))
	ages := []time.Duration{time.Hour, 6 * 24 * time.Hour, 8 * 24 * time.Hour, 300 * 24 * time.Hour}
	owners := []any{nil, int64(7), int64(8)}
	n := 0
	for _, s := range states {
		for ai, age := range ages {
			for variant := 0; variant < 3; variant++ {
				n++
				at := now.Add(-age)
				// Half the rows share an added_at with a neighbour.
				added := at.Add(-time.Duration(n/2) * time.Minute)
				var imported, completed any
				switch variant {
				case 0:
					imported, completed = at, at
				case 1:
					completed = at // a row from before imported_at was stamped
				}
				if _, err := database.Exec(
					`INSERT INTO downloads (guid, title, nzb_url, status, added_at, completed_at, imported_at, owner_user_id)
					 VALUES (?, ?, '', ?, ?, ?, ?, ?)`,
					fmt.Sprintf("g-%s-%d-%d", s, ai, variant), "t", string(s), added, completed, imported, owners[n%len(owners)],
				); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	wantIDs := func(scope string, args ...any) []int64 {
		t.Helper()
		rows, err := database.Query(legacyActiveSQL+scope+" ORDER BY added_at DESC, id DESC",
			append([]any{models.StateImported, bound}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	idsOf := func(ds []models.Download) []int64 {
		out := make([]int64, len(ds))
		for i, d := range ds {
			out[i] = d.ID
		}
		return out
	}
	same := func(name string, got, want []int64) {
		t.Helper()
		if len(want) == 0 {
			t.Fatalf("%s: the reference statement selected nothing, the fixture is wrong", name)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: rows or order differ from the single statement\n got %v\nwant %v", name, got, want)
		}
	}

	all, err := r.ListActive(ctx, bound)
	if err != nil {
		t.Fatal(err)
	}
	same("ListActive", idsOf(all), wantIDs(""))
	if len(all) == n {
		t.Fatal("every row was listed, so the fixture has no import old enough to hide")
	}
	for _, uid := range []int64{7, 8} {
		got, err := r.ListActiveByUser(ctx, uid, bound)
		if err != nil {
			t.Fatal(err)
		}
		same(fmt.Sprintf("ListActiveByUser(%d)", uid), idsOf(got), wantIDs(" AND owner_user_id = ?", uid))
		for _, d := range got {
			if d.OwnerUserID != uid {
				t.Errorf("ListActiveByUser(%d) returned download %d owned by %d", uid, d.ID, d.OwnerUserID)
			}
		}
	}
}

// TestListActive_PlanDoesNotWalkTheWholeTable is the reason for migration 140.
// The queue list must find the rows that are not imported through
// idx_downloads_not_imported and the recent imports through
// idx_downloads_status_finished. Before the migration the plan was a single
// "SCAN downloads USING INDEX idx_downloads_added_at", which fetches every row
// of the table, old imports included, on every 5 second poll.
func TestListActive_PlanDoesNotWalkTheWholeTable(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	q, args := activeQuery(0, time.Now().Add(-7*24*time.Hour))
	plan := strings.Join(explainPlan(t, database, q, args...), "\n")
	for _, want := range []string{
		"USING INDEX idx_downloads_not_imported",
		"USING INDEX idx_downloads_status_finished (status=? AND <expr>>?)",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "idx_downloads_added_at") || strings.Contains(plan, "SCAN downloads\n") || strings.HasSuffix(plan, "SCAN downloads") {
		t.Errorf("plan walks the whole downloads table:\n%s", plan)
	}

	// Scoped to one owner the plan may go through the owner index instead, but
	// it must still never be a walk of every row.
	q, args = activeQuery(7, time.Now().Add(-7*24*time.Hour))
	scoped := strings.Join(explainPlan(t, database, q, args...), "\n")
	if strings.Contains(scoped, "idx_downloads_added_at") || strings.Contains(scoped, "SCAN downloads\n") || strings.HasSuffix(scoped, "SCAN downloads") {
		t.Errorf("scoped plan walks the whole downloads table:\n%s", scoped)
	}
}
