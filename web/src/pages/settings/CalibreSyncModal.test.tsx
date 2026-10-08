import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { CalibreSyncModal } from './CalibreTab'
import type { CalibreSyncProgress } from '../../api/client'
import i18n from '../../i18n'

function progress(over: Partial<CalibreSyncProgress> = {}): CalibreSyncProgress {
  return {
    running: false,
    finishedAt: '2026-09-22T10:00:00Z',
    message: 'done',
    stats: { total: 1, processed: 1, pushed: 1, alreadyInCalibre: 0, failed: 0, skipped: 0 },
    errors: [],
    skips: [],
    ...over,
  }
}

describe('CalibreSyncModal', () => {
  // Discussion #1592: a skipped book appeared in no counter, no error row and
  // no log line, so "pushed 0, already in Calibre 0, failed 0" was
  // indistinguishable from "your library is already in Calibre".
  it('shows a skipped tile with the count', () => {
    render(
      <CalibreSyncModal
        progress={progress({
          stats: { total: 1, processed: 1, pushed: 1, alreadyInCalibre: 0, failed: 0, skipped: 3 },
          skips: [
            { bookId: 2, title: 'Unmonitored', reason: 'not monitored' },
            { bookId: 3, title: 'Audio Only', reason: 'audiobook only with no ebook' },
            { bookId: 4, title: 'Ghost', reason: 'no file on disk' },
          ],
        })}
        error={null}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByText('Skipped')).toBeInTheDocument()
    expect(screen.getByTestId('calibre-sync-skipped')).toHaveTextContent('3')
    expect(screen.getByText('Audio Only')).toBeInTheDocument()
    expect(screen.getByText('audiobook only with no ebook')).toBeInTheDocument()
  })

  it('names the reason instead of a generic message when nothing was pushed', () => {
    render(
      <CalibreSyncModal
        progress={progress({
          message: 'no books to push: 2 not monitored; 1 audiobook only with no ebook',
          stats: { total: 0, processed: 0, pushed: 0, alreadyInCalibre: 0, failed: 0, skipped: 3 },
          skips: [{ bookId: 2, title: 'Unmonitored', reason: 'not monitored' }],
        })}
        error={null}
        onClose={vi.fn()}
      />
    )
    expect(
      screen.getByText('no books to push: 2 not monitored; 1 audiobook only with no ebook')
    ).toBeInTheDocument()
  })

  it('leaves the skip table out when nothing was skipped', () => {
    render(<CalibreSyncModal progress={progress()} error={null} onClose={vi.fn()} />)
    expect(screen.getByTestId('calibre-sync-skipped')).toHaveTextContent('0')
    expect(screen.queryByTestId('calibre-sync-skip-table')).toBeNull()
  })
})

// "Push all to Calibre" and the modal's labels were hardcoded English.
describe('CalibreSyncModal translations', () => {
  afterEach(async () => {
    await act(async () => { await i18n.changeLanguage('en') })
  })

  it('renders the English strings from en.json', () => {
    render(<CalibreSyncModal progress={progress({ stats: { total: 4, processed: 4, pushed: 1, alreadyInCalibre: 2, failed: 0, skipped: 1 } })} error={null} onClose={vi.fn()} />)
    expect(screen.getByRole('heading', { name: 'Push all to Calibre' })).toBeInTheDocument()
    expect(screen.getByText('Done. Pushed 1, already in Calibre 2, failed 0, skipped 1.')).toBeInTheDocument()
    // The header X and the footer button both read Close once it is done.
    expect(screen.getAllByRole('button', { name: 'Close' })).toHaveLength(2)
  })

  it('follows the active locale', async () => {
    i18n.addResourceBundle('fr', 'translation', {
      settings: { calibre: { pushAll: { label: 'Tout envoyer vers Calibre', pushed: 'Envoyés', close: 'Fermer' } } },
    }, true, true)
    await act(async () => { await i18n.changeLanguage('fr') })
    render(<CalibreSyncModal progress={progress()} error={null} onClose={vi.fn()} />)
    expect(screen.getByRole('heading', { name: 'Tout envoyer vers Calibre' })).toBeInTheDocument()
    expect(screen.getByText('Envoyés')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Fermer' })).toHaveLength(2)
  })
})
