import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ThemeToggle from './ThemeToggle'

const theme = { value: 'light' as 'light' | 'dark', setTheme: vi.fn() }
vi.mock('../theme', () => ({ useTheme: () => ({ theme: theme.value, setTheme: theme.setTheme }) }))
// Strings come from en.json, so a missing or renamed key fails here.
vi.mock('react-i18next', async () => {
  const en = (await import('../i18n/locales/en.json')).default as Record<string, unknown>
  const lookup = (key: string): unknown =>
    key.split('.').reduce<unknown>((n, p) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[p] : undefined), en)
  return { useTranslation: () => ({ t: (k: string) => { const v = lookup(k); return typeof v === 'string' ? v : k } }) }
})

// The switch alone showed a sun and a moon with no word for which was on.
describe('ThemeToggle', () => {
  it('writes the current theme beside the switch', () => {
    theme.value = 'light'
    const { rerender } = render(<ThemeToggle />)
    expect(screen.getByText('Light')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button'))
    expect(theme.setTheme).toHaveBeenCalledWith('dark')
    theme.value = 'dark'
    rerender(<ThemeToggle />)
    expect(screen.getByText('Dark')).toBeInTheDocument()
  })
})
