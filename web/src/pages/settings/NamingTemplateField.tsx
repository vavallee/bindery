// NamingTemplateField renders a file-naming template input with a token picker,
// a live rendered preview, and inline validation. Used in the File Naming
// section of GeneralTab for both the ebook and audiobook templates.
//
// The preview + validation are computed client-side via namingTemplate.ts,
// which mirrors the Go renamer (internal/importer/renamer.go) so the preview
// matches what the importer actually writes.

import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { inputCls, labelCls } from './formStyles'
import {
  NAMING_TOKENS,
  TemplateKind,
  renderTemplate,
  validateTemplate,
} from './namingTemplate'

export interface NamingTemplateFieldProps {
  label: string
  hint?: string
  kind: TemplateKind
  placeholder: string
  value: string
  onChange: (value: string) => void
  onSave: () => void
  saving: boolean
}

export default function NamingTemplateField({
  label,
  hint,
  kind,
  placeholder,
  value,
  onChange,
  onSave,
  saving,
}: NamingTemplateFieldProps) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)

  const validation = validateTemplate(value)
  const preview = renderTemplate(value, kind)
  const hasErrors =
    validation.empty || validation.traversal || validation.unknownTokens.length > 0

  // Insert a token at the caret (or replace the current selection). Falls back
  // to appending when the input is not focused.
  function insertToken(token: string) {
    const el = inputRef.current
    if (!el) {
      onChange(value + token)
      return
    }
    const start = el.selectionStart ?? value.length
    const end = el.selectionEnd ?? value.length
    const next = value.slice(0, start) + token + value.slice(end)
    onChange(next)
    // Restore focus and place the caret after the inserted token.
    requestAnimationFrame(() => {
      el.focus()
      const caret = start + token.length
      el.setSelectionRange(caret, caret)
    })
  }

  return (
    <div>
      <label className={labelCls}>{label}</label>
      {hint && <p className="text-xs text-slate-600 dark:text-zinc-500 mb-2">{hint}</p>}

      {/* Token picker */}
      <div className="flex flex-wrap gap-1.5 mb-2" role="group" aria-label={t('settings.general.naming.tokenPickerLabel')}>
        {NAMING_TOKENS.map(tok => {
          const greyed = tok.ebookOnly && kind === 'audiobook'
          return (
            <button
              key={tok.token}
              type="button"
              onClick={() => !greyed && insertToken(tok.token)}
              disabled={greyed}
              title={
                greyed
                  ? t('settings.general.naming.tokenIgnoredAudiobook', { token: tok.token })
                  : t(`settings.general.naming.${tok.descKey}`)
              }
              className={
                greyed
                  ? 'px-2 py-0.5 rounded text-[11px] font-mono border border-slate-200 dark:border-zinc-800 text-slate-400 dark:text-zinc-600 cursor-not-allowed line-through'
                  : 'px-2 py-0.5 rounded text-[11px] font-mono border border-slate-300 dark:border-zinc-700 text-slate-700 dark:text-zinc-300 hover:bg-emerald-600 hover:border-emerald-600 hover:text-white dark:hover:text-white transition-colors'
              }
            >
              {tok.token}
            </button>
          )
        })}
      </div>

      <div className="flex gap-2">
        <input
          ref={inputRef}
          value={value}
          onChange={e => onChange(e.target.value)}
          placeholder={placeholder}
          aria-invalid={hasErrors}
          className={inputCls + ' flex-1 font-mono'}
        />
        <button
          type="button"
          onClick={onSave}
          disabled={saving || hasErrors}
          className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium disabled:opacity-50"
        >
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>

      {/* Live preview */}
      {!validation.empty && (
        <p className="text-xs text-slate-600 dark:text-zinc-400 mt-1.5">
          <span className="text-slate-500 dark:text-zinc-500">
            {t('settings.general.naming.previewLabel')}
          </span>{' '}
          <code
            data-testid={`naming-preview-${kind}`}
            className="font-mono text-[11px] bg-slate-200 dark:bg-zinc-800 text-slate-800 dark:text-zinc-200 px-1.5 py-0.5 rounded break-all"
          >
            {preview}
          </code>
        </p>
      )}

      {/* Validation feedback */}
      {validation.empty && (
        // role="alert" so screen readers announce the still-blocking empty
        // state (Save stays disabled). Kept visually muted — it's a "required"
        // prompt, not a hard error like traversal — but still announced.
        <p className="text-xs text-slate-500 dark:text-zinc-500 mt-1.5" role="alert">
          {t('settings.general.naming.hintEmpty', 'Required — enter at least one token or path segment.')}
        </p>
      )}
      {validation.traversal && (
        <p className="text-xs text-red-600 dark:text-red-400 mt-1.5" role="alert">
          {t('settings.general.naming.errorTraversal')}
        </p>
      )}
      {validation.unknownTokens.length > 0 && (
        <p className="text-xs text-amber-600 dark:text-amber-400 mt-1.5" role="alert">
          {t('settings.general.naming.errorUnknownTokens', {
            tokens: validation.unknownTokens.join(', '),
          })}
        </p>
      )}
    </div>
  )
}
