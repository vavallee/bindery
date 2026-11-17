import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import RequestConfirm from './RequestConfirm'
import en from '../i18n/locales/en.json'

// The signed in account's auto approve value is not sent to the web outside
// the admin Users API, so the hint has to be true for a requester whose
// request waits for an admin and for one whose account approves it at once.

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key.split('.').reduce<unknown>((n, p) => (n as Record<string, unknown>)?.[p], en) as string ?? key,
  }),
}))
vi.mock('../api/client', () => ({ api: { createRequest: vi.fn() } }))

describe('request dialog hint', () => {
  it.each(['book', 'author'] as const)('covers both outcomes for a %s request', kind => {
    render(<RequestConfirm kind={kind} foreignId="x" title="T" onBack={vi.fn()} onClose={vi.fn()} onRequested={vi.fn()} />)
    const hint = screen.getByText(/You can follow it under My requests/)
    expect(hint).toHaveTextContent('An admin approves your request, or your account approves it on its own if auto approve is on.')
    expect(hint).not.toHaveTextContent('if they agree')
  })
})
