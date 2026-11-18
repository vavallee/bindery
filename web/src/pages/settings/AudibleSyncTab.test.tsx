import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

vi.mock('../../api/client', () => ({
  api: {
    audibleSyncConfig: vi.fn(),
    audibleSyncSetConfig: vi.fn(),
    audibleSyncTest: vi.fn(),
    audibleSyncAccounts: vi.fn(),
  },
}))

import { api, AudibleSyncConfig } from '../../api/client'
import AudibleSyncTab from './AudibleSyncTab'
import '../../i18n'

const mocked = api as unknown as Record<keyof typeof api, ReturnType<typeof vi.fn>>

const saved: AudibleSyncConfig = {
  enabled: true,
  baseUrl: 'http://audible-sync:8080',
  apiTokenConfigured: true,
  accountId: 0,
}

beforeEach(() => {
  vi.resetAllMocks()
  mocked.audibleSyncConfig.mockResolvedValue(saved)
  mocked.audibleSyncAccounts.mockResolvedValue([
    { id: 1, name: 'Main', countryCode: 'uk', enabled: true, needsLogin: false },
    { id: 2, name: 'Spare', countryCode: 'us', enabled: true, needsLogin: true },
  ])
})

describe('AudibleSyncTab', () => {
  it('fills the account picker from the saved connection', async () => {
    render(<AudibleSyncTab />)
    expect(await screen.findByRole('option', { name: 'Main (UK)' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Spare (US), needs signing in again' })).toBeInTheDocument()
  })

  it('saves the account and leaves a blank token out, so the stored one is kept', async () => {
    mocked.audibleSyncSetConfig.mockResolvedValue({ ...saved, accountId: 1 })
    render(<AudibleSyncTab />)
    await screen.findByRole('option', { name: 'Main (UK)' })
    fireEvent.change(screen.getByLabelText('Audible account'), { target: { value: '1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(mocked.audibleSyncSetConfig).toHaveBeenCalled())
    const payload = mocked.audibleSyncSetConfig.mock.calls[0][0]
    expect(payload).toEqual({ enabled: true, baseUrl: 'http://audible-sync:8080', accountId: 1 })
    expect(payload).not.toHaveProperty('apiToken')
  })

  it('tests with a typed token, and shows the answer', async () => {
    mocked.audibleSyncTest.mockResolvedValue({ ok: false, message: 'Audible Sync answered, but rejected the API token.' })
    render(<AudibleSyncTab />)
    const token = await screen.findByLabelText(/API token/)
    fireEvent.change(token, { target: { value: 'fresh' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test Connection' }))
    expect(await screen.findByText('Audible Sync answered, but rejected the API token.')).toHaveClass('text-red-700')
    expect(mocked.audibleSyncTest).toHaveBeenCalledWith({ apiToken: 'fresh' })
  })

  it('will not test a URL that has not been saved', async () => {
    render(<AudibleSyncTab />)
    const url = await screen.findByLabelText('Server URL')
    fireEvent.change(url, { target: { value: 'http://elsewhere:8080' } })
    expect(screen.getByRole('button', { name: 'Test Connection' })).toBeDisabled()
    expect(screen.getByText('Save the URL to test it.')).toBeInTheDocument()
  })
})
