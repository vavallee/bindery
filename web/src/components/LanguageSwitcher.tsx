import { useTranslation } from 'react-i18next'

const LANGUAGES = [
  { code: 'system', labelKey: 'settings.general.languageSystem' },
  { code: 'en', label: 'English' },
  { code: 'fr', label: 'Français' },
  { code: 'de', label: 'Deutsch' },
  { code: 'es', label: 'Español' },
  { code: 'nl', label: 'Nederlands' },
]

export default function LanguageSwitcher() {
  const { i18n, t } = useTranslation()

  // 'system' means we clear the localStorage key and let the detector use navigator.language
  const currentStored = localStorage.getItem('bindery.lang')
  const current = currentStored ?? 'system'

  const handleChange = (code: string) => {
    if (code === 'system') {
      localStorage.removeItem('bindery.lang')
      // Reload so the detector re-reads navigator.language
      window.location.reload()
    } else {
      i18n.changeLanguage(code)
    }
  }

  return (
    <select
      value={current}
      onChange={e => handleChange(e.target.value)}
      className="bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
    >
      {LANGUAGES.map(l => (
        <option key={l.code} value={l.code}>{l.labelKey ? t(l.labelKey) : l.label}</option>
      ))}
    </select>
  )
}
