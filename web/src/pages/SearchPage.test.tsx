import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SearchPage from './SearchPage'
import { ApiError, api } from '../api/client'
import type { Download, SearchResult } from '../api/client'
import en from '../i18n/locales/en.json'

// Resolve a dotted i18n key against the real English locale so the test asserts
// against the strings the page actually renders.
function resolveKey(key: string): string | undefined {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let node: any = en
  for (const part of key.split('.')) {
    if (node && typeof node === 'object' && part in node) node = node[part]
    else return undefined
  }
  return typeof node === 'string' ? node : undefined
}

function translate(key: string, arg?: unknown): string {
  const resolved = resolveKey(key)
  const fallback = typeof arg === 'string' ? arg : key
  let str = resolved ?? fallback
  if (arg && typeof arg === 'object') {
    for (const [k, v] of Object.entries(arg as Record<string, unknown>)) {
      str = str.replace(new RegExp(`{{\\s*${k}\\s*}}`, 'g'), String(v))
    }
  }
  return str
}

const translation = { t: translate }

vi.mock('react-i18next', () => ({
  useTranslation: () => translation,
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      searchIndexers: vi.fn(),
      grab: vi.fn(),
    },
  }
})

function makeResult(overrides: Partial<SearchResult> & { guid: string; title: string }): SearchResult {
  return {
    indexerName: 'Test Indexer',
    size: 1572864,
    nzbUrl: 'https://indexer.example.com/release.nzb',
    grabs: 5,
    pubDate: '2026-05-01',
    protocol: 'usenet',
    ...overrides,
  }
}

function makeDownload(overrides: Partial<Download> = {}): Download {
  return {
    id: 1,
    guid: 'dl-guid',
    title: 'Queued release',
    status: 'queued',
    size: 1572864,
    protocol: 'usenet',
    errorMessage: '',
    addedAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function renderSearchPage() {
  return render(
    <MemoryRouter>
      <SearchPage />
    </MemoryRouter>,
  )
}

const noClientBody = en.search.noClient.body
const noClientAction = en.search.noClient.action

async function searchAndGetGrabButton() {
  vi.mocked(api.searchIndexers).mockResolvedValue([
    makeResult({ guid: 'g1', title: 'Some Book EPUB' }),
  ])
  renderSearchPage()
  fireEvent.change(screen.getByPlaceholderText(en.search.placeholder), {
    target: { value: 'some book' },
  })
  fireEvent.submit(screen.getByPlaceholderText(en.search.placeholder).closest('form')!)
  return screen.findByRole('button', { name: en.search.grab })
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('SearchPage no-download-client nudge', () => {
  it('shows the contextual setup nudge when a grab fails with the no-client error', async () => {
    const grabButton = await searchAndGetGrabButton()
    vi.mocked(api.grab).mockRejectedValue(
      new ApiError(400, { error: 'no enabled usenet download client configured — add a download client' }, 'Bad Request'),
    )

    fireEvent.click(grabButton)

    expect(await screen.findByText(noClientBody)).toBeInTheDocument()
    const link = screen.getByRole('link', { name: noClientAction })
    expect(link).toHaveAttribute('href', '/settings?tab=clients')
  })

  it('does not show the nudge on a successful grab', async () => {
    const grabButton = await searchAndGetGrabButton()
    vi.mocked(api.grab).mockResolvedValue(makeDownload({ guid: 'g1' }))

    fireEvent.click(grabButton)

    await waitFor(() => expect(screen.getByText(en.search.grabbed)).toBeInTheDocument())
    expect(screen.queryByText(noClientBody)).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: noClientAction })).not.toBeInTheDocument()
  })

  it('shows the raw error (not the nudge) for an unrelated grab failure', async () => {
    const grabButton = await searchAndGetGrabButton()
    vi.mocked(api.grab).mockRejectedValue(
      new ApiError(502, { error: 'indexer unreachable' }, 'Bad Gateway'),
    )

    fireEvent.click(grabButton)

    expect(await screen.findByText('indexer unreachable')).toBeInTheDocument()
    expect(screen.queryByText(noClientBody)).not.toBeInTheDocument()
  })
})
