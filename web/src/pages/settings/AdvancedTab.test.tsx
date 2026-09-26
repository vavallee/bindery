import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import AdvancedTab from './AdvancedTab'
import { api, SettingDescriptor } from '../../api/client'

// #2311. The Advanced tab renders from GET /api/v1/settings/descriptors alone.
// Every assertion below is about descriptor metadata deciding the control, so a
// new settings key needs no code here and no locale entry.

let mockIsAdmin = true

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: mockIsAdmin, role: mockIsAdmin ? 'admin' : 'user', loading: false }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettingDescriptors: vi.fn(),
      listSettings: vi.fn(),
      setSetting: vi.fn(),
      deleteSetting: vi.fn(),
    },
  }
})

function descriptor(over: Partial<SettingDescriptor> & { key: string }): SettingDescriptor {
  return {
    type: 'string',
    default: '',
    description: 'A description from the server.',
    restartRequired: false,
    state: 'active',
    secret: false,
    adminOnly: false,
    writable: true,
    ...over,
  }
}

function seed(descriptors: SettingDescriptor[], stored: Record<string, string> = {}) {
  vi.mocked(api.listSettingDescriptors).mockResolvedValue(descriptors)
  vi.mocked(api.listSettings).mockResolvedValue(Object.entries(stored).map(([key, value]) => ({ key, value })))
}

async function row(key: string) {
  return await screen.findByTestId(`setting-row-${key}`)
}

beforeEach(() => {
  mockIsAdmin = true
  vi.mocked(api.listSettingDescriptors).mockReset()
  vi.mocked(api.listSettings).mockReset()
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined)
  vi.mocked(api.deleteSetting).mockReset().mockResolvedValue(undefined)
  seed([])
})

describe('AdvancedTab control selection', () => {
  it('renders a bool descriptor as a switch and saves the literal true', async () => {
    seed([descriptor({ key: 'calibre.sync_on_startup', type: 'bool', default: 'false' })])
    render(<AdvancedTab />)
    const toggle = await screen.findByRole('switch', { name: 'calibre.sync_on_startup' })
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(toggle)
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('calibre.sync_on_startup', 'true'))
  })

  it('renders an enum descriptor as a select offering exactly its values plus the default', async () => {
    seed([descriptor({ key: 'default.media_type', type: 'enum', default: 'ebook', values: ['ebook', 'audiobook', 'both'] })])
    render(<AdvancedTab />)
    const select = (await screen.findByLabelText('default.media_type')) as HTMLSelectElement
    expect([...select.options].map(o => o.value)).toEqual(['', 'ebook', 'audiobook', 'both'])
    expect(select.value).toBe('')
  })

  it('renders an int descriptor as a number input carrying the descriptor bounds', async () => {
    seed([descriptor({ key: 'requests.max_pending_per_user', type: 'int', default: '25', min: '1', max: '10000' })], {
      'requests.max_pending_per_user': '40',
    })
    render(<AdvancedTab />)
    const input = (await screen.findByLabelText('requests.max_pending_per_user')) as HTMLInputElement
    expect(input.type).toBe('number')
    expect(input).toHaveAttribute('min', '1')
    expect(input).toHaveAttribute('max', '10000')
    expect(input.value).toBe('40')
  })

  it('renders a duration descriptor as text and states the accepted range', async () => {
    seed([descriptor({ key: 'search.interval', type: 'duration', default: '12h', min: '1h', max: '168h' })])
    render(<AdvancedTab />)
    const input = (await screen.findByLabelText('search.interval')) as HTMLInputElement
    expect(input.type).toBe('text')
    expect(await row('search.interval')).toHaveTextContent('1h')
    expect(await row('search.interval')).toHaveTextContent('168h')
  })

  it('shows the server description as the help text', async () => {
    seed([descriptor({ key: 'log.retention_days', type: 'int', default: '14', description: 'Days to keep log rows.' })])
    render(<AdvancedTab />)
    expect(await row('log.retention_days')).toHaveTextContent('Days to keep log rows.')
  })
})

describe('AdvancedTab restart required', () => {
  it('marks a restart-required key on its own row', async () => {
    seed([
      descriptor({ key: 'search.interval', type: 'duration', default: '12h', restartRequired: true }),
      descriptor({ key: 'log.retention_days', type: 'int', default: '14' }),
    ])
    render(<AdvancedTab />)
    expect(await row('search.interval')).toHaveTextContent('Restart required')
    expect(await row('log.retention_days')).not.toHaveTextContent('Restart required')
  })
})

describe('AdvancedTab secrets', () => {
  it('never renders a secret value, even when the API hands one back', async () => {
    seed([descriptor({ key: 'hardcover.api_token', secret: true, writable: true, adminOnly: true })], {
      'hardcover.api_token': 'hc_supersecret',
    })
    render(<AdvancedTab />)
    const r = await row('hardcover.api_token')
    expect(r).not.toHaveTextContent('hc_supersecret')
    const input = within(r).getByLabelText('hardcover.api_token') as HTMLInputElement
    expect(input.type).toBe('password')
    expect(input.value).toBe('')
  })

  it('offers no control for a secret the generic settings API refuses to write', async () => {
    seed([descriptor({ key: 'auth.session_secret', secret: true, writable: false, adminOnly: true })], {
      'auth.session_secret': 'never-shown',
    })
    render(<AdvancedTab />)
    const r = await row('auth.session_secret')
    expect(r).not.toHaveTextContent('never-shown')
    expect(within(r).queryByLabelText('auth.session_secret')).toBeNull()
    expect(within(r).queryByRole('textbox')).toBeNull()
    expect(r).toHaveTextContent('own settings screen')
  })

  it('offers no reset for a secret, because the server refuses to delete one', async () => {
    seed([descriptor({ key: 'hardcover.api_token', secret: true, writable: true })], {
      'hardcover.api_token': 'hc_supersecret',
    })
    render(<AdvancedTab />)
    const r = await row('hardcover.api_token')
    expect(within(r).queryByRole('button', { name: /reset/i })).toBeNull()
  })
})

describe('AdvancedTab state gating', () => {
  it('refuses to edit an inert key and offers to remove the row instead', async () => {
    seed([descriptor({ key: 'calibre.drop_folder_path', state: 'inert' })], { 'calibre.drop_folder_path': '/old' })
    render(<AdvancedTab />)
    const r = await row('calibre.drop_folder_path')
    expect(within(r).queryByLabelText('calibre.drop_folder_path')).toBeNull()
    expect(r).toHaveTextContent('Nothing reads this')
    expect(within(r).getByRole('button', { name: /reset/i })).toBeInTheDocument()
  })

  it('refuses to edit a key Bindery writes for itself', async () => {
    seed([descriptor({ key: 'library.last_scan', state: 'internal' })], { 'library.last_scan': '{}' })
    render(<AdvancedTab />)
    const r = await row('library.last_scan')
    expect(within(r).queryByLabelText('library.last_scan')).toBeNull()
    expect(r).toHaveTextContent('Bindery writes this')
  })
})

describe('AdvancedTab admin gate', () => {
  it('renders nothing editable and asks for no data when the caller is not an admin', async () => {
    mockIsAdmin = false
    seed([descriptor({ key: 'log.retention_days', type: 'int', default: '14' })])
    render(<AdvancedTab />)
    expect(await screen.findByText(/administrators/i)).toBeInTheDocument()
    expect(api.listSettingDescriptors).not.toHaveBeenCalled()
    expect(api.listSettings).not.toHaveBeenCalled()
    expect(screen.queryByTestId('setting-row-log.retention_days')).toBeNull()
  })
})

describe('AdvancedTab unknown stored keys', () => {
  it('warns about a stored key with no descriptor and does not offer to edit it', async () => {
    seed([descriptor({ key: 'log.retention_days', type: 'int', default: '14' })], { 'serch.interval': '6h' })
    render(<AdvancedTab />)
    const r = await row('serch.interval')
    expect(r).toHaveTextContent('serch.interval')
    expect(r).toHaveTextContent('does not recognise')
    expect(within(r).queryByLabelText('serch.interval')).toBeNull()
    expect(within(r).getByRole('button', { name: /remove/i })).toBeInTheDocument()
  })
})

describe('AdvancedTab saving', () => {
  it('surfaces the server rejection message when a save fails', async () => {
    seed([descriptor({ key: 'search.interval', type: 'duration', default: '12h' })])
    vi.mocked(api.setSetting).mockRejectedValue(new Error('search.interval "9000h" is above the maximum of 168h'))
    render(<AdvancedTab />)
    const input = await screen.findByLabelText('search.interval')
    fireEvent.change(input, { target: { value: '9000h' } })
    fireEvent.click(within(await row('search.interval')).getByRole('button', { name: /save/i }))
    expect(await screen.findByText(/above the maximum of 168h/)).toBeInTheDocument()
  })

  it('resets a key by deleting its row', async () => {
    seed([descriptor({ key: 'log.retention_days', type: 'int', default: '14' })], { 'log.retention_days': '30' })
    render(<AdvancedTab />)
    const r = await row('log.retention_days')
    fireEvent.click(within(r).getByRole('button', { name: /reset/i }))
    await waitFor(() => expect(api.deleteSetting).toHaveBeenCalledWith('log.retention_days'))
  })

  it('offers no reset for a key that has no stored row', async () => {
    seed([descriptor({ key: 'log.retention_days', type: 'int', default: '14' })])
    render(<AdvancedTab />)
    const r = await row('log.retention_days')
    expect(within(r).queryByRole('button', { name: /reset/i })).toBeNull()
  })
})

describe('AdvancedTab filtering', () => {
  it('narrows the list by key and by description', async () => {
    seed([
      descriptor({ key: 'search.interval', type: 'duration', default: '12h', description: 'How often the sweep runs.' }),
      descriptor({ key: 'log.retention_days', type: 'int', default: '14', description: 'Days to keep log rows.' }),
    ])
    render(<AdvancedTab />)
    const filter = await screen.findByLabelText('Filter settings')
    fireEvent.change(filter, { target: { value: 'retention' } })
    await waitFor(() => expect(screen.queryByTestId('setting-row-search.interval')).toBeNull())
    expect(screen.getByTestId('setting-row-log.retention_days')).toBeInTheDocument()

    fireEvent.change(filter, { target: { value: 'sweep runs' } })
    await waitFor(() => expect(screen.queryByTestId('setting-row-log.retention_days')).toBeNull())
    expect(screen.getByTestId('setting-row-search.interval')).toBeInTheDocument()
  })
})
