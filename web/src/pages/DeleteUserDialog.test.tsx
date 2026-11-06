import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import DeleteUserDialog from './DeleteUserDialog'
import type { ManagedUser, UserOwnedRows } from '../api/client'

// Keys come back verbatim with the numeric vars appended, so the assertion
// proves which counts reach the line without depending on English copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      const vars = opts && typeof opts === 'object' ? (opts as Record<string, unknown>) : {}
      const suffix = Object.values(vars).filter(v => typeof v === 'string' || typeof v === 'number').join(',')
      return suffix ? `${key}:${suffix}` : key
    },
  }),
}))

const user: ManagedUser = { id: 2, username: 'reader', role: 'user', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false }

const base: UserOwnedRows = {
  authors: 1,
  books: 1,
  qualityProfiles: 0,
  metadataProfiles: 0,
  downloads: 0,
  rootFolders: 0,
  importLists: 0,
  blocklist: 0,
  othersBooks: 0,
  othersBookFiles: 0,
}

function renderDialog(counts: UserOwnedRows) {
  return render(
    <DeleteUserDialog user={user} counts={counts} users={[user]} busy={false} onCancel={() => {}} onConfirm={() => {}} />,
  )
}

describe('DeleteUserDialog other accounts books', () => {
  afterEach(cleanup)

  it('warns how many books and files from other accounts a purge removes', () => {
    renderDialog({ ...base, othersBooks: 3, othersBookFiles: 7 })
    expect(screen.getByText('users.deletePurgeOthers:3,7')).toBeInTheDocument()
  })

  it('stays quiet when no other account has books under these authors', () => {
    renderDialog(base)
    expect(screen.queryByText(/users\.deletePurgeOthers/)).toBeNull()
  })
})
