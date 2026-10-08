import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ReactElement } from 'react'
import './i18n'
import type { Author, Book, CalibreImportRun, ManagedUser, Series, UserOwnedRows } from './api/client'
import AddSeriesBookModal from './components/AddSeriesBookModal'
import AddToLibraryModal from './components/AddToLibraryModal'
import AuthorMetadataLinkModal from './components/AuthorMetadataLinkModal'
import CatalogueReconciliationModal from './components/CatalogueReconciliationModal'
import ConfirmDialog from './components/ConfirmDialog'
import DuplicateCandidatesModal from './components/DuplicateCandidatesModal'
import EditAuthorModal from './components/EditAuthorModal'
import EditBookModal from './components/EditBookModal'
import FixMatchModal from './components/FixMatchModal'
import HardcoverSeriesLinkModal from './components/HardcoverSeriesLinkModal'
import MergeAuthorsModal from './components/MergeAuthorsModal'
import MergeSeriesModal from './components/MergeSeriesModal'
import RebindModal from './components/RebindModal'
import RenameFilesModal from './components/RenameFilesModal'
import SeriesNameModal from './components/SeriesNameModal'
import DeleteUserDialog from './pages/DeleteUserDialog'
import { CalibreRollbackModal, CalibreSyncModal } from './pages/settings/CalibreTab'

// #3052: every modal must be a labelled, modal dialog, and must get its
// behaviour (focus trap, Escape, back button) from useModal. A new modal that
// skips either fails here: the source scan at the bottom finds every overlay
// in the app and requires each one to be accounted for.

// Every request stays pending: the shell of each modal is what is checked.
vi.mock('./api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('./api/client')>()
  const pending = new Proxy({}, { get: () => () => new Promise(() => {}) })
  return { ...actual, api: pending }
})
vi.mock('./api/reorganize', () => ({
  reorganizeApi: new Proxy({}, { get: () => () => new Promise(() => {}) }),
}))

const noop = () => {}
const book = { id: 1, title: 'A Book', authorId: 2, foreignBookId: 'OL1W', genres: [], lockedFields: [] } as unknown as Book
const author = { id: 2, authorName: 'An Author', foreignAuthorId: 'OL2A' } as unknown as Author
const series = { id: 3, title: 'A Series', books: [] } as unknown as Series
const user = { id: 4, username: 'someone' } as unknown as ManagedUser
const counts = { authors: 1, books: 0, downloads: 0, qualityProfiles: 0, metadataProfiles: 0, rootFolders: 0, importLists: 0 } as unknown as UserOwnedRows
const run = { id: 7, sourceId: 'lib', libraryPath: '/calibre', status: 'completed', dryRun: false, startedAt: '2026-09-22T10:00:00Z' } as CalibreImportRun

// Component file (as the source scan names it) and how to render it.
const MODALS: Array<[string, (onClose: () => void) => ReactElement]> = [
  ['components/AddSeriesBookModal.tsx', onClose => <AddSeriesBookModal series={series} onClose={onClose} onLinked={noop} />],
  ['components/AddToLibraryModal.tsx', onClose => <AddToLibraryModal onClose={onClose} onAdded={noop} />],
  ['components/AuthorMetadataLinkModal.tsx', onClose => <AuthorMetadataLinkModal author={author} onClose={onClose} onLinked={noop} />],
  ['components/CatalogueReconciliationModal.tsx', onClose => <CatalogueReconciliationModal authorId={2} authorName="An Author" onClose={onClose} />],
  ['components/ConfirmDialog.tsx', onClose => <ConfirmDialog title="Sure?" body="Really" confirmLabel="Yes" onConfirm={noop} onClose={onClose} />],
  ['components/DuplicateCandidatesModal.tsx', onClose => <DuplicateCandidatesModal authorId={2} authorName="An Author" onClose={onClose} />],
  ['components/EditAuthorModal.tsx', onClose => <EditAuthorModal author={author} onClose={onClose} onSaved={noop} />],
  ['components/EditBookModal.tsx', onClose => <EditBookModal book={book} onClose={onClose} onSaved={noop} />],
  ['components/FixMatchModal.tsx', onClose => <FixMatchModal sourceBookId={1} path="/b/a.epub" format="ebook" onClose={onClose} onReassigned={noop} />],
  ['components/HardcoverSeriesLinkModal.tsx', onClose => <HardcoverSeriesLinkModal series={series} initialResults={[]} onClose={onClose} onLinked={noop} />],
  ['components/MergeAuthorsModal.tsx', onClose => <MergeAuthorsModal authors={[author]} onClose={onClose} onMerged={noop} />],
  ['components/MergeSeriesModal.tsx', onClose => <MergeSeriesModal target={series} series={[series]} onClose={onClose} onMerged={noop} />],
  ['components/RebindModal.tsx', onClose => <RebindModal book={book} onClose={onClose} onSuccess={noop} />],
  ['components/RenameFilesModal.tsx', onClose => <RenameFilesModal scope="book" id={1} label="A Book" onClose={onClose} />],
  ['components/SeriesNameModal.tsx', onClose => <SeriesNameModal title="Rename series" submitLabel="Save" onClose={onClose} onSubmit={noop} />],
  ['pages/DeleteUserDialog.tsx', onClose => <DeleteUserDialog user={user} counts={counts} users={[user]} busy={false} onCancel={onClose} onConfirm={noop} />],
  ['pages/settings/CalibreTab.tsx#rollback', onClose => <CalibreRollbackModal run={run} onClose={onClose} onApplied={noop} />],
  ['pages/settings/CalibreTab.tsx#sync', onClose => <CalibreSyncModal progress={null} error={null} onClose={onClose} />],
]

// Modals written inline in a page, which cannot be rendered on their own.
// Their page tests open them; here the source scan checks they use ModalPanel.
const INLINE = ['pages/AuthorsPage.tsx', 'pages/QueuePage.tsx', 'pages/settings/CalibreDeliveryPanel.tsx']

afterEach(cleanup)

describe('every modal is a labelled modal dialog', () => {
  it.each(MODALS)('%s', (_name, element) => {
    render(element(noop))
    const dialogs = screen.getAllByRole('dialog')
    expect(dialogs).toHaveLength(1)
    const dialog = dialogs[0]
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    const labelledBy = dialog.getAttribute('aria-labelledby')
    expect(labelledBy).toBeTruthy()
    // Several ids are allowed; the first names the heading.
    const label = document.getElementById(labelledBy!.split(/\s+/)[0])
    expect(label).not.toBeNull()
    expect(dialog.contains(label)).toBe(true)
    expect(label!.textContent!.trim()).not.toBe('')
    // The backdrop, not the panel, carries modal-overlay; a native dialog has
    // no separate backdrop element.
    if (dialog.tagName !== 'DIALOG') {
      expect(dialog.closest('.modal-overlay')).not.toBeNull()
      expect(dialog.closest('.modal-overlay')).not.toBe(dialog)
    }
    expect(dialog.className).toMatch(/\bmodal-max-h\b/)
  })
})

describe('every modal closes on Escape', () => {
  // RebindModal is a native <dialog>: the browser turns Escape into a cancel
  // event, which jsdom does not do.
  it.each(MODALS.filter(([name]) => name !== 'components/RebindModal.tsx'))('%s', (_name, element) => {
    const onClose = vi.fn()
    render(element(onClose))
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

const sources = import.meta.glob(['./**/*.tsx', '!./**/*.test.tsx'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('source scan', () => {
  const overlayFiles = Object.entries(sources)
    .filter(([, src]) => /fixed inset-0|<dialog\b|role="dialog"/.test(src))
    .map(([path]) => path.replace(/^\.\//, ''))

  it('finds the modals', () => {
    expect(overlayFiles.length).toBeGreaterThanOrEqual(MODALS.length)
  })

  it('accounts for every overlay in the app', () => {
    const known = new Set([...MODALS.map(([name]) => name.replace(/#.*$/, '')), ...INLINE])
    expect(overlayFiles.filter(f => !known.has(f))).toEqual([])
  })

  it.each(Object.entries(sources).filter(([, src]) => /fixed inset-0|<dialog\b/.test(src)))('%s uses the shared modal behaviour', (_path, src) => {
    expect(src).toMatch(/useModal\(|<ModalPanel\b/)
    // Every backdrop takes the scroll lock and overscroll containment.
    for (const m of src.matchAll(/className="([^"]*\bfixed inset-0\b[^"]*)"/g)) {
      expect(m[1]).toMatch(/\bmodal-overlay\b/)
    }
    expect(src).not.toMatch(/max-h-\[90vh\]/)
  })

  it('EditBookModal has no literal NUL character', () => {
    const src = sources['./components/EditBookModal.tsx']
    expect(src).toBeTruthy()
    expect(src.includes('\u0000')).toBe(false)
  })
})
