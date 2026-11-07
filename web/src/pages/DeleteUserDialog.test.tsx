import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import DeleteUserDialog from './DeleteUserDialog'
import type { ManagedUser, UserOwnedRows } from '../api/client'

// Resolves the real en.json strings with i18next's one/other plural split, so
// the assertions read the sentence an admin sees, singular and plural.
vi.mock('react-i18next', async () => {
  const en = (await import('../i18n/locales/en.json')).default as { users: Record<string, string> }
  return {
    useTranslation: () => ({
      t: (key: string, opts?: Record<string, unknown>) => {
        const name = key.replace(/^users\./, '')
        const count = opts?.count
        const s = (typeof count === 'number' ? en.users[`${name}_${count === 1 ? 'one' : 'other'}`] : undefined) ?? en.users[name] ?? key
        return s.replace(/\{\{(\w+)\}\}/g, (_m, v: string) => String(opts?.[v] ?? ''))
      },
    }),
  }
})

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

  it('says how many books from other accounts a purge keeps', () => {
    renderDialog({ ...base, othersBooks: 2, othersBookFiles: 3 })
    expect(screen.getByText(/Keeps 2 books that belong/)).toBeInTheDocument()
  })

  it('uses the singular for a count of one', () => {
    renderDialog({ ...base, othersBooks: 1, othersBookFiles: 1 })
    expect(screen.getByText(/Keeps 1 book that belong/)).toBeInTheDocument()
  })

  it('stays quiet when no other account has books under these authors', () => {
    renderDialog(base)
    expect(screen.queryByText(/Keeps \d+ books? that belong/)).toBeNull()
  })
})
