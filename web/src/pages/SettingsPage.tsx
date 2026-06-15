import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DownloadClient, Indexer, ProwlarrInstance } from '../api/client'
import { useAuth } from '../auth/AuthContext'

// Decomposed from the former ~5100-line SettingsPage monolith (#547): each tab
// now lives in its own file under ./settings/. Tabs are React.lazy code-split
// (#773) so each tab's JS only downloads when the tab is first opened, keeping
// it out of the initial app bundle. The single <Suspense> boundary around the
// active tab shows a lightweight loading fallback while the chunk fetches.
// (The SettingsPage tests already await tab content via findBy* queries, so the
// async resolution is transparent to them.)
//
// Cross-tab state: the monolith fetched indexers, download clients, and
// Prowlarr instances eagerly on page mount (not on tab select). That fetch
// timing is preserved by owning those three lists here and passing them to the
// Indexers and Clients tabs. Everything else is genuinely tab-local and lives
// inside its own tab component.

// Each tab is a default export, so lazy(() => import(...)) resolves directly.
const GeneralTab = lazy(() => import('./settings/GeneralTab'))
const IndexersTab = lazy(() => import('./settings/IndexersTab'))
const ClientsTab = lazy(() => import('./settings/ClientsTab'))
const NotificationsTab = lazy(() => import('./settings/NotificationsTab'))
const QualityTab = lazy(() => import('./settings/QualityTab'))
const MetadataTab = lazy(() => import('./settings/MetadataTab'))
const RootFoldersTab = lazy(() => import('./settings/RootFoldersTab'))
const CalibreTab = lazy(() => import('./settings/CalibreTab'))
const ABSTab = lazy(() => import('./settings/ABSTab'))
const GrimmoryTab = lazy(() => import('./settings/GrimmoryTab'))
const ImportTab = lazy(() => import('./settings/ImportTab'))
const BlocklistTab = lazy(() => import('./settings/BlocklistTab'))
const LogsTab = lazy(() => import('./settings/LogsTab'))

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

// Suspense fallback shown while a lazily-loaded tab chunk downloads. Mirrors the
// app's existing inline loading style (animated spinner + muted text in a
// dark:-aware palette).
function TabFallback({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2 py-8 text-sm text-slate-600 dark:text-zinc-500">
      <span
        className="h-4 w-4 animate-spin rounded-full border-2 border-slate-300 border-t-slate-600 dark:border-zinc-700 dark:border-t-zinc-400"
        aria-hidden="true"
      />
      <span>{label}</span>
    </div>
  )
}

export default function SettingsPage() {
  const { t } = useTranslation()
  const { isAdmin } = useAuth()
  const [tab, setTabState] = useState<Tab>(initialTabFromUrl)

  // Keep the active tab in the URL (?tab=…) so every Settings sub-tab is
  // deep-linkable and survives a refresh/back. replaceState (not a router
  // navigate) avoids piling a history entry per tab click while still letting
  // initialTabFromUrl pick the tab on a fresh load (e.g. /blocklist redirects
  // to /settings?tab=blocklist).
  const setTab = useCallback((next: Tab) => {
    setTabState(next)
    try {
      const url = new URL(window.location.href)
      url.searchParams.set('tab', next)
      window.history.replaceState(window.history.state, '', url)
    } catch { /* ignore — tab state still updates */ }
  }, [])

  // Soft cross-tab navigation passed to tabs (e.g. General's "Manage in Root
  // Folders →", Import's "Configure … in General settings →") so those links
  // switch tabs in place via setTab instead of window.location.assign, which
  // would full-page-reload the SPA. Validates the incoming tab id against
  // ALL_TABS so a bad caller can't desync the UI.
  const navigateToTab = useCallback((next: string) => {
    if ((ALL_TABS as string[]).includes(next)) setTab(next as Tab)
  }, [setTab])

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
      case 'general': return <GeneralTab onNavigate={navigateToTab} />
      case 'indexers': return <IndexersTab indexers={indexers} setIndexers={setIndexers} prowlarrInstances={prowlarrInstances} setProwlarrInstances={setProwlarrInstances} />
      case 'clients': return <ClientsTab clients={clients} setClients={setClients} />
      case 'notifications': return <NotificationsTab />
      case 'quality': return <QualityTab />
      case 'metadata': return <MetadataTab />
      case 'rootfolders': return <RootFoldersTab />
      case 'calibre': return <CalibreTab />
      case 'abs': return <ABSTab />
      case 'grimmory': return <GrimmoryTab />
      case 'import': return <ImportTab onNavigate={navigateToTab} />
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
  }, [isAdmin, tab, setTab])

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
                <button
                  onClick={() => setTab('grimmory')}
                  className={`w-full text-left px-3 py-1.5 rounded-md text-sm transition-colors flex items-center gap-2 ${
                    tab === 'grimmory'
                      ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white font-medium'
                      : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-zinc-800/50'
                  }`}
                >
                  <span>{t('settings.tabs.grimmory')}</span>
                  <span className="text-[9px] px-1.5 py-0.5 rounded bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-400 font-medium leading-none">
                    Preview
                  </span>
                </button>
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
          <Suspense fallback={<TabFallback label={t('settings.loadingTab')} />}>
            {renderTab()}
          </Suspense>
        </div>
      </div>
    </div>
  )
}
