import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DownloadClient, Indexer, ProwlarrInstance } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import GeneralTab from './settings/GeneralTab'
import IndexersTab from './settings/IndexersTab'
import ClientsTab from './settings/ClientsTab'
import NotificationsTab from './settings/NotificationsTab'
import QualityTab from './settings/QualityTab'
import MetadataTab from './settings/MetadataTab'
import RootFoldersTab from './settings/RootFoldersTab'
import CalibreTab from './settings/CalibreTab'
import ABSTab from './settings/ABSTab'
import GrimmoryTab from './settings/GrimmoryTab'
import ImportTab from './settings/ImportTab'
import BlocklistTab from './settings/BlocklistTab'
import LogsTab from './settings/LogsTab'

// Decomposed from the former ~5100-line SettingsPage monolith (#547): each tab
// now lives in its own file under ./settings/. Tabs are eager-imported rather
// than React.lazy code-split — the existing SettingsPage tests open a tab and
// query its content synchronously, which a Suspense boundary breaks. Eager
// imports keep behaviour identical to the pre-refactor monolith.
//
// Cross-tab state: the monolith fetched indexers, download clients, and
// Prowlarr instances eagerly on page mount (not on tab select). That fetch
// timing is preserved by owning those three lists here and passing them to the
// Indexers and Clients tabs. Everything else is genuinely tab-local and lives
// inside its own tab component.

type Tab = 'indexers' | 'clients' | 'notifications' | 'quality' | 'metadata' | 'general' | 'import' | 'rootfolders' | 'logs' | 'blocklist' | 'calibre' | 'abs' | 'grimmory'

const ADMIN_TABS: Tab[] = ['indexers', 'clients', 'notifications', 'quality', 'metadata', 'import', 'rootfolders', 'logs', 'blocklist', 'calibre', 'abs', 'grimmory']

const ALL_TABS: Tab[] = ['general', ...ADMIN_TABS]

// Allow deep-linking to a specific tab via ?tab=indexers (used by first-run
// onboarding guidance on the Authors/Books empty states). Read from the URL
// directly rather than via a router hook so SettingsPage stays renderable
// without a Router context (its tests render it bare).
function initialTabFromUrl(): Tab {
  try {
    const param = new URLSearchParams(window.location.search).get('tab')
    if (param && (ALL_TABS as string[]).includes(param)) return param as Tab
  } catch { /* ignore — fall back to general */ }
  return 'general'
}

function SettingsNavLink({ tab, active, onSelect, label }: { tab: Tab; active: Tab; onSelect: (t: Tab) => void; label: string }) {
  return (
    <button
      onClick={() => onSelect(tab)}
      className={`w-full text-left px-3 py-1.5 rounded-md text-sm transition-colors ${
        active === tab
          ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white font-medium'
          : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-zinc-800/50'
      }`}
    >
      {label}
    </button>
  )
}

export default function SettingsPage() {
  const { t } = useTranslation()
  const { isAdmin } = useAuth()
  const [tab, setTab] = useState<Tab>(initialTabFromUrl)

  // Eagerly fetched on page mount (cross-tab — see file header note).
  const [indexers, setIndexers] = useState<Indexer[]>([])
  const [clients, setClients] = useState<DownloadClient[]>([])
  const [prowlarrInstances, setProwlarrInstances] = useState<ProwlarrInstance[]>([])

  useEffect(() => {
    api.listIndexers().then(setIndexers).catch(console.error)
    api.listDownloadClients().then(setClients).catch(console.error)
    api.listProwlarr().then(r => setProwlarrInstances(r ?? [])).catch(console.error)
  }, [])

  useEffect(() => {
    document.title = 'Settings · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const renderTab = () => {
    switch (tab) {
      case 'general': return <GeneralTab />
      case 'indexers': return <IndexersTab indexers={indexers} setIndexers={setIndexers} prowlarrInstances={prowlarrInstances} setProwlarrInstances={setProwlarrInstances} />
      case 'clients': return <ClientsTab clients={clients} setClients={setClients} />
      case 'notifications': return <NotificationsTab />
      case 'quality': return <QualityTab />
      case 'metadata': return <MetadataTab />
      case 'rootfolders': return <RootFoldersTab />
      case 'calibre': return <CalibreTab />
      case 'abs': return <ABSTab />
      case 'grimmory': return <GrimmoryTab />
      case 'import': return <ImportTab />
      case 'blocklist': return <BlocklistTab />
      case 'logs': return <LogsTab />
    }
  }

  // Redirect non-admins back to the general tab if they somehow navigate to an
  // admin-only tab (e.g. via direct link or stale state).
  useEffect(() => {
    if (!isAdmin && ADMIN_TABS.includes(tab)) {
      setTab('general')
    }
  }, [isAdmin, tab])

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">{t('settings.title')}</h2>

      <div className="flex gap-8 items-start">
        {/* Sidebar navigation */}
        <nav className="w-44 flex-shrink-0 space-y-0.5">
          <SettingsNavLink tab="general" active={tab} onSelect={setTab} label={t('settings.tabs.general')} />

          {isAdmin && (
            <>
              <div className="pt-4 pb-0.5">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 dark:text-zinc-600 px-3 mb-1">Sources</p>
                <SettingsNavLink tab="indexers" active={tab} onSelect={setTab} label={t('settings.tabs.indexers')} />
                <SettingsNavLink tab="clients" active={tab} onSelect={setTab} label={t('settings.tabs.clients')} />
                <SettingsNavLink tab="notifications" active={tab} onSelect={setTab} label={t('settings.tabs.notifications')} />
              </div>

              <div className="pt-3 pb-0.5">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 dark:text-zinc-600 px-3 mb-1">Library</p>
                <SettingsNavLink tab="quality" active={tab} onSelect={setTab} label={t('settings.tabs.quality')} />
                <SettingsNavLink tab="metadata" active={tab} onSelect={setTab} label={t('settings.tabs.metadata')} />
                <SettingsNavLink tab="rootfolders" active={tab} onSelect={setTab} label={t('settings.tabs.rootfolders')} />
              </div>

              <div className="pt-3 pb-0.5">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 dark:text-zinc-600 px-3 mb-1">Integrations</p>
                <SettingsNavLink tab="calibre" active={tab} onSelect={setTab} label={t('settings.tabs.calibre')} />
                <SettingsNavLink tab="abs" active={tab} onSelect={setTab} label={t('settings.tabs.abs')} />
                <SettingsNavLink tab="grimmory" active={tab} onSelect={setTab} label={t('settings.tabs.grimmory')} />
              </div>

              <div className="pt-3 pb-0.5">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 dark:text-zinc-600 px-3 mb-1">System</p>
                <SettingsNavLink tab="import" active={tab} onSelect={setTab} label={t('settings.tabs.import')} />
                <SettingsNavLink tab="blocklist" active={tab} onSelect={setTab} label={t('settings.tabs.blocklist')} />
                <SettingsNavLink tab="logs" active={tab} onSelect={setTab} label={t('settings.tabs.logs')} />
              </div>
            </>
          )}
        </nav>

        {/* Tab content */}
        <div className="flex-1 min-w-0">
          {renderTab()}
        </div>
      </div>
    </div>
  )
}
