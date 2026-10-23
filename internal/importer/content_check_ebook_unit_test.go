package importer

import "testing"

func TestEbookTitleMatches(t *testing.T) {
	cases := []struct {
		book, file string
		want       bool
	}{
		{"Mary, Mary: Alex Cross, Book 11", "Mary, Mary", true},
		{"Mary, Mary", "Mary, Mary: Alex Cross, Book 11", true},
		{"Mary, Mary: Alex Cross, Book 11", "Mary and Mr Eliot: A Sort of Love Story", false},
		{"The Racketeer", "", true}, // empty metadata is never a mismatch
		{"The Racketeer", "   ", true},
		{"The Racketeer", "Recursion", false},
	}
	for _, c := range cases {
		if got := ebookTitleMatches(c.book, c.file); got != c.want {
			t.Errorf("ebookTitleMatches(%q, %q) = %v, want %v", c.book, c.file, got, c.want)
		}
	}
}

func TestEbookAuthorMatches(t *testing.T) {
	cases := []struct {
		names   []string
		creator string
		want    bool
	}{
		{[]string{"James Patterson"}, "Mary Trevelyan", false},
		{[]string{"James Patterson"}, "James Patterson", true},
		{[]string{"James Patterson"}, "Patterson, James", true},
		{[]string{"James Patterson"}, "James Patterson and Maxine Paetro", true},
		{[]string{"James Patterson"}, "Patterson, James; Paetro, Maxine", true},
		{[]string{"Leslie McFarlane"}, "Franklin W. Dixon", false},
		{[]string{"Leslie McFarlane", "Franklin W. Dixon"}, "Franklin W. Dixon", true},
		{[]string{"J.R.R. Tolkien"}, "Tolkien, J. R. R.", true},
		{[]string{"James Patterson"}, "", true},
		{[]string{"James Patterson"}, "  ", true},
		{nil, "Mary Trevelyan", true}, // no author on record cannot disprove anything
	}
	for _, c := range cases {
		if got := ebookAuthorMatches(c.names, c.creator); got != c.want {
			t.Errorf("ebookAuthorMatches(%q, %q) = %v, want %v", c.names, c.creator, got, c.want)
		}
	}
}

// TestEbookDefiniteMismatch_NegativeCorpus lists real shapes that must import
// silently under block mode. A false positive here refuses a good download, which
// is worse than missing a mismatch, so each row is a case where title or author
// (or both) tells the check the file is plausibly the book.
func TestEbookDefiniteMismatch_NegativeCorpus(t *testing.T) {
	type row struct {
		name              string
		bookTitle         string
		names             []string
		fileTitle, fileBy string
	}
	rows := []row{
		{"subtitle on the book", "The Hobbit, or There and Back Again", []string{"J.R.R. Tolkien"}, "The Hobbit", "J. R. R. Tolkien"},
		{"subtitle on the file", "Sapiens", []string{"Yuval Noah Harari"}, "Sapiens: A Brief History of Humankind", "Yuval Noah Harari"},
		{"diacritics", "Les Misérables", []string{"Victor Hugo"}, "Les Miserables", "Hugo, Victor"},
		{"translated title, same author", "Voyna i mir", []string{"Leo Tolstoy"}, "War and Peace", "Leo Tolstoy"},
		{"transliterated author, same title", "Crime and Punishment", []string{"Fyodor Dostoevsky"}, "Crime and Punishment", "Fyodor Dostoyevsky"},
		{"filing form", "The Lion, the Witch and the Wardrobe", []string{"C. S. Lewis"}, "Lion, the Witch and the Wardrobe, The", "C.S. Lewis"},
		{"ampersand", "Foundation & Empire", []string{"Isaac Asimov"}, "Foundation and Empire", "Isaac Asimov"},
		{"long series prefix", "The Wheel of Time, Book 1: The Eye of the World", []string{"Robert Jordan"}, "The Eye of the World", "Robert Jordan"},
		{"series prefix, author only differs by spelling", "The Stormlight Archive 4: Rhythm of War", []string{"Brandon Sanderson"}, "Rhythm of War", "Sanderson, Brandon"},
		{"compilation title, author matches", "The Complete Sherlock Holmes", []string{"Arthur Conan Doyle"}, "The Adventures of Sherlock Holmes", "Arthur Conan Doyle"},
		{"anthology, editor credited", "Dangerous Women", []string{"George R. R. Martin"}, "Dangerous Women", "George R. R. Martin; Gardner Dozois"},
		{"anthology, creator says Various", "Dangerous Women", []string{"George R. R. Martin"}, "Dangerous Women", "Various"},
		{"another book by the same author", "Mary, Mary: Alex Cross, Book 11", []string{"James Patterson"}, "Cross", "James Patterson"},
		{"title only, no creator", "Mary, Mary", []string{"James Patterson"}, "Cross", ""},
		{"creator only, no title", "Mary, Mary", []string{"James Patterson"}, "", "Somebody Else"},
		{"co-authored, one credit matches", "The President Is Missing", []string{"James Patterson"}, "The President Is Missing", "Bill Clinton, James Patterson"},
		{"pen name through an alias", "The Tower Treasure", []string{"Leslie McFarlane", "Franklin W. Dixon"}, "Hardy Boys 01", "Franklin W. Dixon"},
		{"edition title differs, author same", "Harry Potter and the Philosopher's Stone", []string{"J. K. Rowling"}, "Harry Potter and the Sorcerer's Stone", "J.K. Rowling"},
		{"placeholder creator with a title variant", "Harry Potter and the Philosopher's Stone", []string{"J.K. Rowling"}, "Harry Potter and the Sorcerer's Stone", "Unknown"},
		{"publisher as creator with a title variant", "Harry Potter and the Philosopher's Stone", []string{"J.K. Rowling"}, "Harry Potter and the Sorcerer's Stone", "Scholastic Inc."},
		{"email as creator with a title variant", "Harry Potter and the Philosopher's Stone", []string{"J.K. Rowling"}, "Harry Potter and the Sorcerer's Stone", "scans@example.org"},
		{"hyphen and apostrophe spelling", "The Hitch-Hiker's Guide to the Galaxy", []string{"Douglas Adams"}, "The Hitchhiker's Guide to the Galaxy", "Somebody Else"},
		{"spelled number", "Slaughterhouse-Five", []string{"Kurt Vonnegut"}, "Slaughterhouse 5", "Somebody Else"},
		{"abbreviation", "Dr. Jekyll and Mr. Hyde", []string{"Robert Louis Stevenson"}, "Doctor Jekyll and Mister Hyde", "Somebody Else"},
		{"Russian edition", "Crime and Punishment", []string{"Fyodor Dostoevsky"}, "Преступление и наказание", "Фёдор Достоевский"},
		{"Hebrew edition", "Sapiens", []string{"Yuval Noah Harari"}, "ספיינס", "יובל נח הררי"},
		{"nothing at all", "Mary, Mary", []string{"James Patterson"}, "", ""},
	}
	for _, r := range rows {
		if ebookDefiniteMismatch(r.bookTitle, r.names, r.fileTitle, r.fileBy) {
			t.Errorf("%s: %q by %v vs file %q by %q was flagged, want silent import", r.name, r.bookTitle, r.names, r.fileTitle, r.fileBy)
		}
	}
}

func TestEbookDefiniteMismatch_RealMismatches(t *testing.T) {
	rows := []struct {
		name              string
		bookTitle         string
		names             []string
		fileTitle, fileBy string
	}{
		{"the #3015 report", "Mary, Mary: Alex Cross, Book 11", []string{"James Patterson"}, "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan"},
		{"another book entirely", "The Racketeer", []string{"John Grisham"}, "Recursion", "Blake Crouch"},
		{"house name with no alias on record", "The Tower Treasure", []string{"Leslie McFarlane"}, "Moby Dick", "Franklin W. Dixon"},
	}
	for _, r := range rows {
		if !ebookDefiniteMismatch(r.bookTitle, r.names, r.fileTitle, r.fileBy) {
			t.Errorf("%s: %q by %v vs file %q by %q was not flagged", r.name, r.bookTitle, r.names, r.fileTitle, r.fileBy)
		}
	}
}
