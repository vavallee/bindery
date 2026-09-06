import { describe, expect, it } from 'vitest'
import type { Author, Book } from '../api/client'
import { splitAuthorSearchResults } from './addAuthorTitleGuard'

function makeAuthor(overrides: Partial<Author>): Author {
  return {
    id: 1,
    foreignAuthorId: 'OL_AUTHOR_A',
    authorName: 'Author',
    sortName: 'Author',
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
    ...overrides,
  }
}

function makeBook(overrides: Partial<Book>): Book {
  return {
    id: 1,
    foreignBookId: 'OL_BOOK_W',
    authorId: 1,
    title: 'Book',
    description: '',
    imageUrl: '',
    genres: [],
    monitored: true,
    status: 'wanted',
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    ...overrides,
  }
}

describe('splitAuthorSearchResults', () => {
  it('hides an exact book-title author inversion', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_BAD_TITLE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    const book = makeBook({
      title: 'Romeo and Juliet',
      author: makeAuthor({
        foreignAuthorId: 'OL_SHAKESPEARE_A',
        authorName: 'William Shakespeare',
      }),
    })

    const split = splitAuthorSearchResults([candidate], [book], 'Romeo and Juliet')

    expect(split.visible).toEqual([])
    expect(split.hidden).toEqual([candidate])
  })

  it('preserves legitimate author results with exact-name searches', () => {
    const author = makeAuthor({
      foreignAuthorId: 'OL_SHAKESPEARE_A',
      authorName: 'William Shakespeare',
      disambiguation: 'Romeo and Juliet',
    })
    const book = makeBook({
      title: 'Romeo and Juliet',
      author,
    })

    const split = splitAuthorSearchResults([author], [book], 'William Shakespeare')

    expect(split.visible).toEqual([author])
    expect(split.hidden).toEqual([])
  })

  it('preserves results when book metadata lacks author data', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_BAD_TITLE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    const book = makeBook({ title: 'Romeo and Juliet' })

    const split = splitAuthorSearchResults([candidate], [book], 'Romeo and Juliet')

    expect(split.visible).toEqual([candidate])
    expect(split.hidden).toEqual([])
  })

  it('preserves results when candidate and book author IDs match', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_SHAKESPEARE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    const book = makeBook({
      title: 'Romeo and Juliet',
      author: makeAuthor({
        foreignAuthorId: 'OL_SHAKESPEARE_A',
        authorName: 'William Shakespeare',
      }),
    })

    const split = splitAuthorSearchResults([candidate], [book], 'Romeo and Juliet')

    expect(split.visible).toEqual([candidate])
    expect(split.hidden).toEqual([])
  })

  it('does not hide partial or merely similar title matches', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_ROMEO_A',
      authorName: 'Romeo',
      disambiguation: 'William Shakespeare',
    })
    const book = makeBook({
      title: 'Romeo and Juliet',
      author: makeAuthor({
        foreignAuthorId: 'OL_SHAKESPEARE_A',
        authorName: 'William Shakespeare',
      }),
    })

    const split = splitAuthorSearchResults([candidate], [book], 'Romeo')

    expect(split.visible).toEqual([candidate])
    expect(split.hidden).toEqual([])
  })
})

// The guard compares three strings that come from two providers plus the user's
// keyboard, so it is the shape of comparison that fails when the two sides fold
// differently. Its normalizer used to be a private copy — the only NFKC fold in
// the project, with no Go counterpart; it now delegates to the shared
// foldForSearch, which additionally folds accents and deletes apostrophes.
describe('splitAuthorSearchResults folds the way the search box does', () => {
  const shakespeare = makeAuthor({
    foreignAuthorId: 'OL_SHAKESPEARE_A',
    authorName: 'William Shakespeare',
  })

  it('hides a title-shaped result when the query differs by case', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_BAD_TITLE_A',
      authorName: 'Romeo and Juliet',
      disambiguation: 'William Shakespeare',
    })
    const book = makeBook({ title: 'Romeo and Juliet', author: shakespeare })

    const split = splitAuthorSearchResults([candidate], [book], 'ROMEO AND JULIET')

    expect(split.hidden).toEqual([candidate])
  })

  it('hides it across apostrophe spellings', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_ENDER_A',
      authorName: "Ender's Game",
      disambiguation: 'Orson Scott Card',
    })
    const card = makeAuthor({ foreignAuthorId: 'OL_CARD_A', authorName: 'Orson Scott Card' })
    const book = makeBook({ title: 'Ender’s Game', author: card })

    const split = splitAuthorSearchResults([candidate], [book], "Ender's Game")

    expect(split.hidden).toEqual([candidate])
  })

  it('hides it across accented spellings, which the previous ASCII fold could not', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_MUERTE_A',
      authorName: 'Muerte Súbita',
      disambiguation: 'Alvaro Enrigue',
    })
    const enrigue = makeAuthor({ foreignAuthorId: 'OL_ENRIGUE_A', authorName: 'Álvaro Enrigue' })
    const book = makeBook({ title: 'Muerte Subita', author: enrigue })

    const split = splitAuthorSearchResults([candidate], [book], 'Muerte Súbita')

    expect(split.hidden).toEqual([candidate])
  })

  it('still hides nothing when the titles genuinely differ', () => {
    const candidate = makeAuthor({
      foreignAuthorId: 'OL_HAMLET_A',
      authorName: 'Hamlet',
      disambiguation: 'William Shakespeare',
    })
    const book = makeBook({ title: 'Romeo and Juliet', author: shakespeare })

    const split = splitAuthorSearchResults([candidate], [book], 'Hamlet')

    expect(split.visible).toEqual([candidate])
    expect(split.hidden).toEqual([])
  })
})
