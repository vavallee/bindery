import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import i18n, { i18nReady } from '../i18n'
import BookRating from './BookRating'

beforeAll(async () => {
  await i18nReady
})

afterEach(async () => {
  await i18n.changeLanguage('en')
})

describe('BookRating', () => {
  it('renders nothing for an unrated book', () => {
    const zero = render(<BookRating book={{ averageRating: 0, ratingsCount: 12 }} />)
    expect(zero.container).toBeEmptyDOMElement()
    const missing = render(<BookRating book={{}} />)
    expect(missing.container).toBeEmptyDOMElement()
  })

  it('shows the rating alone when there is no count', () => {
    const { container } = render(<BookRating book={{ averageRating: 4.2 }} withCount />)
    expect(container).toHaveTextContent(/^★ 4\.20$/)
    expect(container.querySelector('span')).not.toHaveAttribute('title')
  })

  it('uses the singular for a single rating', () => {
    const { container } = render(<BookRating book={{ averageRating: 5, ratingsCount: 1 }} withCount />)
    expect(container).toHaveTextContent(/^★ 5\.00 \(1 rating\)$/)
  })

  it('puts the count in the tooltip unless withCount is set', () => {
    const { container } = render(<BookRating book={{ averageRating: 4.21, ratingsCount: 1234 }} />)
    expect(container).toHaveTextContent(/^★ 4\.21$/)
    expect(container.querySelector('span')).toHaveAttribute('title', '1,234 ratings')
  })

  it('formats both numbers for the UI language', async () => {
    await i18n.changeLanguage('de')
    const { container } = render(<BookRating book={{ averageRating: 4.21, ratingsCount: 1234 }} withCount />)
    expect(container).toHaveTextContent('★ 4,21')
    expect(container).toHaveTextContent('1.234')
  })
})
