package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// AuthorAliasRepo owns the author_aliases table plus the cross-table Merge
// operation. Merge needs access to authors + books too, so it takes the
// relevant column updates inline rather than reaching back into AuthorRepo
// and BookRepo (which would drag two repos into the transaction boundary
// for no benefit).
type AuthorAliasRepo struct {
	db *sql.DB
}

func NewAuthorAliasRepo(db *sql.DB) *AuthorAliasRepo {
	return &AuthorAliasRepo{db: db}
}

// ListByAuthor returns every alias row pointing at the given canonical
// author, newest first.
func (r *AuthorAliasRepo) ListByAuthor(ctx context.Context, authorID int64) ([]models.AuthorAlias, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, author_id, name, COALESCE(source_ol_id, ''), created_at
		FROM author_aliases WHERE author_id = ? ORDER BY created_at DESC, id DESC`, authorID)
	if err != nil {
		return nil, fmt.Errorf("list aliases for author %d: %w", authorID, err)
	}
	defer rows.Close()

	var out []models.AuthorAlias
	for rows.Next() {
		var a models.AuthorAlias
		if err := rows.Scan(&a.ID, &a.AuthorID, &a.Name, &a.SourceOLID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LookupByName returns the canonical author id for the given name, or nil if
// no alias matches. The comparison is case-insensitive and trimmed so the
// caller doesn't need to normalise before calling.
func (r *AuthorAliasRepo) LookupByName(ctx context.Context, name string) (*int64, error) {
	normalized := strings.TrimSpace(name)
	if normalized == "" {
		return nil, nil
	}
	var id int64
	row := r.db.QueryRowContext(ctx,
		"SELECT author_id FROM author_aliases WHERE LOWER(name) = LOWER(?)", normalized)
	err := row.Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup alias %q: %w", normalized, err)
	}
	return &id, nil
}

// Create inserts an alias row. Idempotent on `name` — a duplicate name for
// the *same* author is a no-op; a duplicate name for a *different* author
// returns an error (the caller is trying to point one alias at two authors,
// which would make LookupByName ambiguous).
func (r *AuthorAliasRepo) Create(ctx context.Context, a *models.AuthorAlias) error {
	return r.createTx(ctx, r.db, a)
}

func (r *AuthorAliasRepo) createTx(ctx context.Context, exec sqlExecutor, a *models.AuthorAlias) error {
	now := time.Now().UTC()
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return fmt.Errorf("alias name required")
	}
	result, err := exec.ExecContext(ctx, `
		INSERT OR IGNORE INTO author_aliases (author_id, name, source_ol_id, created_at)
		VALUES (?, ?, NULLIF(?, ''), ?)`,
		a.AuthorID, name, a.SourceOLID, now)
	if err != nil {
		return fmt.Errorf("create alias %q: %w", name, err)
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		id, _ := result.LastInsertId()
		a.ID = id
		a.Name = name
		a.CreatedAt = now
		return nil
	}
	// Row existed — confirm it points at the same author. If not, the caller
	// is asking us to reassign an alias without going through Merge, which
	// silently breaks LookupByName for the previous owner.
	var existingAuthor int64
	row := exec.QueryRowContext(ctx, "SELECT id, author_id, created_at FROM author_aliases WHERE LOWER(name) = LOWER(?)", name)
	if err := row.Scan(&a.ID, &existingAuthor, &a.CreatedAt); err != nil {
		return fmt.Errorf("read existing alias %q: %w", name, err)
	}
	if existingAuthor != a.AuthorID {
		return fmt.Errorf("alias %q already points at author %d (refusing to reassign to %d)", name, existingAuthor, a.AuthorID)
	}
	a.Name = name
	return nil
}

// Delete removes a single alias row by id.
func (r *AuthorAliasRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM author_aliases WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete alias %d: %w", id, err)
	}
	return nil
}

// MergeOptions controls how field-level settings are carried from source to
// target during Merge. `OverwriteDefaults` means "copy source.monitored /
// *_profile_id / root_folder_id onto target only if target's value looks
// unset (nil pointer, or monitored matching the seeded default of true)".
// Set to false to leave target untouched.
type MergeOptions struct {
	OverwriteDefaults bool
}

// MergeResult describes what Merge changed, for logging and UI confirmation.
type MergeResult struct {
	BooksReparented int64
	AliasesMigrated int64
	AliasesCreated  int64
	TargetUpdated   bool
}

// Merge collapses sourceID into targetID:
//   - every book pointing at sourceID is reparented to targetID
//   - every alias pointing at sourceID is repointed at targetID
//   - source.name (+ source.foreign_id if set) become alias rows on target
//   - target.monitored / *_profile_id / root_folder_id are copied from source
//     only where target's value is a default (per MergeOptions)
//   - sourceID is deleted
//
// Executes in a single transaction: any child-update failure rolls back
// everything. source and target must be different and both must exist.
func (r *AuthorAliasRepo) Merge(ctx context.Context, sourceID, targetID int64, opts MergeOptions) (*MergeResult, error) {
	if sourceID == targetID {
		return nil, fmt.Errorf("merge source and target must differ (both = %d)", sourceID)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin merge tx: %w", err)
	}
	defer func() {
		// Safe to call after Commit — the driver returns ErrTxDone which we
		// ignore. Guards the error paths below.
		_ = tx.Rollback()
	}()

	source, err := loadAuthorForMerge(ctx, tx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("load merge source %d: %w", sourceID, err)
	}
	target, err := loadAuthorForMerge(ctx, tx, targetID)
	if err != nil {
		return nil, fmt.Errorf("load merge target %d: %w", targetID, err)
	}

	result := &MergeResult{}

	// Reparent books. FK has no ON DELETE action that would interfere — the
	// cascade is authors → books, and we're updating books away before the
	// source author row is removed.
	booksRes, err := tx.ExecContext(ctx,
		"UPDATE books SET author_id = ?, updated_at = ? WHERE author_id = ?",
		targetID, time.Now().UTC(), sourceID)
	if err != nil {
		return nil, fmt.Errorf("reparent books: %w", err)
	}
	result.BooksReparented, _ = booksRes.RowsAffected()

	// Migrate existing aliases on source to target. Because `name` is UNIQUE,
	// a collision with a pre-existing alias on target would 2067 us; use
	// INSERT OR IGNORE semantics via a conditional update.
	migrateRes, err := tx.ExecContext(ctx, `
		UPDATE author_aliases SET author_id = ?
		WHERE author_id = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM author_aliases existing
		    WHERE LOWER(existing.name) = LOWER(author_aliases.name)
		      AND existing.author_id = ?
		  )`, targetID, sourceID, targetID)
	if err != nil {
		return nil, fmt.Errorf("migrate aliases: %w", err)
	}
	result.AliasesMigrated, _ = migrateRes.RowsAffected()

	// Drop any source aliases that collided with an existing target alias —
	// their name is already represented and the FK will cascade them when we
	// delete source, but we prefer an explicit cleanup so the author row
	// delete below is pure.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM author_aliases WHERE author_id = ?", sourceID); err != nil {
		return nil, fmt.Errorf("drop orphan source aliases: %w", err)
	}

	// Record source.name + source.foreign_id as aliases on target, unless
	// they already exist.
	if alias, err := insertMergeAlias(ctx, tx, targetID, source.Name, source.ForeignID); err != nil {
		return nil, fmt.Errorf("record source name alias: %w", err)
	} else if alias {
		result.AliasesCreated++
	}

	// Propagate monitored/profile fields. Target's seeded defaults
	// (monitored=1, nil profile / root-folder ids) lose to whatever source
	// had, on the theory that the user configured the source intentionally
	// and expects those settings to survive the merge. Disabled by option so
	// the API can flip it off if the UI explicitly says "keep target's
	// settings".
	if opts.OverwriteDefaults {
		updated, err := applyMergeFieldCopy(ctx, tx, source, target)
		if err != nil {
			return nil, err
		}
		result.TargetUpdated = updated
	}

	// Finally, remove the source author. FK cascade handles any stragglers
	// (books/aliases have already been moved). Tags and other author-scoped
	// rows that use ON DELETE CASCADE vanish automatically — that's the
	// desired outcome since they were source-specific.
	if _, err := tx.ExecContext(ctx, "DELETE FROM authors WHERE id = ?", sourceID); err != nil {
		return nil, fmt.Errorf("delete source author: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit merge: %w", err)
	}
	return result, nil
}

// insertMergeAlias records the source author's name (and optionally its
// foreign OL id) as an alias on target. Returns true if a new row landed.
func insertMergeAlias(ctx context.Context, tx *sql.Tx, targetID int64, name, foreignID string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO author_aliases (author_id, name, source_ol_id, created_at)
		VALUES (?, ?, NULLIF(?, ''), ?)`,
		targetID, name, foreignID, now)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// applyMergeFieldCopy carries source.monitored / profile / root_folder onto
// target when target's current value is the seeded default. Returns true
// when any column was actually written.
//
// Each field is UPDATEd individually with a fixed SQL literal (rather than
// a single dynamically-composed UPDATE) to keep the SQL out of gosec's
// concatenation heuristic and to keep the code trivially auditable.
func applyMergeFieldCopy(ctx context.Context, tx *sql.Tx, source, target *mergeAuthor) (bool, error) {
	now := time.Now().UTC()
	var wrote bool

	apply := func(query string, args ...any) error {
		if _, err := tx.ExecContext(ctx, query, append(args, now, target.ID)...); err != nil {
			return fmt.Errorf("copy merge fields onto target: %w", err)
		}
		wrote = true
		return nil
	}

	// Target monitored=true is the seeded default (migration 001); if the
	// user explicitly unmonitored the target, keep it.
	if target.Monitored && !source.Monitored {
		if err := apply("UPDATE authors SET monitored = 0, updated_at = ? WHERE id = ?"); err != nil {
			return false, err
		}
	}
	if target.QualityProfileID == nil && source.QualityProfileID != nil {
		if err := apply("UPDATE authors SET quality_profile_id = ?, updated_at = ? WHERE id = ?", *source.QualityProfileID); err != nil {
			return false, err
		}
	}
	if target.MetadataProfileID == nil && source.MetadataProfileID != nil {
		if err := apply("UPDATE authors SET metadata_profile_id = ?, updated_at = ? WHERE id = ?", *source.MetadataProfileID); err != nil {
			return false, err
		}
	}
	if target.RootFolderID == nil && source.RootFolderID != nil {
		if err := apply("UPDATE authors SET root_folder_id = ?, updated_at = ? WHERE id = ?", *source.RootFolderID); err != nil {
			return false, err
		}
	}
	return wrote, nil
}

// mergeAuthor is the slim row shape Merge needs — just the id + fields it
// might carry across. Avoids coupling this file to AuthorRepo's scanners.
type mergeAuthor struct {
	ID                int64
	Name              string
	ForeignID         string
	Monitored         bool
	QualityProfileID  *int64
	MetadataProfileID *int64
	RootFolderID      *int64
}

func loadAuthorForMerge(ctx context.Context, tx *sql.Tx, id int64) (*mergeAuthor, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, foreign_id, name, monitored, quality_profile_id, metadata_profile_id, root_folder_id
		FROM authors WHERE id = ?`, id)
	var a mergeAuthor
	var monitored int
	err := row.Scan(&a.ID, &a.ForeignID, &a.Name, &monitored, &a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("author %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	a.Monitored = monitored == 1
	return &a, nil
}

// sqlExecutor is the shared subset of *sql.DB and *sql.Tx we need so Create
// can work either standalone or inside a caller's transaction.
type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
