package models

import (
	"slices"
	"strings"
	"time"
)

// ProviderOwnedBookField describes one Book field whose value usually comes
// from a metadata provider record, so a path that adopts a different record
// may clear it when that record genuinely lacks it (#3104).
type ProviderOwnedBookField struct {
	// Name is the JSON name of the field on Book. It is also the name a
	// fetch puts in Book.UnknownFields when it could not determine the field.
	Name string
	// Lock is the BookField* lock name that protects the field from being
	// cleared, or "" when the field cannot be edited by hand and so has no
	// lock.
	Lock string
	// missingIn reports whether the record carries no value for the field.
	missingIn func(src *Book) bool
	// clear empties the field on dst.
	clear func(dst *Book)
}

// ProviderOwnedBookFields is the ONE list of Book fields that a rebind may
// empty when the record it adopts has no value for them. Rebind uses it today;
// an author refresh that adopts a different record should use the same list
// rather than invent its own.
//
// "Provider owned" says where the value usually comes from. It is not proof the
// user did not supply it. Bindery tracks user edits only through
// Book.LockedFields, so a value that an Audiobookshelf or Calibre import wrote,
// or one the user typed without it being locked, is cleared like any other.
// There is no finer tracking, by design.
//
// Two protections apply, both in ClearProviderFieldsMissingFrom: a field the
// book had locked before the rebind is never emptied, and a field the fetch
// listed in Book.UnknownFields (a secondary call failed, or the query never
// asks for it) is never emptied, because an empty value there is not evidence
// the record lacks it.
//
//   - releaseDate, language, description, genres: shown as the book's own, so a
//     stale value from the previous record makes the book look like it came
//     from the new one. All four are lockable.
//
// Deliberately NOT here, and why:
//
//   - averageRating, ratingsCount: OpenLibrary's single-book fetch never
//     returns a rating (it arrives through enrichment, which a rebind skips),
//     so listing them would erase a rating on every rebind to OpenLibrary.
//   - originalTitle: neither provider's single-book fetch supplies it.
//   - title, sortTitle: a record without a title is broken, never a signal to
//     blank the book.
//   - imageUrl: a cover can come from the user's own file, an import or an
//     edition, and an empty cover is a worse state than a stale one.
//   - page count: it is not a Book field. It lives on the editions, which a
//     rebind already replaces (#2781).
//   - narrator, durationSeconds, asin, mediaType, calibre_id, file paths,
//     status, monitored: owned by the user or the import pipeline.
//   - series membership: relinked from the record's own series refs.
var ProviderOwnedBookFields = []ProviderOwnedBookField{
	{Name: BookFieldReleaseDate, Lock: BookFieldReleaseDate,
		missingIn: func(s *Book) bool { return s.ReleaseDate == nil },
		clear:     func(b *Book) { b.ReleaseDate = nil }},
	{Name: BookFieldLanguage, Lock: BookFieldLanguage,
		missingIn: func(s *Book) bool { return s.Language == "" },
		clear:     func(b *Book) { b.Language = "" }},
	{Name: BookFieldDescription, Lock: BookFieldDescription,
		missingIn: func(s *Book) bool { return s.Description == "" },
		clear:     func(b *Book) { b.Description = "" }},
	{Name: BookFieldGenres, Lock: BookFieldGenres,
		missingIn: func(s *Book) bool { return len(s.Genres) == 0 },
		clear:     func(b *Book) { b.Genres = []string{} }},
}

// ClearProviderFieldsMissingFrom empties every ProviderOwnedBookFields entry
// on b that src has no value for, except a field named in lockedBefore.
//
// lockedBefore is the lock set the book had BEFORE the caller touched it. It is
// a parameter, not b.LockedFields, because a rebind drops the locks as part of
// adopting the new record; the user's lock still decides what may be erased.
// A field named in src.UnknownFields is skipped too (see ProviderOwnedBookFields).
// Fields src does have a value for are not touched here: the caller writes
// those. It returns the names it cleared.
func (b *Book) ClearProviderFieldsMissingFrom(src *Book, lockedBefore []string) []string {
	if b == nil || src == nil {
		return nil
	}
	var cleared []string
	for _, f := range ProviderOwnedBookFields {
		if !f.missingIn(src) || slices.Contains(src.UnknownFields, f.Name) {
			continue
		}
		if f.Lock != "" && slices.Contains(lockedBefore, f.Lock) {
			continue
		}
		f.clear(b)
		cleared = append(cleared, f.Name)
	}
	return cleared
}

// AdoptRefreshedProviderFields copies the release date and, when empty, the
// description from src, the provider record an author refresh just read for the
// work b already is, onto b. It returns the names it changed (#3105).
//
// The caller decides WHICH rows may use it: only a row the record's own id
// resolved (src.ForeignID == b.ForeignID), never one matched by title, where
// src can be another work with the same name.
//
// Unlike a rebind, a refresh NEVER empties a field: clearing is
// ClearProviderFieldsMissingFrom's job. Every field below also needs: not in
// b.LockedFields, not in src.UnknownFields, and a value in src.
//
//   - releaseDate: replaced when it differs. A catalogue date is year granular
//     (Jan 1), so it does not replace a date in the same year, which may be a
//     precise one from an import and says nothing new. A date the merge filled
//     in from a supplementary provider is marked unknown on src and not used.
//   - description: filled only when b has none. Bindery has no record of what
//     wrote a description (a typed one is locked, an import's is not), and
//     replacing it on every refresh would undo the import.
//
// Language is deliberately NOT adopted: a catalogue record's language can be a
// guess (an edition majority, or the author's majority language). Genres are
// not either: the refresh already updates them from Hardcover provenance.
//
// Nothing happens unless src comes from the provider b's own record came from.
func (b *Book) AdoptRefreshedProviderFields(src *Book) []string {
	if b == nil || src == nil || b.MetadataProvider == "" ||
		!strings.EqualFold(b.MetadataProvider, src.MetadataProvider) {
		return nil
	}
	writable := func(field string) bool {
		return !b.IsFieldLocked(field) && !slices.Contains(src.UnknownFields, field)
	}
	var changed []string
	if src.ReleaseDate != nil && writable(BookFieldReleaseDate) && !sameReleaseDate(b.ReleaseDate, src.ReleaseDate) {
		d := *src.ReleaseDate
		b.ReleaseDate = &d
		changed = append(changed, BookFieldReleaseDate)
	}
	if strings.TrimSpace(src.Description) != "" && strings.TrimSpace(b.Description) == "" && writable(BookFieldDescription) {
		b.Description = src.Description
		changed = append(changed, BookFieldDescription)
	}
	return changed
}

// sameReleaseDate reports whether adopting incoming would tell the book
// nothing new: the same instant, or a Jan 1 (year granular) incoming date in
// the year the book already has.
func sameReleaseDate(have, incoming *time.Time) bool {
	if have == nil {
		return false
	}
	if have.Equal(*incoming) {
		return true
	}
	u := incoming.UTC()
	yearOnly := u.Month() == time.January && u.Day() == 1 && u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0
	return yearOnly && have.UTC().Year() == u.Year()
}
