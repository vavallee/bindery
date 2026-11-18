import { describe, it, expect, vi, beforeEach, afterEach, onTestFinished } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import MyRequestsPage from './MyRequestsPage'
import RequestsPage from './RequestsPage'

// One t for the whole file, like the real react-i18next, which hands out the
// same function on every render. A new t per render (#3606) gave
// RequestsPage's load callback, which lists t, a new identity on rerender, so
// the effect that runs it fetched the queue again: twice on mount instead of
// once, and again after an approval, which could put the row the test had
// just approved back on screen before waitFor looked.
vi.mock('react-i18next', () => {
  const translation = {
    t: (key: string, opts?: unknown) => {
      const vars = (opts && typeof opts === 'object' ? opts : {}) as Record<string, unknown>
      const strings: Record<string, string> = {
        'requests.mine.withdrawLabel': 'Withdraw request for {{title}}',
        'requests.mine.title': 'My requests',
        'requests.mine.reason': 'Reason: {{reason}}',
        'requests.status.pending': 'Waiting for an admin',
        'requests.status.declined': 'Declined',
        'requests.status.available': 'Available',
        'requests.status.approved': 'Approved, not here yet',
        'requests.status.authorProgress': '{{imported}} of {{total}} books here',
        'requests.admin.title': 'Requests',
        'requests.admin.empty': 'No requests here.',
        'requests.admin.emptyHint': 'Try another filter to see requests that were already decided.',
        'requests.admin.emptyPending': 'No requests are waiting.',
        'requests.admin.emptyPendingHint': 'New requests from your users show up here for you to approve or decline.',
        'requests.admin.approveLabel': 'Approve request for {{title}}',
        'requests.admin.declineLabel': 'Decline request for {{title}}',
        'requests.admin.approveFormLabel': 'Approve {{title}}',
        'requests.admin.declineFormLabel': 'Decline {{title}}',
        'requests.admin.confirmApprove': 'Approve and add',
        'requests.admin.confirmDecline': 'Decline',
        'requests.admin.reason': 'Reason',
        'requests.admin.format': 'Format',
        'requests.admin.searchOnAdd': 'Search indexers after adding',
        'requests.admin.metadataProfile': 'Metadata profile',
        'requests.admin.rootFolder': 'Root folder',
        'requests.admin.monitorMode': 'Monitor',
        'requests.admin.qualityProfile': 'Quality profile',
        'common.qualityProfileNone': 'None (accepts every format)',
        'requests.admin.requestedBy': 'asked by {{username}} on {{date}}',
        'requests.admin.alreadyInLibrary': 'Already in the library',
        'requests.admin.createdByAPIKey': 'made for them with the API key',
        'requests.admin.createdByAdmin': 'made for them by {{username}}',
      }
      const s = strings[key] ?? key
      return s.replace(/\{\{(\w+)\}\}/g, (_m, name: string) => String(vars[name] ?? ''))
    },
  }
  return { useTranslation: () => translation }
})

vi.mock('../../api/client', () => ({
  api: {
    listMyRequests: vi.fn(),
    withdrawRequest: vi.fn(),
    listRequestQueue: vi.fn(),
    approveRequest: vi.fn(),
    declineRequest: vi.fn(),
    listMetadataProfiles: vi.fn().mockResolvedValue([{ id: 1, name: 'Standard' }]),
    listRootFolders: vi.fn().mockResolvedValue([{ id: 4, path: '/books' }]),
    listQualityProfiles: vi.fn().mockResolvedValue([]),
    getSetting: vi.fn().mockRejectedValue(new Error('unset')),
  },
}))

import { api } from '../../api/client'
import type { LibraryRequest } from '../../api/client'

function req(overrides: Partial<LibraryRequest>): LibraryRequest {
  return {
    id: 1, kind: 'book', foreignId: 'OL1W', mediaType: '', title: 'Dune', authorName: 'Frank Herbert',
    status: 'pending', createdAt: '2026-09-17T00:00:00Z', fulfilled: false, ...overrides,
  }
}

// #3606: these tests query by label and text rather than by role and
// accessible name. getByRole walks the whole document and computes styles for
// every candidate on each call, which on a loaded CI runner was enough to push
// a test past its timeout. The controls all carry aria-label or a <label>, so
// the label queries still find them by what a person or screen reader is told.

// openApproveForm opens the inline approve form. For an author it also waits
// until the instance defaults have loaded, which is what enables the submit
// button, so no assertion runs against a half initialised form.
async function openApproveForm(title: string, kind: 'book' | 'author' = 'book') {
  fireEvent.click(await screen.findByLabelText(`Approve request for ${title}`))
  const form = screen.getByLabelText(`Approve ${title}`)
  const submit = within(form).getByText('Approve and add')
  if (kind === 'author') await waitFor(() => expect(submit).toBeEnabled())
  return { form, submit }
}

describe('MyRequestsPage', () => {
  beforeEach(() => vi.clearAllMocks())

  it('lists what became of each request and withdraws a pending one', async () => {
    vi.mocked(api.listMyRequests).mockResolvedValue({
      items: [
        req({ id: 1, title: 'Dune' }),
        req({ id: 2, title: 'Emma', status: 'declined', declineReason: 'Not this year' }),
        req({ id: 3, title: 'Ubik', status: 'approved', fulfilled: true }),
        req({ id: 4, kind: 'author', title: 'Ursula K. Le Guin', status: 'approved', booksTotal: 5, booksImported: 2, fulfilled: true }),
      ],
      total: 4, limit: 50, offset: 0,
    })
    vi.mocked(api.withdrawRequest).mockResolvedValue(undefined)
    render(<MemoryRouter><MyRequestsPage /></MemoryRouter>)

    const list = await screen.findByLabelText('My requests')
    expect(list.tagName).toBe('UL')
    expect(within(list).getByText('Waiting for an admin')).toBeInTheDocument()
    expect(within(list).getByText('Declined')).toBeInTheDocument()
    expect(within(list).getByText('Reason: Not this year')).toBeInTheDocument()
    expect(within(list).getByText('Available')).toBeInTheDocument()
    expect(within(list).getByText('2 of 5 books here')).toBeInTheDocument()
    // Only the pending request can be withdrawn.
    expect(screen.queryByLabelText('Withdraw request for Emma')).not.toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Withdraw request for Dune'))
    await waitFor(() => expect(screen.queryByText('Dune')).not.toBeInTheDocument())
    expect(api.withdrawRequest).toHaveBeenCalledWith(1)
  })
})

describe('RequestsPage', () => {
  beforeEach(() => vi.clearAllMocks())

  it('approves a book request with the choices from the inline form', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [req({ id: 9, mediaType: 'audiobook', username: 'reader' })], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.approveRequest).mockResolvedValue(req({ id: 9, status: 'approved' }))
    const changed = vi.fn()
    window.addEventListener('bindery:requests-changed', changed)
    // Removed even when an assertion below fails, so a failure here cannot
    // leave a listener behind for the tests after it.
    onTestFinished(() => window.removeEventListener('bindery:requests-changed', changed))
    render(<RequestsPage />)

    const { form, submit } = await openApproveForm('Dune')
    expect(within(form).getByLabelText('Format')).toHaveValue('audiobook')
    expect(within(form).queryByLabelText('Root folder')).not.toBeInTheDocument()
    fireEvent.click(within(form).getByLabelText('Search indexers after adding'))
    fireEvent.click(submit)

    await waitFor(() => expect(api.approveRequest).toHaveBeenCalledWith(9, { searchOnAdd: false, mediaType: 'audiobook' }))
    await waitFor(() => expect(screen.queryByText('Dune')).not.toBeInTheDocument())
    expect(changed).toHaveBeenCalled()
    // A book approval has none of the author choices, so it fetches no defaults.
    expect(api.listMetadataProfiles).not.toHaveBeenCalled()
    expect(api.getSetting).not.toHaveBeenCalled()
  })

  // #3835: the book reached the library while the request waited.
  it('flags a pending request the library already covers, and shows what the approval did', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({
      items: [
        req({ id: 1, title: 'Covered', username: 'reader', alreadyInLibrary: true }),
        req({ id: 2, title: 'Standing', username: 'reader' }),
      ],
      total: 2, limit: 50, offset: 0,
    })
    const sentence = 'This book was already in the library with the requested format, so nothing was added and the request is resolved.'
    vi.mocked(api.approveRequest).mockResolvedValue(req({ id: 1, title: 'Covered', status: 'approved', notice: sentence }))
    render(<RequestsPage />)

    const row = async (title: string) => (await screen.findByText(title)).closest('li') as HTMLElement
    expect(within(await row('Covered')).getByText('Already in the library')).toBeInTheDocument()
    expect(within(await row('Standing')).queryByText('Already in the library')).not.toBeInTheDocument()

    fireEvent.click((await openApproveForm('Covered')).submit)

    // The row leaves the pending list, and the server's sentence stays on screen.
    await waitFor(() => expect(screen.queryByText('Covered')).not.toBeInTheDocument())
    expect(screen.getByText(sentence).closest('[role="status"]')).not.toBeNull()
  })

  it('says who made a request on its owner\'s behalf', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({
      items: [
        req({ id: 1, title: 'By key', username: 'reader', createdVia: 'api_key' }),
        req({ id: 2, title: 'By admin', username: 'reader', createdVia: 'admin', createdBy: 'boss' }),
        req({ id: 3, title: 'Own', username: 'reader' }),
      ],
      total: 3, limit: 50, offset: 0,
    })
    render(<RequestsPage />)

    const row = async (title: string) => (await screen.findByText(title)).closest('li') as HTMLElement
    expect(within(await row('By key')).getByText(/made for them with the API key/)).toBeInTheDocument()
    expect(within(await row('By admin')).getByText(/made for them by boss/)).toBeInTheDocument()
    expect(within(await row('Own')).queryByText(/made for them/)).not.toBeInTheDocument()
    // The queue loads once, not again on every render (#3606).
    expect(api.listRequestQueue).toHaveBeenCalledTimes(1)
  })

  it('offers the author choices prefilled from the instance defaults', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [req({ id: 5, kind: 'author', title: 'Frank Herbert' })], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.approveRequest).mockResolvedValue(req({ id: 5, kind: 'author', status: 'approved' }))
    render(<RequestsPage />)

    const { form, submit } = await openApproveForm('Frank Herbert', 'author')
    expect(within(form).getByLabelText('Metadata profile')).toHaveValue('1')
    expect(within(form).getByLabelText('Root folder')).toHaveValue('4')
    fireEvent.click(submit)

    await waitFor(() => expect(api.approveRequest).toHaveBeenCalledWith(5, expect.objectContaining({
      metadataProfileId: 1, rootFolderId: 4, monitorMode: 'all', mediaType: 'ebook', searchOnAdd: false,
    })))
  })

  it('keeps the format the requester asked for on an author request instead of the default', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [req({ id: 6, kind: 'author', title: 'Ann Leckie', mediaType: 'audiobook' })], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.approveRequest).mockResolvedValue(req({ id: 6, kind: 'author', status: 'approved' }))
    render(<RequestsPage />)

    const { form, submit } = await openApproveForm('Ann Leckie', 'author')
    expect(within(form).getByLabelText('Format')).toHaveValue('audiobook')
    fireEvent.click(submit)

    await waitFor(() => expect(api.approveRequest).toHaveBeenCalledWith(6, expect.objectContaining({ mediaType: 'audiobook' })))
  })

  // #3831: the picker shows the default but sends a profile only once the
  // admin changes it, so an untouched picker leaves the default to the server.
  describe('quality profile picker (#3831)', () => {
    const qps = [
      { id: 1, name: 'Any', upgradeAllowed: false, cutoff: '', items: [] },
      { id: 2, name: 'Epub only', upgradeAllowed: false, cutoff: '', items: [] },
    ]
    const approveAuthor = async () => {
      vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [req({ id: 5, kind: 'author', title: 'Frank Herbert' })], total: 1, limit: 50, offset: 0 })
      vi.mocked(api.approveRequest).mockResolvedValue(req({ id: 5, kind: 'author', status: 'approved' }))
      render(<RequestsPage />)
      const { form, submit } = await openApproveForm('Frank Herbert', 'author')
      return { submit, picker: within(form).getByLabelText('Quality profile') }
    }

    beforeEach(() => {
      vi.mocked(api.listQualityProfiles).mockResolvedValue(qps)
      vi.mocked(api.getSetting).mockImplementation(async (key: string) => {
        if (key === 'author.default_quality_profile_id') return { key, value: '2' }
        throw new Error('unset')
      })
    })
    afterEach(() => {
      vi.mocked(api.listQualityProfiles).mockResolvedValue([])
      vi.mocked(api.getSetting).mockRejectedValue(new Error('unset'))
    })

    it('shows the default and leaves qualityProfileId out when untouched', async () => {
      const { submit, picker } = await approveAuthor()
      expect(picker).toHaveValue('2')
      fireEvent.click(submit)
      await waitFor(() => expect(api.approveRequest).toHaveBeenCalledTimes(1))
      expect('qualityProfileId' in vi.mocked(api.approveRequest).mock.calls[0][1]).toBe(false)
    })

    it('sends the profile, or null for None, once the admin changes it', async () => {
      const { submit, picker } = await approveAuthor()
      expect(picker).toHaveValue('2')
      fireEvent.change(picker, { target: { value: '' } })
      fireEvent.click(submit)
      await waitFor(() => expect(api.approveRequest).toHaveBeenCalledTimes(1))
      const choices = vi.mocked(api.approveRequest).mock.calls[0][1]
      expect('qualityProfileId' in choices).toBe(true)
      expect(choices.qualityProfileId).toBeNull()
    })
  })

  it('declines with the reason typed', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [req({ id: 3 })], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.declineRequest).mockResolvedValue(req({ id: 3, status: 'declined' }))
    render(<RequestsPage />)

    fireEvent.click(await screen.findByLabelText('Decline request for Dune'))
    const form = screen.getByLabelText('Decline Dune')
    fireEvent.change(within(form).getByLabelText('Reason'), { target: { value: '  Not in scope  ' } })
    fireEvent.click(within(form).getByText('Decline'))

    await waitFor(() => expect(api.declineRequest).toHaveBeenCalledWith(3, 'Not in scope'))
  })

  it('titles the tab like every other page', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    render(<RequestsPage />)

    await waitFor(() => expect(document.title).toBe('Requests \u00b7 Bindery'))
  })

  it('gives the empty queue a hint instead of a bare left aligned line', async () => {
    vi.mocked(api.listRequestQueue).mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    render(<RequestsPage />)

    const empty = await screen.findByText('No requests are waiting.')
    expect(screen.getByText('New requests from your users show up here for you to approve or decline.')).toBeInTheDocument()
    expect(empty.parentElement).toHaveClass('text-center')
  })
})
