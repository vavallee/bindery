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
}

beforeEach(() => {
  vi.resetAllMocks()
  mocked.audibleSyncConfig.mockResolvedValue(saved)
})

describe('AudibleSyncTab', () => {
  it('leaves a blank token out of a save, so the stored one is kept', async () => {
    mocked.audibleSyncSetConfig.mockResolvedValue(saved)
    render(<AudibleSyncTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Save' }))
    await waitFor(() => expect(mocked.audibleSyncSetConfig).toHaveBeenCalled())
    const payload = mocked.audibleSyncSetConfig.mock.calls[0][0]
    expect(payload).toEqual({ enabled: true, baseUrl: 'http://audible-sync:8080' })
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
