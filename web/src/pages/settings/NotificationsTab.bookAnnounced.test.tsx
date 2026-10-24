import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, opts?: unknown) => (typeof opts === 'string' ? opts : key) }),
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
})

// #2236: the new book alert is its own toggle, off for a new webhook, and
// saved with the rest of the triggers.
describe('NotificationsTab new book toggle', () => {
  it('is off for a new webhook and is sent when turned on', async () => {
    vi.mocked(api.listNotifications).mockResolvedValue([])
    vi.mocked(api.addNotification).mockResolvedValue(notification({ id: 2, onBookAnnounced: true }))
    render(<NotificationsTab />)

    fireEvent.click(await screen.findByRole('button', { name: 'settings.notifications.addButton' }))
    const toggle = screen.getByRole('button', { name: 'settings.notifications.onBookAnnouncedToggle' })
    expect(toggle).toHaveAttribute('aria-pressed', 'false')

    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
    fireEvent.change(screen.getByPlaceholderText('Webhook URL'), { target: { value: 'https://example.test/new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.addNotification).toHaveBeenCalled())
    expect(vi.mocked(api.addNotification).mock.calls[0][0]).toMatchObject({ onBookAnnounced: true })
  })

  it('shows the badge and keeps the stored value when editing', async () => {
    const stored = notification({ onBookAnnounced: true })
    vi.mocked(api.listNotifications).mockResolvedValue([stored])
    vi.mocked(api.updateNotification).mockResolvedValue({ ...stored, onBookAnnounced: false })
    render(<NotificationsTab />)

    expect(await screen.findByText('settings.notifications.onBookAnnounced')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    const toggle = screen.getByRole('button', { name: 'settings.notifications.onBookAnnouncedToggle' })
    expect(toggle).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(toggle)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.updateNotification).toHaveBeenCalled())
    expect(vi.mocked(api.updateNotification).mock.calls[0][1]).toMatchObject({ onBookAnnounced: false, onGrab: true })
  })
})
