// PathRemapField is the path-remap textarea used by the ABS and download-client forms.

import { useTranslation } from 'react-i18next'
import { inputCls } from './formStyles'
import RichText from '../../components/RichText'

export default function PathRemapField({ id, label, value, onChange, placeholder, help }: { id: string; label: string; value: string; onChange: (value: string) => void; placeholder: string; help: string }) {
  const { t } = useTranslation()
  return (
    <div>
      <label htmlFor={id} className="block text-xs text-fg-muted mb-1">{label}</label>
      <textarea
        id={id}
        value={value}
        onChange={e => onChange(e.target.value)}
        rows={3}
        placeholder={placeholder}
        className={inputCls}
      />
      <p className="text-[11px] text-fg-muted mt-1">
        {help} <RichText text={t('settings.pathRemap.syntaxHint', 'Use comma-separated <code>from:to</code> prefix rewrites.')} components={{ code: <code className="text-[10px] bg-slate-200 dark:bg-zinc-800 px-1 rounded" /> }} />
      </p>
    </div>
  )
}
