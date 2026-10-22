// PathRemapField is the path-remap textarea used by the ABS and download-client forms.

import { inputCls } from './formStyles'

export default function PathRemapField({ id, label, value, onChange, placeholder, help }: { id: string; label: string; value: string; onChange: (value: string) => void; placeholder: string; help: string }) {
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
        {help} Use comma-separated <code className="text-[10px] bg-slate-200 dark:bg-zinc-800 px-1 rounded">from:to</code> prefix rewrites.
      </p>
    </div>
  )
}
