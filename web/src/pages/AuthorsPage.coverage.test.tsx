import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import AuthorsPage from './AuthorsPage'
import { api } from '../api/client'
import type { Author, Book } from '../api/client'
import { acceptConfirm, cancelConfirm, confirmDialog } from '../test-utils'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listAuthors: vi.fn(),
      listAllAuthors: vi.fn(),
      listBooks: vi.fn(),
      deleteAuthor: vi.fn(),
      updateAuthor: vi.fn(),
      refreshAuthor: vi.fn(),
      refreshAllAuthors: vi.fn(),
      refreshAllAuthorsStatus: vi.fn(),
      bulkActionAuthors: vi.fn(),
      bulkSetAuthorMonitorMode: vi.fn(),
      createSeries: vi.fn(),
      mergeAuthors: vi.fn(),
    },
  }
})

// Keys come back verbatim so assertions don't depend on English copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}))

// The checklist and the add dialog have their own suites and their own API
// traffic; this file is about the page around them.
vi.mock('../components/SetupChecklist', () => ({ default: () => null }))
vi.mock('../components/AddToLibraryModal', () => ({
  default: ({ mode, onClose, onAdded }: { mode: string; onClose: () => void; onAdded: () => void }) => (
    <div role="dialog" aria-label={`add-${mode}`}>
      <button onClick={onAdded}>stub-added</button>
      <button onClick={onClose}>stub-close</button>
    </div>
  ),
}))

function author(id: number, authorName: string, extra: Partial<Author> = {}): Author {
  return {
    id,
    foreignAuthorId: `OL${id}A`,
    authorName,
    sortName: authorName,
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
    ...extra,
  }
}

const weir = author(7, 'Andy Weir', { description: 'Wrote The Martian', imageUrl: 'http://img/weir.jpg', averageRating: 4.12, statistics: { bookCount: 3, availableBookCount: 1, wantedBookCount: 2 } })
const leguin = author(8, 'Ursula Le Guin', { monitored: false })

function seed(items: Author[], total = items.length) {
  vi.mocked(api.listAuthors).mockResolvedValue({ items, total, limit: 50, offset: 0 })
}

function renderPage() {
  return render(<MemoryRouter><AuthorsPage /></MemoryRouter>)
}

async function renderWithAuthors(items: Author[] = [weir, leguin]) {
  seed(items)
  renderPage()
  await screen.findByText(items[0].authorName)
}

function card(name: string): HTMLElement {
  let el: HTMLElement | null = screen.getByRole('heading', { name })
  while (el && !within(el).queryByRole('switch')) el = el.parentElement
  return el as HTMLElement
}

async function selectBoth() {
  fireEvent.click(screen.getByTitle('Select Andy Weir'))
  fireEvent.click(screen.getByTitle('Select Ursula Le Guin'))
}

function bookWithFile(id: number, filePath: string): Book {
  return { id, filePath } as Book
}

describe('AuthorsPage coverage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    vi.mocked(api.refreshAllAuthorsStatus).mockResolvedValue(null)
    vi.mocked(api.listAllAuthors).mockResolvedValue([weir, leguin])
    vi.mocked(api.listBooks).mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    vi.mocked(api.deleteAuthor).mockResolvedValue(undefined)
    vi.mocked(api.updateAuthor).mockResolvedValue(weir)
    vi.mocked(api.refreshAllAuthors).mockResolvedValue({ message: 'started' })
    vi.mocked(api.bulkActionAuthors).mockResolvedValue({ results: {} })
    vi.mocked(api.bulkSetAuthorMonitorMode).mockResolvedValue({ results: {} })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  describe('empty and loading states', () => {
    it('shows loading, then the empty library prompt with refresh-all and merge disabled', async () => {
      seed([])
      renderPage()
      expect(screen.getByText('common.loading')).toBeInTheDocument()
      expect(await screen.findByText('authors.empty')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'authors.refreshAll' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'authors.merge' })).toBeDisabled()
      expect(document.title).toBe('Authors · Bindery')
    })

    it('debounces the search box and says nothing matched', async () => {
      seed([])
      renderPage()
      await screen.findByText('authors.empty')
      fireEvent.change(screen.getByPlaceholderText('authors.searchPlaceholder'), { target: { value: '  zzz  ' } })
      await waitFor(() => expect(api.listAuthors).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'zzz', offset: 0 })))
      expect(await screen.findByText('authors.noMatch')).toBeInTheDocument()
      expect(screen.queryByText('authors.empty')).toBeNull()
    })

    it('restores a stored monitored filter and sends it to the server', async () => {
      localStorage.setItem('bindery.filter.authors.monitored', 'unmonitored')
      seed([])
      renderPage()
      await waitFor(() => expect(api.listAuthors).toHaveBeenCalledWith(expect.objectContaining({ monitored: false })))
      // A filter that matches nothing is "no match", not an empty library.
      expect(await screen.findByText('authors.noMatch')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /common.clearFilter/ })).toHaveTextContent('authors.filterUnmonitored')
    })

    it('sends monitored=true for the monitored-only filter and persists it', async () => {
      localStorage.setItem('bindery.filter.authors.monitored', 'monitored')
      seed([])
      renderPage()
      await waitFor(() => expect(api.listAuthors).toHaveBeenCalledWith(expect.objectContaining({ monitored: true })))
      expect(screen.getByRole('button', { name: /common.clearFilter/ })).toHaveTextContent('authors.filterMonitoredOnly')
      expect(localStorage.getItem('bindery.filter.authors.monitored')).toBe('monitored')
    })

    it('keeps rendering when the author list request fails', async () => {
      const error = vi.spyOn(console, 'error').mockImplementation(() => {})
      vi.mocked(api.listAuthors).mockRejectedValue(new Error('offline'))
      renderPage()
      expect(await screen.findByText('authors.empty')).toBeInTheDocument()
      expect(error).toHaveBeenCalled()
    })
  })

  describe('grid cards', () => {
    it('renders the cover, description, and placeholder initial', async () => {
      await renderWithAuthors()
      expect(screen.getByRole('img', { name: 'Andy Weir' })).toHaveAttribute('src', 'http://img/weir.jpg')
      expect(screen.getByText('Wrote The Martian')).toBeInTheDocument()
      expect(within(card('Ursula Le Guin')).getByText('U')).toBeInTheDocument()
      expect(within(card('Ursula Le Guin')).getByText('authors.noDescription')).toBeInTheDocument()
      expect(within(card('Ursula Le Guin')).getByText('authors.unmonitored')).toBeInTheDocument()
    })

    it('toggles monitoring and reloads', async () => {
      await renderWithAuthors()
      const before = vi.mocked(api.listAuthors).mock.calls.length
      fireEvent.click(within(card('Ursula Le Guin')).getByRole('switch'))
      await waitFor(() => expect(api.updateAuthor).toHaveBeenCalledWith(8, { monitored: true }))
      await waitFor(() => expect(vi.mocked(api.listAuthors).mock.calls.length).toBeGreaterThan(before))
    })

    it('deletes an author with no files after a plain confirmation', async () => {
      await renderWithAuthors()
      fireEvent.click(within(card('Andy Weir')).getByRole('button', { name: 'authors.moreActionsFor' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'common.delete' }))
      await waitFor(() => expect(api.listBooks).toHaveBeenCalledWith({ authorId: 7 }))
      const dialog = await screen.findByTestId('confirm-dialog')
      expect(within(dialog).getByText('authors.deleteConfirm')).toBeInTheDocument()
      expect(within(dialog).queryByRole('checkbox')).toBeNull()
      await acceptConfirm()
      await waitFor(() => expect(api.deleteAuthor).toHaveBeenCalledWith(7, false))
    })

    it('gates deleting files on disk behind an acknowledgement and deletes them', async () => {
      vi.mocked(api.listBooks).mockResolvedValue({
        items: [bookWithFile(1, '/books/a.epub'), bookWithFile(2, '')],
        total: 2,
        limit: 50,
        offset: 0,
      })
      await renderWithAuthors()
      fireEvent.click(within(card('Andy Weir')).getByRole('button', { name: 'authors.moreActionsFor' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'common.delete' }))
      const dialog = await screen.findByTestId('confirm-dialog')
      expect(within(dialog).getByText('authors.deleteWithFilesConfirm')).toBeInTheDocument()
      expect(within(dialog).getByRole('checkbox')).toBeInTheDocument()
      await acceptConfirm()
      await waitFor(() => expect(api.deleteAuthor).toHaveBeenCalledWith(7, true))
    })

    it('still offers a plain delete when the book lookup fails, and cancelling deletes nothing', async () => {
      vi.mocked(api.listBooks).mockRejectedValue(new Error('offline'))
      await renderWithAuthors()
      fireEvent.click(within(card('Andy Weir')).getByRole('button', { name: 'authors.moreActionsFor' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'common.delete' }))
      const dialog = await screen.findByTestId('confirm-dialog')
      expect(within(dialog).getByText('authors.deleteConfirm')).toBeInTheDocument()
      await cancelConfirm()
      await waitFor(() => expect(confirmDialog()).toBeNull())
      expect(api.deleteAuthor).not.toHaveBeenCalled()
    })
  })

  describe('table view', () => {
    beforeEach(() => {
      localStorage.setItem('bindery.view.authors', 'table')
    })

    it('shows ratings, book counts and thumbnails, and selects the whole page', async () => {
      await renderWithAuthors()
      expect(screen.getByText('★ 4.12')).toBeInTheDocument()
      const leguinRow = screen.getByText('Ursula Le Guin').closest('tr') as HTMLElement
      expect(within(leguinRow).getAllByText('—')).toHaveLength(2)
      expect(within(leguinRow).getByText('U')).toBeInTheDocument()

      const selectAll = screen.getByTitle('Select all on this page') as HTMLInputElement
      const weirBox = within(screen.getByText('Andy Weir').closest('tr') as HTMLElement).getByRole('checkbox')
      fireEvent.click(weirBox)
      expect(selectAll.indeterminate).toBe(true)
      fireEvent.click(selectAll)
      expect(selectAll).toBeChecked()
      expect(selectAll.indeterminate).toBe(false)
      expect(screen.getAllByRole('checkbox').every(b => (b as HTMLInputElement).checked)).toBe(true)
      fireEvent.click(selectAll)
      expect(screen.getAllByRole('checkbox').some(b => (b as HTMLInputElement).checked)).toBe(false)
    })

    it('toggles monitoring and deletes from the row', async () => {
      await renderWithAuthors()
      const row = screen.getByText('Andy Weir').closest('tr') as HTMLElement
      fireEvent.click(within(row).getByRole('switch'))
      await waitFor(() => expect(api.updateAuthor).toHaveBeenCalledWith(7, { monitored: false }))

      fireEvent.click(within(row).getByRole('button', { name: 'authors.moreActionsFor' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'common.delete' }))
      await acceptConfirm()
      await waitFor(() => expect(api.deleteAuthor).toHaveBeenCalledWith(7, false))
    })

    it('refreshes a single author from the row menu', async () => {
      vi.mocked(api.refreshAuthor).mockResolvedValue(undefined)
      await renderWithAuthors()
      const row = screen.getByText('Ursula Le Guin').closest('tr') as HTMLElement
      fireEvent.click(within(row).getByRole('button', { name: 'authors.moreActionsFor' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'common.refresh' }))
      await waitFor(() => expect(api.refreshAuthor).toHaveBeenCalledWith(8))
    })
  })

  describe('refresh all metadata', () => {
    it('confirms, starts the job, polls until it completes, then reloads', async () => {
      vi.mocked(api.refreshAllAuthorsStatus)
        .mockResolvedValueOnce(null)
        .mockResolvedValue({ status: 'completed', total: 2, done: 2, failed: 0, started_at: '' })
      await renderWithAuthors()
      const loadsBefore = vi.mocked(api.listAuthors).mock.calls.length

      fireEvent.click(screen.getByRole('button', { name: 'authors.refreshAll' }))
      await acceptConfirm()
      await waitFor(() => expect(api.refreshAllAuthors).toHaveBeenCalled())
      expect(screen.getByRole('button', { name: 'authors.refreshAllRunning' })).toBeDisabled()

      expect(await screen.findByText('authors.refreshAllDone', {}, { timeout: 10000 })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'authors.refreshAll' })).toBeEnabled()
      await waitFor(() => expect(vi.mocked(api.listAuthors).mock.calls.length).toBeGreaterThan(loadsBefore))
    })

    it('does nothing when the confirmation is cancelled', async () => {
      await renderWithAuthors()
      fireEvent.click(screen.getByRole('button', { name: 'authors.refreshAll' }))
      await cancelConfirm()
      await waitFor(() => expect(confirmDialog()).toBeNull())
      expect(api.refreshAllAuthors).not.toHaveBeenCalled()
    })

    it('alerts and re-enables the button when the job cannot start', async () => {
      const alert = vi.spyOn(window, 'alert').mockImplementation(() => {})
      vi.mocked(api.refreshAllAuthors).mockRejectedValue(new Error('already running'))
      await renderWithAuthors()
      fireEvent.click(screen.getByRole('button', { name: 'authors.refreshAll' }))
      await acceptConfirm()
      await waitFor(() => expect(alert).toHaveBeenCalledWith('already running'))
      expect(screen.getByRole('button', { name: 'authors.refreshAll' })).toBeEnabled()
    })

    it('resumes a running job found on load and shows its progress', async () => {
      vi.mocked(api.refreshAllAuthorsStatus).mockResolvedValue({ status: 'running', total: 10, done: 3, failed: 0, started_at: '' })
      await renderWithAuthors()
      expect(await screen.findByText('authors.refreshAllProgress')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'authors.refreshAllRunning' })).toBeDisabled()
    })

    it('reports a completed job that had failures', async () => {
      vi.mocked(api.refreshAllAuthorsStatus).mockResolvedValue({ status: 'completed', total: 10, done: 8, failed: 2, started_at: '' })
      await renderWithAuthors()
      expect(await screen.findByText('authors.refreshAllDoneWithFailures')).toBeInTheDocument()
    })

    it('reports a failed job', async () => {
      vi.mocked(api.refreshAllAuthorsStatus).mockResolvedValue({ status: 'failed', total: 0, done: 0, failed: 0, started_at: '', message: 'restart' })
      await renderWithAuthors()
      expect(await screen.findByText('authors.refreshAllFailed')).toBeInTheDocument()
    })

    it('ignores a status lookup that fails', async () => {
      vi.mocked(api.refreshAllAuthorsStatus).mockRejectedValue(new Error('500'))
      await renderWithAuthors()
      expect(screen.queryByText(/authors.refreshAll(Progress|Done|Failed)/)).toBeNull()
      expect(screen.getByRole('button', { name: 'authors.refreshAll' })).toBeEnabled()
    })
  })

  describe('toolbar dialogs', () => {
    it('opens merge with every author from the server', async () => {
      await renderWithAuthors()
      fireEvent.click(screen.getByRole('button', { name: 'authors.merge' }))
      await waitFor(() => expect(api.listAllAuthors).toHaveBeenCalled())
      expect(await screen.findByRole('heading', { name: 'mergeAuthorsModal.title' })).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'mergeAuthorsModal.cancel' }))
      expect(screen.queryByRole('heading', { name: 'mergeAuthorsModal.title' })).toBeNull()
    })

    it('falls back to the loaded page when the full author list fails', async () => {
      vi.mocked(api.listAllAuthors).mockRejectedValue(new Error('offline'))
      await renderWithAuthors()
      fireEvent.click(screen.getByRole('button', { name: 'authors.merge' }))
      const heading = await screen.findByRole('heading', { name: 'mergeAuthorsModal.title' })
      const panel = heading.parentElement!.parentElement as HTMLElement
      expect(within(panel).getAllByRole('option', { name: 'Andy Weir' }).length).toBeGreaterThan(0)
    })

    it('opens the add dialog in author and book mode and reloads after an add', async () => {
      await renderWithAuthors()
      fireEvent.click(screen.getByRole('button', { name: 'authors.addAuthor' }))
      expect(screen.getByRole('dialog', { name: 'add-author' })).toBeInTheDocument()
      const before = vi.mocked(api.listAuthors).mock.calls.length
      fireEvent.click(screen.getByRole('button', { name: 'stub-added' }))
      await waitFor(() => expect(vi.mocked(api.listAuthors).mock.calls.length).toBeGreaterThan(before))
      fireEvent.click(screen.getByRole('button', { name: 'stub-close' }))
      expect(screen.queryByRole('dialog', { name: 'add-author' })).toBeNull()

      fireEvent.click(screen.getByRole('button', { name: 'addToLibrary.addBook' }))
      expect(screen.getByRole('dialog', { name: 'add-book' })).toBeInTheDocument()
    })
  })

  describe('bulk actions', () => {
    it('confirms a bulk delete, sends every selected id and clears the selection', async () => {
      await renderWithAuthors()
      await selectBoth()
      const bar = screen.getByRole('button', { name: 'bulkActionBar.clear' }).parentElement as HTMLElement
      fireEvent.click(within(bar).getByRole('button', { name: 'common.delete' }))
      await acceptConfirm()
      await waitFor(() => expect(api.bulkActionAuthors).toHaveBeenCalledWith([7, 8], 'delete', undefined, false))
      await waitFor(() => expect(screen.getByTitle('Select Andy Weir')).not.toBeChecked())
    })

    it('skips a bulk delete when the confirmation is cancelled', async () => {
      await renderWithAuthors()
      await selectBoth()
      const bar = screen.getByRole('button', { name: 'bulkActionBar.clear' }).parentElement as HTMLElement
      fireEvent.click(within(bar).getByRole('button', { name: 'common.delete' }))
      await cancelConfirm()
      await waitFor(() => expect(confirmDialog()).toBeNull())
      expect(api.bulkActionAuthors).not.toHaveBeenCalled()
      expect(screen.getByTitle('Select Andy Weir')).toBeChecked()
    })

    it('alerts when a bulk action fails and keeps the selection', async () => {
      const alert = vi.spyOn(window, 'alert').mockImplementation(() => {})
      vi.mocked(api.bulkActionAuthors).mockRejectedValue(new Error('server down'))
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'common.search' }))
      await waitFor(() => expect(alert).toHaveBeenCalledWith('server down'))
      expect(screen.getByTitle('Select Andy Weir')).toBeChecked()
    })

    it.each([
      ['authors.bulkSetEbook', 'ebook'],
      ['authors.bulkSetAudiobook', 'audiobook'],
      ['authors.bulkSetBoth', 'both'],
    ])('sets the media type with %s after confirmation', async (label, mediaType) => {
      await renderWithAuthors()
      await selectBoth()
      fireEvent.click(screen.getByRole('button', { name: label }))
      await acceptConfirm()
      await waitFor(() => expect(api.bulkActionAuthors).toHaveBeenCalledWith([7, 8], 'set_media_type', mediaType))
      await waitFor(() => expect(screen.getByTitle('Select Andy Weir')).not.toBeChecked())
    })

    it('does not set the media type when cancelled, and alerts on failure', async () => {
      const alert = vi.spyOn(window, 'alert').mockImplementation(() => {})
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'authors.bulkSetEbook' }))
      await cancelConfirm()
      await waitFor(() => expect(confirmDialog()).toBeNull())
      expect(api.bulkActionAuthors).not.toHaveBeenCalled()

      vi.mocked(api.bulkActionAuthors).mockRejectedValue(new Error('rewrite failed'))
      fireEvent.click(screen.getByRole('button', { name: 'authors.bulkSetEbook' }))
      await acceptConfirm()
      await waitFor(() => expect(alert).toHaveBeenCalledWith('rewrite failed'))
    })

    it('closes the monitor/unmonitor dialog without acting from Cancel and the backdrop', async () => {
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'common.unmonitor' }))
      const dialog = screen.getByRole('dialog')
      expect(within(dialog).getByRole('heading', { name: 'common.unmonitor' })).toBeInTheDocument()
      fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
      expect(screen.queryByRole('dialog')).toBeNull()

      fireEvent.click(screen.getByRole('button', { name: 'common.monitor' }))
      fireEvent.click(screen.getByRole('dialog').parentElement as HTMLElement)
      expect(screen.queryByRole('dialog')).toBeNull()
      expect(api.bulkActionAuthors).not.toHaveBeenCalled()
    })

    it('unmonitors with the cascade off by default', async () => {
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'common.unmonitor' }))
      fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'common.apply' }))
      await waitFor(() => expect(api.bulkActionAuthors).toHaveBeenCalledWith([7], 'unmonitor', undefined, false))
    })

    it('sends a changed new-items policy and clamps an invalid latest count', async () => {
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'authors.bulkSetMonitorMode' }))
      const dialog = screen.getByRole('dialog')
      fireEvent.change(within(dialog).getByLabelText('editAuthorModal.monitorMode'), { target: { value: 'latest' } })
      const count = within(dialog).getByLabelText('editAuthorModal.monitorLatestCount')
      fireEvent.change(count, { target: { value: '-3' } })
      expect(count).toHaveValue(1)
      fireEvent.change(within(dialog).getByLabelText('editAuthorModal.monitorNewItems'), { target: { value: 'none' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'authors.bulkSetMonitorModeApply' }))
      await waitFor(() => expect(api.bulkSetAuthorMonitorMode).toHaveBeenCalledWith([7], 'latest', {
        monitorLatestCount: 1,
        applyMonitorModeToExisting: true,
        monitorNewItems: 'none',
      }))
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    })

    it('keeps the monitor mode dialog open with the error when the request throws', async () => {
      vi.mocked(api.bulkSetAuthorMonitorMode).mockRejectedValue(new Error('bad mode'))
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'authors.bulkSetMonitorMode' }))
      const dialog = screen.getByRole('dialog')
      fireEvent.click(within(dialog).getByRole('button', { name: 'authors.bulkSetMonitorModeApply' }))
      expect(await within(dialog).findByRole('alert')).toHaveTextContent('bad mode')

      fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
      expect(screen.queryByRole('dialog')).toBeNull()
    })

    it('uses a generic partial-failure error when the server gives none', async () => {
      vi.mocked(api.bulkSetAuthorMonitorMode).mockResolvedValue({ results: { '7': { ok: false } } } as never)
      await renderWithAuthors()
      fireEvent.click(screen.getByTitle('Select Andy Weir'))
      fireEvent.click(screen.getByRole('button', { name: 'authors.bulkSetMonitorMode' }))
      const dialog = screen.getByRole('dialog')
      fireEvent.click(within(dialog).getByRole('button', { name: 'authors.bulkSetMonitorModeApply' }))
      expect(await within(dialog).findByRole('alert')).toHaveTextContent('authors.bulkSetMonitorModePartial')
      // Closing from the backdrop works too.
      fireEvent.click(dialog.parentElement as HTMLElement)
      expect(screen.queryByRole('dialog')).toBeNull()
    })
  })
})
