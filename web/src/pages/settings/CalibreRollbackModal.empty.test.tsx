import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { CalibreRollbackModal } from './CalibreTab'
import { api } from '../../api/client'
import type { CalibreImportRun, CalibreRollbackResult } from '../../api/client'
import '../../i18n'

vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return { ...actual, api: { ...actual.api, calibreRunRollbackPreview: vi.fn() } }
})

function previewFor(dryRun: boolean, actions: CalibreRollbackResult['actions']): CalibreRollbackResult {
  return {
    runId: 7,
    preview: true,
    applied: false,
    dryRun,
    status: 'completed',
    stats: { actionsPlanned: 0, entitiesDeleted: 0, provenanceUnlinked: 0, skipped: 0, failed: 0, filesAffected: 0 },
    actions,
    finishedAt: '2026-10-07T10:00:00Z',
  }
}

function runFor(dryRun: boolean): CalibreImportRun {
  return { id: 7, sourceId: 'lib', libraryPath: '/calibre', status: 'completed', dryRun, startedAt: '2026-10-07T09:00:00Z' }
}

// A run with nothing to undo came back as actions: null, the modal read
// actions.length, and the error boundary replaced the settings page.
describe('CalibreRollbackModal with nothing to roll back', () => {
  for (const [label, actions] of [['null', null], ['an empty list', []]] as const) {
    it(`shows the empty state for ${label} and disables Apply`, async () => {
      vi.mocked(api.calibreRunRollbackPreview).mockResolvedValue(previewFor(false, actions === null ? null : [...actions]))
      render(<CalibreRollbackModal run={runFor(false)} onClose={vi.fn()} onApplied={vi.fn()} />)
      expect(await screen.findByText('Nothing to roll back. This import left nothing Bindery can undo.')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Apply rollback' })).toBeDisabled()
    })
  }

  it('says why for a dry run', async () => {
    vi.mocked(api.calibreRunRollbackPreview).mockResolvedValue(previewFor(true, null))
    render(<CalibreRollbackModal run={runFor(true)} onClose={vi.fn()} onApplied={vi.fn()} />)
    expect(await screen.findByText('Nothing to roll back. This was a dry run, so it never changed your library.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply rollback' })).toBeDisabled()
  })
})
