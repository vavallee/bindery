import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { CalibreRollbackModal, CalibreSyncModal } from './CalibreTab'
import { api } from '../../api/client'
import type { CalibreImportRun, CalibreSyncProgress } from '../../api/client'
import '../../i18n'

vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return { ...actual, api: { ...actual.api, calibreRunRollbackPreview: vi.fn() } }
})

const run: CalibreImportRun = {
  id: 7,
  sourceId: 'lib',
  libraryPath: '/calibre',
  status: 'completed',
  dryRun: false,
  startedAt: '2026-09-22T10:00:00Z',
}

const progress: CalibreSyncProgress = {
  running: true,
  message: 'working',
  stats: { total: 10, processed: 2, pushed: 2, alreadyInCalibre: 0, failed: 0, skipped: 0 },
  errors: [],
  skips: [],
}

// Both modals grew past the screen on a phone (a long action list, a long
// error list) with no cap, so the footer buttons ended up below the fold.
// They now follow the #3053 pattern: a capped flex column whose body scrolls
// between a fixed header and a fixed footer.
function expectScrollingBody(dialog: HTMLElement) {
  // The panel itself is the dialog; the backdrop around it is not (#3052).
  const panel = dialog
  expect(panel.parentElement!.className).toContain('modal-overlay')
  expect(panel.className).toContain('modal-max-h')
  expect(panel.className).toContain('flex flex-col')
  const [header, body, footer] = Array.from(panel.children) as HTMLElement[]
  expect(header.className).toContain('shrink-0')
  expect(body.className).toContain('overflow-y-auto')
  expect(body.className).toContain('min-h-0')
  expect(footer.className).toContain('shrink-0')
}

describe('Calibre modals on a phone', () => {
  it('rollback modal scrolls its body and names its close button', async () => {
    vi.mocked(api.calibreRunRollbackPreview).mockReturnValue(new Promise(() => {}))
    render(<CalibreRollbackModal run={run} onClose={vi.fn()} onApplied={vi.fn()} />)
    const dialog = screen.getByRole('dialog')
    expectScrollingBody(dialog)
    // The header X used to be announced as "✕" or as nothing at all.
    expect(screen.getByRole('button', { name: 'Close' })).toHaveTextContent('✕')
    await waitFor(() => expect(api.calibreRunRollbackPreview).toHaveBeenCalled())
  })

  it('push all modal scrolls its body and names its close button', () => {
    render(<CalibreSyncModal progress={progress} error={null} onClose={vi.fn()} />)
    expectScrollingBody(screen.getByRole('dialog'))
    // While running the footer reads "Close and keep going", so the only
    // button named plain "Close" is the header X.
    expect(screen.getByRole('button', { name: 'Close' })).toHaveTextContent('✕')
  })
})
