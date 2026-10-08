import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DuplicateCandidateGroup, DuplicateCandidateMember } from '../api/client'
import { useConfirmDialog } from './useConfirmDialog'

// The actions both duplicate review views share (#1970, #2999): toggling one
// row's exclusion, and excluding a group's empty rows in one confirmed click.
// Both go through the existing exclude routes; nothing here decides anything
// on its own. `reload` re-fetches the caller's groups afterwards, so a group
// that no longer has two rows to compare disappears.
export function useDuplicateReviewActions(reload: () => void, onChanged?: () => void) {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [busyBooks, setBusyBooks] = useState<Set<number>>(() => new Set())
  const [error, setError] = useState<string | null>(null)

  const markBusy = (ids: number[], busy: boolean) => {
    setBusyBooks(prev => {
      const next = new Set(prev)
      ids.forEach(id => (busy ? next.add(id) : next.delete(id)))
      return next
    })
  }

  const toggleExclusion = async (book: DuplicateCandidateMember) => {
    // Excluding a row that has files is allowed, it is just never the
    // suggestion, so it asks first.
    if (!book.excluded && book.hasFiles && !await confirm({
      title: t('duplicateReview.excludeWithFilesTitle', 'Exclude a row that has files?'),
      body: t('duplicateReview.excludeWithFilesBody', {
        title: book.title,
        defaultValue: '"{{title}}" has files in your library. Excluding it hides it from Wanted and stops searches for it; the files stay on disk.',
      }),
      confirmLabel: t('duplicateCandidates.exclude', 'Exclude'),
    })) return
    markBusy([book.id], true)
    setError(null)
    try {
      await api.toggleExcluded(book.id)
      reload()
      onChanged?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('duplicateCandidates.toggleFailed', 'Changing the exclusion failed'))
    } finally {
      markBusy([book.id], false)
    }
  }

  const excludeEmptyRows = async (group: DuplicateCandidateGroup) => {
    // The server only suggests rows without files; filter again here so a
    // row with files can never ride along, whatever the payload says.
    const rows = group.books.filter(b =>
      (group.suggestedExcludeIds ?? []).includes(b.id) && !b.hasFiles && !b.excluded && b.id !== group.keeperId)
    if (rows.length === 0) return
    const keeper = group.books.find(b => b.id === group.keeperId)
    if (!await confirm({
      title: t('duplicateReview.excludeEmptyTitle', { count: rows.length, defaultValue: 'Exclude {{count}} empty row(s)?' }),
      body: t('duplicateReview.excludeEmptyBody', {
        keep: keeper?.title ?? '',
        rows: rows.map(r => `• ${r.title}`).join('\n'),
        defaultValue: 'Keep "{{keep}}", which has files, and exclude:\n{{rows}}\n\nExcluded rows leave Wanted and are no longer searched. You can include them again later.',
      }),
      confirmLabel: t('duplicateCandidates.exclude', 'Exclude'),
    })) return
    const ids = rows.map(r => r.id)
    markBusy(ids, true)
    setError(null)
    try {
      const res = await api.excludeEmptyBooks(ids)
      const results = Object.values(res.results ?? {})
      const skipped = results.filter(r => !r.ok && r.code === 'has_files').length
      const failed = results.filter(r => !r.ok && r.code !== 'has_files').length
      const messages: string[] = []
      if (skipped > 0) {
        messages.push(t('duplicateReview.excludeEmptySkipped', {
          count: skipped,
          defaultValue: '{{count}} row(s) were skipped because they have files now. Review the group again.',
        }))
      }
      if (failed > 0) {
        messages.push(t('duplicateReview.excludeEmptyPartial', {
          count: failed,
          defaultValue: '{{count}} row(s) could not be excluded',
        }))
      }
      if (messages.length > 0) setError(messages.join(' '))
      reload()
      onChanged?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('duplicateCandidates.toggleFailed', 'Changing the exclusion failed'))
    } finally {
      markBusy(ids, false)
    }
  }

  return { busyBooks, error, setError, toggleExclusion, excludeEmptyRows, confirmDialog }
}
