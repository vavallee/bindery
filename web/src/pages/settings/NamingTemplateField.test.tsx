import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useState } from 'react'

// i18n: echo the key (plus interpolated options) so assertions are stable.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (!options) return key
      let out = key
      for (const [k, v] of Object.entries(options)) {
        out += ` ${k}=${String(v)}`
      }
      return out
    },
  }),
}))

import NamingTemplateField from './NamingTemplateField'
import { renderTemplate, validateTemplate, SAMPLE_BOOK } from './namingTemplate'

// Controlled wrapper so onChange actually updates the rendered value, the way
// GeneralTab wires it.
function Harness({ kind = 'book' as const, initial = '' }: { kind?: 'book' | 'audiobook'; initial?: string }) {
  const [value, setValue] = useState(initial)
  return (
    <NamingTemplateField
      label="Book Template"
      kind={kind}
      placeholder="placeholder"
      value={value}
      onChange={setValue}
      onSave={() => {}}
      saving={false}
    />
  )
}

describe('namingTemplate renderer (renamer.go mirror)', () => {
  it('renders the default ebook template against the sample book', () => {
    const out = renderTemplate('{Author}/{Title} ({Year})/{Title} - {Author}.{ext}', 'book')
    expect(out).toBe('Jane Doe/Sample Book (2024)/Sample Book - Jane Doe.epub')
  })

  it('renders all tokens', () => {
    const out = renderTemplate('{Author}|{SortAuthor}|{Title}|{Year}|{ASIN}|{Series}|{SeriesNumber}|{Genre}|{Lang}|{ext}', 'book')
    // "|" is stripped by sanitize only inside a substituted field, not in the
    // literal template, so the separators survive between tokens.
    expect(out).toBe('Jane Doe|Doe, Jane|Sample Book|2024|B01ABCDEFG|Demo Series|2|Fantasy|en|epub')
  })

  it('renders the {Lang} token and collapses its glue when empty', () => {
    expect(renderTemplate('{Title} [{Lang}]', 'book')).toBe('Sample Book [en]')
    const noLang = { ...SAMPLE_BOOK, lang: '' }
    expect(renderTemplate('{Lang}/{Title}.{ext}', 'book', noLang)).toBe('Sample Book.epub')
  })

  it('renders {ext} as empty for the audiobook (folder) kind', () => {
    expect(renderTemplate('{Title}.{ext}', 'audiobook')).toBe('Sample Book.')
    expect(renderTemplate('{Title}.{ext}', 'book')).toBe('Sample Book.epub')
  })

  it('sanitizes characters that would break a path inside a field', () => {
    const out = renderTemplate('{Title}', 'book', { ...SAMPLE_BOOK, title: 'A: B / C? <D>' })
    // ":" and "/" -> "-", "?<>" stripped; result is one segment
    expect(out).toBe('A- B - C D')
  })

  it('drops dangling leading separators when a leading token is empty', () => {
    const noSeries = { ...SAMPLE_BOOK, series: '', seriesNumber: '' }
    // Discord report: "{SeriesNumber} - {Title}" with no number must not yield " - Title".
    expect(
      renderTemplate('{Author}/{Series}/{SeriesNumber} - {Title}.{ext}', 'book', noSeries),
    ).toBe('Jane Doe/Sample Book.epub')
    // Consecutive empty leading tokens collapse.
    expect(
      renderTemplate('{Author}/{Series} - {SeriesNumber} - {Title}.{ext}', 'book', noSeries),
    ).toBe('Jane Doe/Sample Book.epub')
    // Interior/trailing glue is preserved (empty {Year} still yields "()").
    expect(
      renderTemplate('{Title} ({Year})', 'book', { ...SAMPLE_BOOK, year: '' }),
    ).toBe('Sample Book ()')
  })

  it('renders the {Genre} token and {Token:default} fallback', () => {
    expect(renderTemplate('{Genre}/{Title}.{ext}', 'book')).toBe('Fantasy/Sample Book.epub')
    const noGenre = { ...SAMPLE_BOOK, genre: '' }
    // Empty genre drops the segment...
    expect(renderTemplate('{Genre}/{Title}.{ext}', 'book', noGenre)).toBe('Sample Book.epub')
    // ...unless a default is given (mirrors Calibre's ifempty(Unsorted)).
    expect(renderTemplate('{Genre:Unsorted}/{Title}.{ext}', 'book', noGenre)).toBe('Unsorted/Sample Book.epub')
    // Default is ignored when the token has a value.
    expect(renderTemplate('{Genre:Unsorted}/{Title}.{ext}', 'book')).toBe('Fantasy/Sample Book.epub')
  })

  it('treats {Genre:Unsorted} as a known token in validation', () => {
    expect(validateTemplate('{Genre:Unsorted}/{Author}/{Title}.{ext}').unknownTokens).toEqual([])
    expect(validateTemplate('{Bogus:x}').unknownTokens).toEqual(['{Bogus:x}'])
  })
})

describe('validateTemplate', () => {
  it('flags unknown tokens (case-sensitive)', () => {
    const r = validateTemplate('{author}/{Title}.{ext}')
    expect(r.unknownTokens).toEqual(['{author}'])
    expect(r.empty).toBe(false)
    expect(r.traversal).toBe(false)
  })

  it('flags empty templates', () => {
    expect(validateTemplate('   ').empty).toBe(true)
  })

  it('flags traversal segments', () => {
    expect(validateTemplate('../{Title}').traversal).toBe(true)
    expect(validateTemplate('{Author}/../etc').traversal).toBe(true)
    expect(validateTemplate('{Author}/{Title}').traversal).toBe(false)
  })

  it('accepts a fully valid template', () => {
    const r = validateTemplate('{Author}/{Title} ({Year}).{ext}')
    expect(r.unknownTokens).toEqual([])
    expect(r.empty).toBe(false)
    expect(r.traversal).toBe(false)
  })
})

describe('NamingTemplateField component', () => {
  beforeEach(() => vi.clearAllMocks())

  it('shows a live preview that updates as you type', () => {
    render(<Harness initial="{Author}/{Title}.{ext}" />)
    const preview = screen.getByTestId('naming-preview-book')
    expect(preview.textContent).toBe('Jane Doe/Sample Book.epub')

    const input = screen.getByPlaceholderText('placeholder')
    fireEvent.change(input, { target: { value: '{Title} - {Author}.{ext}' } })
    expect(screen.getByTestId('naming-preview-book').textContent).toBe('Sample Book - Jane Doe.epub')
  })

  it('inserts a token at the caret when a picker chip is clicked', () => {
    render(<Harness initial="A.{ext}" />)
    const input = screen.getByPlaceholderText('placeholder') as HTMLInputElement
    // Place caret after "A"
    input.setSelectionRange(1, 1)
    fireEvent.click(screen.getByRole('button', { name: '{Title}' }))
    expect(input.value).toBe('A{Title}.{ext}')
  })

  it('warns on an unknown token and disables save', () => {
    render(<Harness initial="{Bogus}/{Title}" />)
    expect(screen.getByText(/errorUnknownTokens.*\{Bogus\}/)).toBeTruthy()
    const save = screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
  })

  it('blocks save on an empty template and announces the blocking hint', () => {
    render(<Harness initial="" />)
    // The empty-template message is announced (role="alert") while Save stays
    // blocked, so screen-reader users hear why the button is disabled.
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('settings.general.naming.hintEmpty')
    const save = screen.getByRole('button', { name: 'common.save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
  })

  it('greys out {ext} for the audiobook template and ignores clicks on it', () => {
    render(<Harness kind="audiobook" initial="{Title}" />)
    const extChip = screen.getByRole('button', { name: '{ext}' }) as HTMLButtonElement
    expect(extChip.disabled).toBe(true)
    fireEvent.click(extChip)
    expect((screen.getByPlaceholderText('placeholder') as HTMLInputElement).value).toBe('{Title}')
  })
})
