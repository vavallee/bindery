import { FormEvent, ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useModal } from './useModal'
import { primaryFill } from './buttons'

interface Props {
  title: string
  initialName?: string
  submitLabel: string
  onClose: () => void
  onSubmit: (name: string) => Promise<void> | void
  // Rendered above the name field, for a way to pick a series instead of typing one.
  beforeName?: ReactNode
}

export default function SeriesNameModal({ title, initialName = '', submitLabel, onClose, onSubmit, beforeName }: Props) {
  const { t } = useTranslation()
  const [name, setName] = useState(initialName)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      setError(t('series.nameModal.required'))
      return
    }
    setSaving(true)
    setError(null)
    try {
      await onSubmit(trimmed)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('series.nameModal.failed'))
    } finally {
      setSaving(false)
    }
  }

  const { titleId, panelProps } = useModal({ onClose, canClose: !saving })

  return (
    <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div
        {...panelProps}
        className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-md shadow-2xl modal-max-h overflow-y-auto"
        onClick={e => e.stopPropagation()}
      >
        {/* display: contents keeps the form out of the layout, so the panel
            styles still apply to its children; the dialog role sits on the
            div, since a form may not take it. */}
        <form onSubmit={submit} className="contents">
          <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
            <h3 id={titleId} className="text-lg font-semibold">{title}</h3>
          </div>
          <div className="p-4 space-y-3">
            {beforeName}
            <label className="block text-sm font-medium text-slate-700 dark:text-zinc-300" htmlFor="series-name">
              {t('series.nameModal.name')}
            </label>
            <input
              id="series-name"
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              placeholder={t('series.nameModal.placeholder')}
              className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm"
              autoFocus
            />
            {error && <p className="text-sm text-red-700 dark:text-red-400">{error}</p>}
          </div>
          <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={saving || !name.trim()}
              className={`px-4 py-2 ${primaryFill} disabled:opacity-50 rounded-md text-sm font-medium`}
            >
              {saving ? t('common.saving') : submitLabel}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
