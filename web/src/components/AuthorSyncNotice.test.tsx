import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import AuthorSyncNotice from './AuthorSyncNotice'
import type { AuthorSyncSummary } from '../api/client'

// AuthorSyncNotice calls t(key, { count, defaultValue, ...vars }), not the
// plain-string-fallback shape (t(key, 'fallback')) most other components'
// mocks assume — this substitutes {{var}} placeholders in defaultValue the
// same way real i18next interpolation would, so assertions can match on the
// rendered text rather than the raw template.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    // t() is called here in two shapes: t(key, { defaultValue, ...vars }) and
    // t(key, 'default string', { ...vars }) — handle both.
    t: (key: string, arg2?: string | Record<string, unknown>, arg3?: Record<string, unknown>) => {
      let text: string
      let vars: Record<string, unknown>
      if (typeof arg2 === 'string') {
        text = arg2
        vars = arg3 ?? {}
      } else if (arg2) {
        const { defaultValue, ...rest } = arg2
        text = typeof defaultValue === 'string' ? defaultValue : key
        vars = rest
      } else {
        return key
      }
      for (const [k, v] of Object.entries(vars)) {
        text = text.replaceAll(`{{${k}}}`, String(v))
      }
      return text
    },
  }),
}))

function summary(overrides: Partial<AuthorSyncSummary> = {}): AuthorSyncSummary {
  return {
    completedAt: '2026-08-15T00:00:00Z',
    total: 10,
    added: 10,
    skippedLanguage: 0,
    skippedJunk: 0,
    skippedMediaType: 0,
    ...overrides,
  }
}

function renderNotice(sync?: AuthorSyncSummary) {
  return render(
    <MemoryRouter>
      <AuthorSyncNotice sync={sync} />
    </MemoryRouter>,
  )
}

describe('AuthorSyncNotice', () => {
  it('renders nothing when nothing was skipped', () => {
    const { container } = renderNotice(summary())
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing when sync is undefined', () => {
    const { container } = renderNotice(undefined)
    expect(container).toBeEmptyDOMElement()
  })

  // The bug this component exists to fix (#1889-shaped, vavallee PR review):
  // a sync that dropped books only through one of the five newer
  // metadata-profile filters used to render nothing at all, since the sum
  // gating the notice only counted the original four reasons. Each of these
  // is its own regression test so a future filter can't reintroduce the gap
  // silently for just one reason.
  it('renders when only skippedPartBooks is nonzero', () => {
    renderNotice(summary({ skippedPartBooks: 3 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/3 skipped as box sets, omnibuses/)).toBeInTheDocument()
  })

  it('renders when only skippedMissingDate is nonzero', () => {
    renderNotice(summary({ skippedMissingDate: 2 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/2 skipped for having no release date/)).toBeInTheDocument()
  })

  it('renders when only skippedMinPopularity is nonzero', () => {
    renderNotice(summary({ skippedMinPopularity: 4 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/4 skipped for falling below the minimum popularity floor/)).toBeInTheDocument()
  })

  it('renders when only skippedMinPages is nonzero', () => {
    renderNotice(summary({ skippedMinPages: 1 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/1 skipped for falling below the minimum page count/)).toBeInTheDocument()
  })

  it('renders when only skippedMissingIsbn is nonzero', () => {
    renderNotice(summary({ skippedMissingIsbn: 5 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/5 skipped for having no ISBN on any edition/)).toBeInTheDocument()
  })

  it('merges samples from every filter into one combined examples line', () => {
    renderNotice(
      summary({
        skippedPartBooks: 1,
        skippedPartBooksSample: [{ title: 'Boxed Set: Foo', language: '' }],
        skippedMissingIsbn: 1,
        skippedMissingIsbnSample: [{ title: 'No ISBN Book', language: '' }],
      }),
    )
    const examples = screen.getByText(/For example:/)
    expect(examples.textContent).toContain('Boxed Set: Foo')
    expect(examples.textContent).toContain('No ISBN Book')
  })

  it('still renders on the original four reasons (regression)', () => {
    renderNotice(summary({ skippedJunk: 2 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/2 skipped as untitled provider records/)).toBeInTheDocument()
  })

  // vavallee, PR review: merging six per-filter samples (5 each) with no cap
  // of its own could print up to 30 titles on one line, and a naive
  // .slice(0, 5) on the concatenation would let language — listed first —
  // crowd out every other filter's example. Both need covering: the total
  // must stay bounded, and a filter later in the list must still get a slot.
  it('caps the merged examples line at 5 and round-robins across filters', () => {
    renderNotice(
      summary({
        skippedLanguage: 3,
        skippedLanguageSample: [
          { title: 'Lang A', language: 'fre' },
          { title: 'Lang B', language: 'fre' },
          { title: 'Lang C', language: 'fre' },
        ],
        skippedPartBooks: 1,
        skippedPartBooksSample: [{ title: 'Part Book A', language: '' }],
        skippedMissingDate: 1,
        skippedMissingDateSample: [{ title: 'Undated A', language: '' }],
        skippedMinPopularity: 1,
        skippedMinPopularitySample: [{ title: 'Unpopular A', language: '' }],
        skippedMinPages: 1,
        skippedMinPagesSample: [{ title: 'Short A', language: '' }],
        skippedMissingIsbn: 1,
        skippedMissingIsbnSample: [{ title: 'No ISBN A', language: '' }],
      }),
    )
    const examples = screen.getByText(/For example:/)
    const titles = examples.textContent!.split('For example:')[1].split(', ')
    expect(titles).toHaveLength(5)
    // One from each non-language filter must appear despite language having
    // 3 candidates and being listed first — round-robin, not a head slice.
    for (const want of ['Part Book A', 'Undated A', 'Unpopular A', 'Short A']) {
      expect(examples.textContent).toContain(want)
    }
    // Only the round's worth of language examples fit before the cap — with
    // 5 non-language sources also contributing one each in round 0, only one
    // language slot remains in round 0 and the cap is hit before round 1.
    expect(examples.textContent).not.toContain('Lang C')
  })
})
