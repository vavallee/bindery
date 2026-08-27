import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, CatalogueReconciliation } from '../api/client'
import CatalogueReconciliationModal from './CatalogueReconciliationModal'

vi.mock('react-i18next', () => {
  const t = (key: string, fallback?: string | Record<string, unknown>) => {
      if (typeof fallback === 'string') return fallback
      const template = String(fallback?.defaultValue ?? key)
      return Object.entries(fallback ?? {}).reduce(
        (text, [name, value]) => name === 'defaultValue' ? text : text.replaceAll(`{{${name}}}`, String(value)),
        template,
      )
  }
  return { useTranslation: () => ({ t }) }
})

vi.mock('../api/client', () => ({
  api: {
    previewAuthorCatalogueReconciliation: vi.fn(),
    applyAuthorCatalogueReconciliation: vi.fn(),
  },
}))

const preview: CatalogueReconciliation = {
  authorId: 7,
  authorName: 'Test Author',
  provider: 'hardcover',
  providerComplete: true,
  profileName: 'English only',
  candidates: [
    { bookId: 11, title: 'Libro', metadataProvider: 'hardcover', reason: 'language_not_allowed' },
    { bookId: 12, title: 'Old Work', metadataProvider: 'openlibrary', reason: 'provider_changed' },
  ],
  summary: {
    total: 8,
    candidates: 2,
    kept: 3,
    protected: 3,
    protectedFiles: 2,
    protectedImported: 1,
    protectedStatus: 0,
    protectedExcluded: 0,
    indeterminate: 1,
    reasons: { language_not_allowed: 1, provider_changed: 1 },
  },
}

describe('CatalogueReconciliationModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.previewAuthorCatalogueReconciliation).mockResolvedValue(preview)
  })

  it('previews candidates and explains protected rows before apply', async () => {
    render(<CatalogueReconciliationModal authorId={7} authorName="Test Author" onClose={() => {}} />)

    await waitFor(() => expect(screen.getByText('Libro')).toBeInTheDocument())
    expect(api.previewAuthorCatalogueReconciliation).toHaveBeenCalledWith(7)
    expect(screen.getByText('Old Work')).toBeInTheDocument()
    expect(screen.getByText('Provider: hardcover · Profile: English only')).toBeInTheDocument()
    expect(screen.getByText(/2 file-bearing, 1 imported, 0 other-status, and 0 excluded/)).toBeInTheDocument()
    expect(screen.getByText('1 row(s) were kept because provider or profile evidence was incomplete.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove 2 stale row(s)' })).toBeInTheDocument()
  })

  it('applies exactly the previewed IDs and reports server-side rechecks', async () => {
    const onApplied = vi.fn()
    vi.mocked(api.applyAuthorCatalogueReconciliation).mockResolvedValue({
      ...preview,
      applied: { requested: 2, deleted: 1, skipped: 1 },
    })
    render(<CatalogueReconciliationModal authorId={7} authorName="Test Author" onClose={() => {}} onApplied={onApplied} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Remove 2 stale row(s)' }))

    await waitFor(() => {
      expect(api.applyAuthorCatalogueReconciliation).toHaveBeenCalledWith(7, [11, 12])
      expect(onApplied).toHaveBeenCalledTimes(1)
    })
    expect(screen.getByText('Removed 1 row(s). 1 were skipped because they were no longer eligible.')).toBeInTheDocument()
    expect(onApplied).toHaveBeenCalledTimes(1)
  })

  it('applies only the rows selected by the user', async () => {
    vi.mocked(api.applyAuthorCatalogueReconciliation).mockResolvedValue({
      ...preview,
      applied: { requested: 1, deleted: 1, skipped: 0 },
    })
    render(<CatalogueReconciliationModal authorId={7} authorName="Test Author" onClose={() => {}} />)

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Select Old Work' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove 1 stale row(s)' }))

    await waitFor(() => {
      expect(api.applyAuthorCatalogueReconciliation).toHaveBeenCalledWith(7, [11])
    })
  })

  it('disables apply when every candidate is deselected', async () => {
    render(<CatalogueReconciliationModal authorId={7} authorName="Test Author" onClose={() => {}} />)

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Select Libro' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select Old Work' }))

    expect(screen.getByRole('button', { name: 'Remove 0 stale row(s)' })).toBeDisabled()
  })

  it('does not offer apply when the preview is clean', async () => {
    vi.mocked(api.previewAuthorCatalogueReconciliation).mockResolvedValue({
      ...preview,
      candidates: [],
      summary: { ...preview.summary, candidates: 0 },
    })
    render(<CatalogueReconciliationModal authorId={7} authorName="Test Author" onClose={() => {}} />)

    expect(await screen.findByText('No metadata-only Wanted rows need reconciliation.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Remove .* stale/ })).toBeDisabled()
  })
})
