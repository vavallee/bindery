// A quiet one line subtitle that shows what a Settings control currently holds
// (#3989), so state can be read without opening the control. One component for
// every section: callers pass the value the control itself reads, never a copy,
// so the subtitle moves with the control and reverts with it when a save fails
// (useAutoSave puts the control state back, and this follows).
//
// Rules the component enforces so call sites cannot forget them:
//   - an empty value reads "Not set", or "Default (x)" when the caller names
//     the effective default;
//   - a secret is never rendered: pass secret and only "Set" or "Not set"
//     shows, whatever the value is;
//   - one line, long values (root folder paths) are cut with an ellipsis and
//     the full text is in the title attribute;
//   - userinfo in a URL (user:pass@host) is never shown, and such a value gets
//     no title, since the title would carry the same text.
// The text is plain, static, not a live region. The control points at it with
// aria-describedby (describedBy(id)), so it is read with the control once and
// is not announced on its own. Colour is not the signal: the words are.

import { useTranslation } from 'react-i18next'
import { fieldValueId } from './fieldValueId'
import { redactUrlUserinfo } from './redactUrl'

interface Props {
  /** Same string as the control's id (or group id); the subtitle id derives from it. */
  for: string
  /** What the control holds, already translated for display. Empty means unset. */
  value?: string | null
  /** The effective default shown as "Default (x)" when value is empty. */
  defaultValue?: string
  /** Show only Set or Not set. Use for API keys, tokens, passwords. */
  secret?: boolean
  /** No bottom margin, for a subtitle that sits above a hint line. */
  flush?: boolean
}

export default function FieldValue({ for: base, value, defaultValue, secret = false, flush = false }: Props) {
  const { t } = useTranslation()
  const has = !!value && value.trim() !== ''
  let text: string
  let redacted = false
  if (secret) {
    text = has ? t('settings.currentValue.set', 'Set') : t('settings.currentValue.notSet', 'Not set')
  } else if (has) {
    const r = redactUrlUserinfo(value as string)
    text = r.text
    redacted = r.redacted
  } else if (defaultValue) {
    text = t('settings.currentValue.default', 'Default ({{value}})', { value: defaultValue })
  } else {
    text = t('settings.currentValue.notSet', 'Not set')
  }
  const full = t('settings.currentValue.current', 'Current: {{value}}', { value: text })
  return (
    <p
      id={fieldValueId(base)}
      data-testid="field-value"
      title={redacted ? undefined : full}
      className={`block max-w-full truncate text-xs text-fg-muted ${flush ? 'mb-0' : 'mb-1'}`}
    >
      {full}
    </p>
  )
}
