package models

import "slices"

// ProviderOwnedBookField describes one Book field whose value comes from a
// metadata provider record and nowhere else, so a path that adopts a different
// record may clear it when that record genuinely lacks it (#3104).
type ProviderOwnedBookField struct {
	// Name is the JSON name of the field on Book.
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

// ProviderOwnedBookFields is the ONE list of Book fields that are provider
// owned and safe to clear when the record a book is bound to changes and the
// new record has no value for them. Rebind uses it today; an author refresh
// that adopts a different record should use the same list rather than invent
// its own.
//
// Adding a field here means a user can lose its value to a record that lacks
// it, so each entry has a reason:
//
//   - releaseDate, language, description, genres: shown as the book's own, so a
//     stale value from the previous record makes the book look like it came
//     from the new one. All four are lockable, and a locked one is never
//     cleared.
//   - originalTitle: only ever supplied by a provider and not editable.
//   - averageRating (with ratingsCount): a rating belongs to one provider's
//     audience, and carrying it across records is a wrong number, not a gap.
//
// Deliberately NOT here, and why:
//
//   - title, sortTitle: a record without a title is a broken record, never a
//     signal to blank the book.
//   - imageUrl: a cover can come from the user's own file, an import or an
//     edition, and an empty cover is a worse state than a stale one.
//   - page count: it is not a Book field. It lives on the editions, which a
//     rebind already replaces (#2781).
//   - narrator, durationSeconds, asin, mediaType, calibre_id, file paths,
//     status, monitored: owned by the user or the import pipeline.
//   - series membership: relinked from the record's own series refs.
var ProviderOwnedBookFields = []ProviderOwnedBookField{
	{Name: "releaseDate", Lock: BookFieldReleaseDate,
		missingIn: func(s *Book) bool { return s.ReleaseDate == nil },
		clear:     func(b *Book) { b.ReleaseDate = nil }},
	{Name: "language", Lock: BookFieldLanguage,
		missingIn: func(s *Book) bool { return s.Language == "" },
		clear:     func(b *Book) { b.Language = "" }},
	{Name: "description", Lock: BookFieldDescription,
		missingIn: func(s *Book) bool { return s.Description == "" },
		clear:     func(b *Book) { b.Description = "" }},
	{Name: "genres", Lock: BookFieldGenres,
		missingIn: func(s *Book) bool { return len(s.Genres) == 0 },
		clear:     func(b *Book) { b.Genres = []string{} }},
	{Name: "originalTitle",
		missingIn: func(s *Book) bool { return s.OriginalTitle == "" },
		clear:     func(b *Book) { b.OriginalTitle = "" }},
	{Name: "averageRating",
		missingIn: func(s *Book) bool { return s.AverageRating <= 0 },
		clear:     func(b *Book) { b.AverageRating = 0; b.RatingsCount = 0 }},
}

// ClearProviderFieldsMissingFrom empties every ProviderOwnedBookFields entry
// on b that src has no value for, except a field named in lockedBefore.
//
// lockedBefore is the lock set the book had BEFORE the caller touched it. It is
// a parameter, not b.LockedFields, because a rebind drops the locks as part of
// adopting the new record; the user's lock still decides what may be erased.
// Fields src does have a value for are not touched here: the caller writes
// those. It returns the names it cleared.
func (b *Book) ClearProviderFieldsMissingFrom(src *Book, lockedBefore []string) []string {
	if b == nil || src == nil {
		return nil
	}
	var cleared []string
	for _, f := range ProviderOwnedBookFields {
		if !f.missingIn(src) {
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
