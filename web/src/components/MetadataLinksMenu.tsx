import { useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { MetadataSourceLink } from '../util/metadataSource'

function ExternalLinkIcon({ className = '' }: { className?: string }) {
  return (
    <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} className={className}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M13.5 6H18m0 0v4.5M18 6l-7.5 7.5M15 13.5v3.75A1.75 1.75 0 0 1 13.25 19h-6.5A1.75 1.75 0 0 1 5 17.25v-6.5A1.75 1.75 0 0 1 6.75 9H10.5" />
    </svg>
  )
}

export default function MetadataLinksMenu({ links }: { links: MetadataSourceLink[] }) {
  const { t } = useTranslation()
  const [hovered, setHovered] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelId = useId()
  const open = hovered || expanded
  if (links.length === 0) return null

  return (
    <div
      className="relative inline-flex"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onBlur={event => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setExpanded(false)
      }}
      onKeyDown={event => {
        if (event.key === 'Escape' && open) {
          event.preventDefault()
          setHovered(false)
          setExpanded(false)
          triggerRef.current?.focus()
        }
      }}
    >
      <button
        ref={triggerRef}
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setExpanded(value => !value)}
        className="inline-flex items-center gap-1 rounded border border-slate-300 dark:border-zinc-700 bg-slate-100 dark:bg-zinc-800 px-2 py-1 font-medium text-slate-600 dark:text-zinc-300 hover:border-slate-400 dark:hover:border-zinc-600 hover:text-slate-900 dark:hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500"
      >
        <ExternalLinkIcon className="w-3.5 h-3.5" />
        {t('common.links')}
      </button>
      <div
        id={panelId}
        data-testid="book-links-menu"
        hidden={!open}
        className="absolute left-0 top-full z-20 min-w-40 pt-1"
      >
        <div className="overflow-hidden rounded-md border border-slate-200 dark:border-zinc-700 bg-white dark:bg-zinc-900 py-1 shadow-lg">
          {links.map(link => (
            <a
              key={link.url}
              href={link.url}
              target="_blank"
              rel="noopener noreferrer"
              title={link.label}
              aria-label={t('common.viewOnSource', { source: link.label })}
              className="flex items-center justify-between gap-3 px-3 py-2 text-xs text-slate-700 dark:text-zinc-200 hover:bg-slate-100 dark:hover:bg-zinc-800 focus-visible:outline-none focus-visible:bg-slate-100 dark:focus-visible:bg-zinc-800"
            >
              <span>{link.label}</span>
              <ExternalLinkIcon className="w-3.5 h-3.5 text-slate-400 dark:text-zinc-500" />
            </a>
          ))}
        </div>
      </div>
    </div>
  )
}
