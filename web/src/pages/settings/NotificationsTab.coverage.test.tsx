import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) =>
      opts && typeof opts === 'object' && 'error' in opts ? `${key}:${(opts as { error: string }).error}` : key,
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    listNotifications: vi.fn(),
    addNotification: vi.fn(),
    updateNotification: vi.fn(),
    deleteNotification: vi.fn(),
    testNotification: vi.fn(),
  },
}))

import { api, NotificationConfig } from '../../api/client'
import NotificationsTab from './NotificationsTab'

function notification(overrides: Partial<NotificationConfig> = {}): NotificationConfig {
  return {
    id: 1,
    name: 'Discord',
    type: 'webhook',
    url: 'https://example.test/hook',
    topic: '',
    method: 'POST',
    headers: '{}',
    onGrab: true,
    onImport: true,
    onUpgrade: false,
    onFailure: true,
    onHealth: false,
    onBookAnnounced: false,
    enabled: true,
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listNotifications).mockResolvedValue([])
})

describe('NotificationsTab list', () => {
  it('shows the empty state', async () => {
    render(<NotificationsTab />)
    expect(await screen.findByText('settings.notifications.empty')).toBeInTheDocument()
  })

  it('stays on the empty state when loading fails', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.listNotifications).mockRejectedValue(new Error('down'))
    render(<NotificationsTab />)
    await waitFor(() => expect(err).toHaveBeenCalled())
    expect(screen.getByText('settings.notifications.empty')).toBeInTheDocument()
    err.mockRestore()
  })

  it('shows a badge for every enabled trigger', async () => {
    vi.mocked(api.listNotifications).mockResolvedValue([notification({
      onUpgrade: true, onHealth: true, onBookAnnounced: true, onRequestCreated: true,
    })])
    render(<NotificationsTab />)
    for (const k of ['onGrab', 'onImport', 'onUpgrade', 'onFailure', 'onHealth', 'onBookAnnounced', 'onRequestCreated']) {
      expect(await screen.findByText(`settings.notifications.${k}`)).toBeInTheDocument()
    }
  })

  it('enables and disables a notification through the toggle', async () => {
    const n = notification({ enabled: false })
    vi.mocked(api.listNotifications).mockResolvedValue([n])
    vi.mocked(api.updateNotification).mockResolvedValue({ ...n, enabled: true })
    render(<NotificationsTab />)
    const toggle = await screen.findByRole('switch')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(toggle).toHaveAttribute('title', 'common.enable')
    fireEvent.click(toggle)
    await waitFor(() => expect(api.updateNotification).toHaveBeenCalledWith(1, { ...n, enabled: true }))
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'true'))
  })

  it('reports a successful and a failed test send', async () => {
    vi.mocked(api.listNotifications).mockResolvedValue([notification()])
    vi.mocked(api.testNotification).mockResolvedValueOnce(undefined as never)
    vi.mocked(api.testNotification).mockRejectedValueOnce(new Error('timeout'))
    vi.mocked(api.testNotification).mockRejectedValueOnce('weird')
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.test' }))
    expect(await screen.findByRole('status')).toHaveTextContent('settings.notifications.testSent')
    expect(api.testNotification).toHaveBeenCalledWith(1)
    fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('common.connFail:timeout'))
    fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('common.connFail:Unknown error'))
  })

  it('asks before deleting and can back out', async () => {
    vi.mocked(api.listNotifications).mockResolvedValue([notification(), notification({ id: 2, name: 'Ntfy' })])
    vi.mocked(api.deleteNotification).mockResolvedValue(undefined as never)
    render(<NotificationsTab />)
    await screen.findByText('Ntfy')
    fireEvent.click(screen.getAllByRole('button', { name: 'common.delete' })[0])
    fireEvent.click(screen.getByRole('button', { name: 'common.no' }))
    expect(screen.queryByRole('button', { name: 'common.yes' })).not.toBeInTheDocument()
    expect(api.deleteNotification).not.toHaveBeenCalled()

    fireEvent.click(screen.getAllByRole('button', { name: 'common.delete' })[0])
    fireEvent.click(screen.getByRole('button', { name: 'common.yes' }))
    await waitFor(() => expect(api.deleteNotification).toHaveBeenCalledWith(1))
    await waitFor(() => expect(screen.queryByText('Discord')).not.toBeInTheDocument())
    expect(screen.getByText('Ntfy')).toBeInTheDocument()
  })
})

describe('NotificationsTab add form', () => {
  it('adds a webhook with every field and trigger set', async () => {
    const created = notification({ id: 9, name: 'Hook' })
    vi.mocked(api.addNotification).mockResolvedValue(created)
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'settings.notifications.addButton' }))
    // The trigger hint list renders with each chip.
    expect(screen.getByRole('list', { name: 'settings.notifications.triggerHintsLabel' })).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('Name'), { target: { value: 'Hook' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'PUT' } })
    fireEvent.change(screen.getByPlaceholderText('Webhook URL'), { target: { value: 'https://ntfy.sh' } })
    fireEvent.change(screen.getByPlaceholderText('my-topic'), { target: { value: 'books' } })
    fireEvent.change(document.querySelector('textarea')!, {
      target: { value: ' {"Authorization": "Bearer x"} ' },
    })
    for (const chip of ['Grab', 'Import', 'Failure', 'Upgrade', 'Health']) {
      fireEvent.click(screen.getByRole('button', { name: chip }))
    }
    fireEvent.click(screen.getByRole('button', { name: 'settings.notifications.requestToggle' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.addNotification).toHaveBeenCalledWith({
      name: 'Hook',
      url: 'https://ntfy.sh',
      topic: 'books',
      method: 'PUT',
      type: 'webhook',
      headers: '{"Authorization":"Bearer x"}',
      onGrab: false,
      onImport: false,
      onFailure: false,
      onUpgrade: true,
      onHealth: true,
      onBookAnnounced: false,
      onRequestCreated: true,
      enabled: true,
    }))
    expect(await screen.findByText('Hook')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Webhook URL')).not.toBeInTheDocument()
  })

  it('refuses invalid headers without calling the API', async () => {
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'settings.notifications.addButton' }))
    const textarea = document.querySelector('textarea')!
    for (const bad of ['not json', '[1,2]', '{"a": 1}', 'null']) {
      fireEvent.change(textarea, { target: { value: bad } })
      fireEvent.click(screen.getByRole('button', { name: 'Save' }))
      expect(await screen.findByRole('alert')).toBeInTheDocument()
    }
    expect(api.addNotification).not.toHaveBeenCalled()
  })

  it('shows the server error when adding fails and closes on cancel', async () => {
    vi.mocked(api.addNotification).mockRejectedValueOnce(new Error('bad url'))
    vi.mocked(api.addNotification).mockRejectedValueOnce(7)
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'settings.notifications.addButton' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('settings.notifications.saveFailed:bad url')
    expect(vi.mocked(api.addNotification).mock.calls[0][0]).toMatchObject({ headers: '{}', onGrab: true, onImport: true, onFailure: true })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('settings.notifications.saveFailed:Unknown error'))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByPlaceholderText('Webhook URL')).not.toBeInTheDocument()
  })
})

describe('NotificationsTab edit form', () => {
  it('pretty-prints stored headers and saves edited fields', async () => {
    const n = notification({ topic: 'old', method: 'GET', headers: '{"X-Key":"abc"}' })
    vi.mocked(api.listNotifications).mockResolvedValue([n])
    vi.mocked(api.updateNotification).mockImplementation(async (_id, body) => body as NotificationConfig)
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    const textarea = document.querySelector('textarea')!
    expect(textarea.value).toBe('{\n  "X-Key": "abc"\n}')
    expect(screen.getByDisplayValue('old')).toBeInTheDocument()
    expect(screen.getByRole('combobox')).toHaveValue('GET')

    fireEvent.change(screen.getByDisplayValue('Discord'), { target: { value: 'Renamed' } })
    fireEvent.change(screen.getByDisplayValue('https://example.test/hook'), { target: { value: 'https://x.test' } })
    fireEvent.change(screen.getByDisplayValue('old'), { target: { value: 'new' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'POST' } })
    fireEvent.change(textarea, { target: { value: '' } })
    for (const chip of ['Grab', 'Import', 'Failure', 'Upgrade', 'Health']) {
      fireEvent.click(screen.getByRole('button', { name: chip }))
    }
    fireEvent.click(screen.getByRole('button', { name: 'settings.notifications.requestToggle' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.updateNotification).toHaveBeenCalledWith(1, {
      ...n,
      name: 'Renamed',
      url: 'https://x.test',
      topic: 'new',
      method: 'POST',
      headers: '{}',
      onGrab: false,
      onImport: false,
      onFailure: false,
      onUpgrade: true,
      onHealth: true,
      onBookAnnounced: false,
      onRequestCreated: true,
    }))
    expect(await screen.findByText('Renamed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  })

  it('keeps unparseable stored headers verbatim and rejects them on save', async () => {
    vi.mocked(api.listNotifications).mockResolvedValue([notification({ headers: 'garbage', topic: undefined as unknown as string, method: '' })])
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    expect(document.querySelector('textarea')!.value).toBe('garbage')
    expect(screen.getByRole('combobox')).toHaveValue('POST')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(api.updateNotification).not.toHaveBeenCalled()
  })

  it('shows the server error when saving fails, and closes on cancel or a second edit click', async () => {
    vi.mocked(api.listNotifications).mockResolvedValue([notification({ headers: '' })])
    vi.mocked(api.updateNotification).mockRejectedValueOnce(new Error('conflict'))
    vi.mocked(api.updateNotification).mockRejectedValueOnce(null)
    render(<NotificationsTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'common.edit' }))
    expect(document.querySelector('textarea')!.value).toBe('')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('settings.notifications.saveFailed:conflict')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('settings.notifications.saveFailed:Unknown error'))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
    const row = screen.getByText('Discord').closest('div.p-4') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'common.edit' }))
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  })
})
