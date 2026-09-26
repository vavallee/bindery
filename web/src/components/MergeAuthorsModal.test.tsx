import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import MergeAuthorsModal from './MergeAuthorsModal'
import type { Author } from '../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

function makeAuthor(id: number, authorName: string, sortName: string): Author {
  return {
    id,
    foreignAuthorId: `OL${id}A`,
    authorName,
    sortName,
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
  }
}

describe('MergeAuthorsModal (#2805)', () => {
  it('lists authors in last name order, matching the Authors page', () => {
    render(
      <MergeAuthorsModal
        authors={[
          makeAuthor(1, 'Vincent van Gogh', 'Gogh, Vincent van'),
          makeAuthor(2, 'Ada Zed', 'Zed, Ada'),
          makeAuthor(3, 'Zoe Adams', 'Adams, Zoe'),
        ]}
        onClose={() => {}}
        onMerged={() => {}}
      />,
    )
    const [removeSelect] = screen.getAllByRole('combobox')
    const names = Array.from(removeSelect.querySelectorAll('option'))
      .slice(1)
      .map(o => o.textContent)
    expect(names).toEqual(['Zoe Adams', 'Vincent van Gogh', 'Ada Zed'])
  })
})
