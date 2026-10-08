import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import CalendarPage from './CalendarPage'
import { api } from '../api/client'
import type { Book } from '../api/client'
import i18n from '../i18n'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, api: { ...actual.api, listAllBooks: vi.fn() } }
})

const release = { id: 1, title: 'Spring Release', releaseDate: '2026-03-05T00:00:00Z', monitored: true } as Book

// Month and weekday names used to be a hardcoded English array, so the
// calendar read "March" and "Mon" in every language.
describe('CalendarPage month names', () => {
  beforeEach(() => {
    // Only Date is faked: timers stay real so findBy* can poll.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-03-15T12:00:00Z'))
    vi.mocked(api.listAllBooks).mockResolvedValue([release])
  })

  afterEach(async () => {
    vi.useRealTimers()
    await act(async () => { await i18n.changeLanguage('en') })
  })

  it('renders English month and weekday names in English', async () => {
    await act(async () => { await i18n.changeLanguage('en') })
    render(<CalendarPage />)
    expect(await screen.findByText('March 2026')).toBeInTheDocument()
    expect(await screen.findByText('Releasing in March 2026')).toBeInTheDocument()
    expect(screen.getByText('Mar 5')).toBeInTheDocument()
    expect(screen.getByText('Mon')).toBeInTheDocument()
  })

  it('follows the active language', async () => {
    await act(async () => { await i18n.changeLanguage('fr') })
    render(<CalendarPage />)
    expect(await screen.findByText('mars 2026')).toBeInTheDocument()
    expect(await screen.findByText('Sorties en mars 2026')).toBeInTheDocument()
    expect(screen.getByText('5 mars')).toBeInTheDocument()
    expect(screen.getByText('lun.')).toBeInTheDocument()
    expect(screen.queryByText('March 2026')).not.toBeInTheDocument()
  })
})

// On a phone the month grid marks a day with releases by a dot and nothing
// else, and the dot could not be tapped. A day with releases is a button now
// that narrows the agenda below the grid to that day.
describe('CalendarPage day cells on a phone', () => {
  const other = { id: 2, title: 'Late March Release', releaseDate: '2026-03-20T00:00:00Z', monitored: true } as Book

  beforeEach(async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-03-15T12:00:00Z'))
    vi.mocked(api.listAllBooks).mockResolvedValue([release, other])
    await act(async () => { await i18n.changeLanguage('en') })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('lists only the tapped day in the agenda, and the whole month again on a second tap', async () => {
    render(<CalendarPage />)
    const day = await screen.findByRole('button', { name: 'Mar 5: 1 release' })
    const agenda = screen.getByTestId('calendar-agenda')
    expect(within(agenda).getByText('Late March Release')).toBeInTheDocument()

    fireEvent.click(day)
    expect(day).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText('Releasing on Mar 5')).toBeInTheDocument()
    expect(within(agenda).getByText('Spring Release')).toBeInTheDocument()
    expect(within(agenda).queryByText('Late March Release')).not.toBeInTheDocument()

    fireEvent.click(day)
    expect(day).toHaveAttribute('aria-pressed', 'false')
    expect(within(agenda).getByText('Late March Release')).toBeInTheDocument()
  })

  it('offers the whole month back from the agenda header', async () => {
    render(<CalendarPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Mar 20: 1 release' }))
    expect(within(screen.getByTestId('calendar-agenda')).queryByText('Spring Release')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show the whole month' }))
    expect(within(screen.getByTestId('calendar-agenda')).getByText('Spring Release')).toBeInTheDocument()
    expect(screen.getByText('Releasing in March 2026')).toBeInTheDocument()
  })

  it('leaves days without releases as plain cells', async () => {
    render(<CalendarPage />)
    await screen.findByRole('button', { name: 'Mar 5: 1 release' })
    expect(screen.queryByRole('button', { name: /^Mar 6/ })).not.toBeInTheDocument()
  })
})
