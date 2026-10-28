package models

import "slices"

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
