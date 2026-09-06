import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
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
    matched: 0,
    skippedLanguage: 0,
    skippedJunk: 0,
    skippedMediaType: 0,
    ...overrides,
  }
}

// The per-filter breakdown is behind the info alert's disclosure now: the
// notice leads with the count and hands over the reasons on request. Every
// assertion about a reason has to open it first, which is also the check that
// it is still reachable.
function openDetails() {
  fireEvent.click(screen.getByRole('button', { name: 'Show details' }))
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
    openDetails()
    expect(screen.getByText(/3 skipped as box sets, omnibuses/)).toBeInTheDocument()
  })

  it('renders when only skippedMissingDate is nonzero', () => {
    renderNotice(summary({ skippedMissingDate: 2 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    openDetails()
    expect(screen.getByText(/2 skipped for having no release date/)).toBeInTheDocument()
  })

  it('renders when only skippedMinPages is nonzero', () => {
    renderNotice(summary({ skippedMinPages: 1 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    openDetails()
    expect(screen.getByText(/1 skipped for falling below the minimum page count/)).toBeInTheDocument()
  })

  it('renders when only skippedMissingIsbn is nonzero', () => {
    renderNotice(summary({ skippedMissingIsbn: 5 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    openDetails()
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
    openDetails()
    const examples = screen.getByText(/For example:/)
    expect(examples.textContent).toContain('Boxed Set: Foo')
    expect(examples.textContent).toContain('No ISBN Book')
  })

  it('still renders on the original four reasons (regression)', () => {
    renderNotice(summary({ skippedJunk: 2 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    openDetails()
    expect(screen.getByText(/2 skipped as untitled provider records/)).toBeInTheDocument()
  })

  // vavallee, PR review: merging six per-filter samples (5 each) with no cap
  // of its own could print up to 30 titles on one line, and a naive
  // .slice(0, 5) on the concatenation would let language — listed first —
  // crowd out every other filter's example. Both need covering: the total
  // must stay bounded, and a filter later in the list must still get a slot.
  // The notice describes a filter doing exactly what it was configured to do,
  // so it must not wear the same amber a broken indexer wears. Asserting on
  // the absence of amber is the only way this stays true, since a future
  // refactor could reintroduce it without breaking a text assertion.
  it('renders in the neutral info tier, not amber', () => {
    renderNotice(summary({ skippedLanguage: 4 }))
    const el = screen.getByTestId('author-sync-notice')
    expect(el.className).not.toContain('amber')
    expect(el.className).toContain('bg-slate-100')
    expect(el.className).toContain('dark:bg-zinc-900')
  })

  it('leads with the count and keeps the settings link on screen unopened', () => {
    renderNotice(summary({ skippedLanguage: 4 }))
    expect(screen.getByText(/Last refresh skipped 4 of this author/)).toBeInTheDocument()
    expect(screen.getByText('Metadata profile settings')).toBeInTheDocument()
    expect(screen.queryByText(/skipped by the language filter/)).not.toBeInTheDocument()
  })

  it('can be dismissed', () => {
    renderNotice(summary({ skippedLanguage: 4 }))
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByTestId('author-sync-notice')).not.toBeInTheDocument()
  })

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
        skippedMinPages: 1,
        skippedMinPagesSample: [{ title: 'Short A', language: '' }],
        skippedMissingIsbn: 1,
        skippedMissingIsbnSample: [{ title: 'No ISBN A', language: '' }],
      }),
    )
    openDetails()
    const examples = screen.getByText(/For example:/)
    const titles = examples.textContent!.split('For example:')[1].split(', ')
    expect(titles).toHaveLength(5)
    // One from each non-language filter must appear despite language having
    // 3 candidates and being listed first — round-robin, not a head slice.
    for (const want of ['Part Book A', 'Undated A', 'Short A', 'No ISBN A']) {
      expect(examples.textContent).toContain(want)
    }
    // Only the round's worth of language examples fit before the cap — with
    // 4 non-language sources also contributing one each in round 0, only one
    // language slot remains in round 0 and the cap is hit before round 1.
    expect(examples.textContent).not.toContain('Lang C')
  })

  // #2449. The notice reported "skipped 2 of 106" and said nothing about the
  // other 103, so a reader doing the subtraction concluded the sync had lost
  // books. It had not: they were already on the shelf, and the notice had no
  // sentence for that.
  it('says how many works were added and how many were already in the library', () => {
    renderNotice(summary({ total: 106, added: 1, matched: 103, skippedLanguage: 2 }))
    expect(screen.getByText(/1 added, 103 already in your library/)).toBeInTheDocument()
  })

  it('accounts for the whole total once the skips are added in', () => {
    renderNotice(summary({ total: 106, added: 1, matched: 103, skippedLanguage: 2 }))
    // heading + accounting line together have to name all 106.
    expect(screen.getByText(/skipped 2 of this author’s 106 works/)).toBeInTheDocument()
    expect(screen.getByText(/1 added, 103 already in your library/)).toBeInTheDocument()
  })

  // A failed write is the one outcome in this component that is a fault
  // rather than a setting, so it shows on its own with no filter involved.
  it('reports failed writes even when no filter dropped anything', () => {
    renderNotice(summary({ total: 10, added: 9, matched: 0, failed: 1 }))
    expect(screen.getByTestId('author-sync-notice')).toBeInTheDocument()
    expect(screen.getByText(/could not save 1 of this author’s 10 works/)).toBeInTheDocument()
    expect(screen.getByText(/1 could not be saved/)).toBeInTheDocument()
  })

  // The failure stays on screen while the filter reasons fold into the
  // disclosure. An error tier whose reason is one click away is the weaker
  // half of both ideas: the colour says something is wrong and the page does
  // not say what.
  it('shows a failed write without opening the disclosure, and folds the filters away', () => {
    renderNotice(summary({ total: 10, added: 7, matched: 0, failed: 1, skippedLanguage: 2 }))
    expect(screen.getByText(/1 could not be saved/)).toBeInTheDocument()
    expect(screen.queryByText(/language filter/)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /Show details/ }))
    expect(screen.getByText(/language filter/)).toBeInTheDocument()
  })

  // A lost write is a fault, which is what the error tier is for. A run that
  // only tripped filters is not.
  it('reads as an error only when a write failed', () => {
    const { unmount } = renderNotice(summary({ total: 10, added: 7, matched: 0, failed: 1, skippedLanguage: 2 }))
    expect(screen.getByTestId('author-sync-notice')).toHaveAttribute('role', 'alert')
    unmount()

    renderNotice(summary({ total: 10, added: 8, matched: 0, skippedLanguage: 2 }))
    expect(screen.getByTestId('author-sync-notice')).toHaveAttribute('role', 'status')
  })

  // skippedExcluded exists so the server's total reconciles. Rendering it
  // would ask the user to justify a decision they already made.
  it('does not mention works dropped for matching an excluded book', () => {
    const { container } = renderNotice(summary({ total: 10, added: 8, matched: 1, skippedExcluded: 1 }))
    expect(container).toBeEmptyDOMElement()
  })
})
