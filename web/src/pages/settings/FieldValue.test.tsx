import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import FieldValue from './FieldValue'
import { describedBy, fieldValueId } from './fieldValueId'
import { expectFieldValuesWired } from '../../test/fieldValue'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
  }),
}))

describe('FieldValue (#3989)', () => {
  it('shows the value it is given', () => {
    render(<FieldValue for="a" value="Hardlink" />)
    expect(screen.getByTestId('field-value')).toHaveTextContent('Current: Hardlink')
  })

  it('says Not set for an empty value', () => {
    render(<FieldValue for="a" value="" />)
    expect(screen.getByTestId('field-value')).toHaveTextContent('Current: Not set')
  })

  it('says Default (x) for an empty value when a default is named', () => {
    render(<FieldValue for="a" value={undefined} defaultValue="12 hours" />)
    expect(screen.getByTestId('field-value')).toHaveTextContent('Current: Default (12 hours)')
  })

  it('never renders a secret, only Set or Not set', () => {
    const { rerender } = render(<FieldValue for="a" value="sk-super-secret" secret />)
    expect(screen.getByTestId('field-value')).toHaveTextContent('Current: Set')
    expect(document.body.innerHTML).not.toContain('sk-super-secret')
    rerender(<FieldValue for="a" value="" secret />)
    expect(screen.getByTestId('field-value')).toHaveTextContent('Current: Not set')
  })

  it('keeps a long path to one line and puts the whole path in the title', () => {
    const path = '/mnt/storage/media/library/books/very/deeply/nested/folder/structure/ebooks'
    render(<FieldValue for="a" value={path} />)
    const el = screen.getByTestId('field-value')
    expect(el).toHaveClass('truncate')
    expect(el).toHaveAttribute('title', `Current: ${path}`)
  })

  it('never shows userinfo from a URL, drops its query and gives it no title', () => {
    render(<FieldValue for="a" value="https://user:pa55@host.example:8080/api/v1?token=abc#frag" />)
    const el = screen.getByTestId('field-value')
    expect(el).toHaveTextContent('Current: https://host.example:8080/api/v1')
    expect(el.textContent).not.toMatch(/pa55|user|token/)
    expect(el).not.toHaveAttribute('title')
    expect(document.body.innerHTML).not.toContain('pa55')
  })

  it('shows Set for credentials it cannot show a host for', () => {
    render(<FieldValue for="a" value="http://u:p@" />)
    expect(screen.getByTestId('field-value')).toHaveTextContent(/^Current: Set$/)
    expect(screen.getByTestId('field-value')).not.toHaveAttribute('title')
  })

  it('drops a query token even with no userinfo', () => {
    render(<FieldValue for="a" value="http://h/a?token=abc" />)
    expect(screen.getByTestId('field-value')).toHaveTextContent('Current: http://h/a')
    expect(document.body.innerHTML).not.toContain('token')
  })

  it('keeps a plain URL and its title', () => {
    render(<FieldValue for="a" value="http://calibre:8099/x" />)
    expect(screen.getByTestId('field-value')).toHaveAttribute('title', 'Current: http://calibre:8099/x')
  })

  it('the wiring check fails when a control points at a neighbouring field line', () => {
    render(<><div><FieldValue for="a" value="1" /><input aria-label="a" /></div><div><FieldValue for="b" value="2" /><input aria-label="b" aria-describedby="a-value" /></div></>)
    expect(() => expectFieldValuesWired(document.body)).toThrow()
  })

  it('derives the id the control points at', () => {
    render(<><FieldValue for="x" value="1" /><input aria-label="x" aria-describedby={describedBy('x')} /></>)
    expect(screen.getByTestId('field-value').id).toBe(fieldValueId('x'))
    expect(screen.getByLabelText('x')).toHaveAccessibleDescription('Current: 1')
    expect(expectFieldValuesWired(document.body)).toEqual({ 'x-value': 'Current: 1' })
  })

  it('appends to an existing description', () => {
    expect(describedBy('x', 'hint')).toBe('hint x-value')
  })

  it('the wiring check fails when no control is described by the subtitle', () => {
    render(<FieldValue for="orphan" value="1" />)
    expect(() => expectFieldValuesWired(document.body)).toThrow()
  })
})
