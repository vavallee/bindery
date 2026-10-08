import { Suspense, useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { lazyWithReload } from '../util/lazyWithReload'
import { useTranslation } from 'react-i18next'
import { api, DownloadClient, Indexer, ProwlarrInstance } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import ProtocolMismatchWarning from '../components/ProtocolMismatchWarning'
import { BELOW_MD, useMediaQuery } from '../components/useMediaQuery'

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

// Each tab is a default export, so the import resolves directly. lazyWithReload
// reloads once if a tab chunk is gone after an upgrade, keyed by its path.
const GeneralTab = lazyWithReload(() => import('./settings/GeneralTab'), './settings/GeneralTab')
const IndexersTab = lazyWithReload(() => import('./settings/IndexersTab'), './settings/IndexersTab')
const ClientsTab = lazyWithReload(() => import('./settings/ClientsTab'), './settings/ClientsTab')
const NotificationsTab = lazyWithReload(() => import('./settings/NotificationsTab'), './settings/NotificationsTab')
const QualityTab = lazyWithReload(() => import('./settings/QualityTab'), './settings/QualityTab')
const MetadataTab = lazyWithReload(() => import('./settings/MetadataTab'), './settings/MetadataTab')
const RootFoldersTab = lazyWithReload(() => import('./settings/RootFoldersTab'), './settings/RootFoldersTab')
const CalibreTab = lazyWithReload(() => import('./settings/CalibreTab'), './settings/CalibreTab')
const ABSTab = lazyWithReload(() => import('./settings/ABSTab'), './settings/ABSTab')
const GrimmoryTab = lazyWithReload(() => import('./settings/GrimmoryTab'), './settings/GrimmoryTab')
const ApiKeysTab = lazyWithReload(() => import('./settings/ApiKeysTab'), './settings/ApiKeysTab')
const ImportTab = lazyWithReload(() => import('./settings/ImportTab'), './settings/ImportTab')
const BlocklistTab = lazyWithReload(() => import('./settings/BlocklistTab'), './settings/BlocklistTab')
const LogsTab = lazyWithReload(() => import('./settings/LogsTab'), './settings/LogsTab')
const AdvancedTab = lazyWithReload(() => import('./settings/AdvancedTab'), './settings/AdvancedTab')
const AboutTab = lazyWithReload(() => import('./settings/AboutTab'), './settings/AboutTab')

type Tab = 'indexers' | 'clients' | 'notifications' | 'quality' | 'metadata' | 'general' | 'import' | 'rootfolders' | 'logs' | 'blocklist' | 'calibre' | 'abs' | 'grimmory' | 'api-keys' | 'advanced' | 'about'

const ADMIN_TABS: Tab[] = ['indexers', 'clients', 'notifications', 'quality', 'metadata', 'import', 'rootfolders', 'logs', 'blocklist', 'calibre', 'abs', 'grimmory', 'api-keys', 'advanced']

// 'general' and 'about' are visible to every authenticated user — About is where
// the in-app update message and bug_report.yml ("check Settings → About") point
// users to read their exact version/commit.
const ALL_TABS: Tab[] = ['general', 'about', ...ADMIN_TABS]

// Allow deep-linking to a specific tab via ?tab=indexers (used by first-run
// onboarding guidance on the Authors/Books empty states). Anything missing or
// unknown is General.
function tabFromParam(param: string | null): Tab {
  return param && (ALL_TABS as string[]).includes(param) ? param as Tab : 'general'
}

// The admin tabs in their sidebar groups. The sidebar and the phone select
// both read this, so the two cannot drift apart.
type TabGroup = { key: 'sources' | 'library' | 'integrations' | 'system'; tabs: Tab[] }
const ADMIN_GROUPS: TabGroup[] = [
  { key: 'sources', tabs: ['indexers', 'clients'] },
  { key: 'library', tabs: ['quality', 'metadata', 'rootfolders'] },
  // Notifications used to sit under Sources next to Indexers and Download
  // Clients, which it is not: nothing comes in from it. A webhook, Discord or
  // Apprise target is an outbound connection to another service, so it
  // belongs here.
  { key: 'integrations', tabs: ['notifications', 'calibre', 'abs', 'grimmory', 'api-keys'] },
  // Advanced is last on purpose: it is the escape hatch for the rare keys,
  // not a place to start (#2311).
  { key: 'system', tabs: ['import', 'blocklist', 'logs', 'advanced'] },
]

// Tabs still marked as a preview carry a chip in the sidebar and a suffix in
// the phone select.
const PREVIEW_TABS: Tab[] = ['grimmory']

function SettingsNavLink({ tab, active, onSelect, label, badge }: { tab: Tab; active: Tab; onSelect: (t: Tab) => void; label: string; badge?: string }) {
  return (
    <button
      onClick={() => onSelect(tab)}
      className={`w-full text-left px-3 py-1.5 rounded-md text-sm transition-colors ${badge ? 'flex items-center gap-2 ' : ''}${
        active === tab
          ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white font-medium'
          : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-zinc-800/50'
      }`}
    >
      {badge ? (
        <>
          <span>{label}</span>
          <span className="text-[9px] px-1.5 py-0.5 rounded bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-400 font-medium leading-none">
            {badge}
          </span>
        </>
      ) : label}
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
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = tabFromParam(searchParams.get('tab'))
  const narrow = useMediaQuery(BELOW_MD)

  // The active tab lives in the URL (?tab=…), so every Settings sub-tab is
  // deep-linkable and survives a refresh (e.g. /blocklist redirects to
  // /settings?tab=blocklist). Each change is a router navigation that pushes
  // a history entry: with a replace, Android back left Settings entirely
  // instead of returning to the previous tab. Going through the router (not
  // history.pushState) gives every tab its own location key, so scroll
  // restoration keeps tabs apart. Picking the tab already showing, and
  // corrections the user did not ask for, replace instead: neither should
  // be a back step.
  const setTab = useCallback((next: Tab, { replace = false }: { replace?: boolean } = {}) => {
    setSearchParams(prev => {
      const params = new URLSearchParams(prev)
      params.set('tab', next)
      return params
    }, { replace: replace || next === tab })
  }, [setSearchParams, tab])

  // Soft cross-tab navigation passed to tabs (e.g. General's "Manage in Root
  // Folders →", Import's "Configure … in General settings →") so those links
  // switch tabs in place via setTab instead of window.location.assign, which
  // would full-page-reload the SPA. Validates the incoming tab id against
  // ALL_TABS so a bad caller can't desync the UI.
  const navigateToTab = useCallback((next: string) => {
    if ((ALL_TABS as string[]).includes(next)) setTab(next as Tab)
  }, [setTab])

  // Labels for the grouped admin tabs. API Keys and Advanced use keys that do
  // not match their tab id.
  const tabLabel = (id: Tab): string => {
    switch (id) {
      case 'api-keys': return t('settings.tabs.apiKeys')
      case 'advanced': return t('settings.tabs.advanced', 'Advanced')
      default: return t(`settings.tabs.${id}`)
    }
  }

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
      case 'indexers': return (
        <>
          <ProtocolMismatchWarning indexers={indexers} clients={clients} onNavigate={navigateToTab} />
          <IndexersTab indexers={indexers} setIndexers={setIndexers} prowlarrInstances={prowlarrInstances} setProwlarrInstances={setProwlarrInstances} />
        </>
      )
      case 'clients': return (
        <>
          <ProtocolMismatchWarning indexers={indexers} clients={clients} onNavigate={navigateToTab} />
          <ClientsTab clients={clients} setClients={setClients} />
        </>
      )
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
      case 'api-keys': return <ApiKeysTab />
      case 'advanced': return <AdvancedTab />
      case 'about': return <AboutTab />
    }
  }

  // Redirect non-admins back to the general tab if they somehow navigate to an
  // admin-only tab (e.g. via direct link or stale state).
  useEffect(() => {
    if (!isAdmin && ADMIN_TABS.includes(tab)) {
      setTab('general', { replace: true })
    }
  }, [isAdmin, tab, setTab])

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">{t('settings.title')}</h2>

      <div className="flex flex-col md:flex-row gap-4 md:gap-8 items-stretch md:items-start">
        {narrow ? (
          // Below md the sidebar stacked above the content, so tapping a tab
          // changed something a screen further down and the tap looked like
          // it did nothing. A select keeps the choice to one line.
          <select
            value={tab}
            onChange={e => setTab(e.target.value as Tab)}
            aria-label={t('settings.sectionLabel')}
            className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
          >
            <option value="general">{t('settings.tabs.general')}</option>
            <option value="about">{t('settings.tabs.about', 'About')}</option>
            {isAdmin && ADMIN_GROUPS.map(group => (
              <optgroup key={group.key} label={t(`settings.groups.${group.key}`)}>
                {group.tabs.map(id => (
                  <option key={id} value={id}>
                    {PREVIEW_TABS.includes(id)
                      ? t('settings.tabWithPreview', { tab: tabLabel(id) })
                      : tabLabel(id)}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        ) : (
          <nav className="w-full md:w-44 flex-shrink-0 space-y-0.5">
            <SettingsNavLink tab="general" active={tab} onSelect={setTab} label={t('settings.tabs.general')} />
            <SettingsNavLink tab="about" active={tab} onSelect={setTab} label={t('settings.tabs.about', 'About')} />

            {isAdmin && ADMIN_GROUPS.map((group, i) => (
              <div key={group.key} className={`${i === 0 ? 'pt-4' : 'pt-3'} pb-0.5`}>
                <p className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 dark:text-zinc-600 px-3 mb-1">{t(`settings.groups.${group.key}`)}</p>
                {group.tabs.map(id => (
                  <SettingsNavLink
                    key={id}
                    tab={id}
                    active={tab}
                    onSelect={setTab}
                    label={tabLabel(id)}
                    badge={PREVIEW_TABS.includes(id) ? t('settings.previewBadge') : undefined}
                  />
                ))}
              </div>
            ))}
          </nav>
        )}

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
