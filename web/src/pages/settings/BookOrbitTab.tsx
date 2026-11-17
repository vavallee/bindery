import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import RichText from '../../components/RichText'
import { api, ABSImportProgress, ABSImportRun, ABSImportStats, BookOrbitConfig, BookOrbitLibrary, BookOrbitTestResult } from '../../api/client'
import { inputCls } from './formStyles'
import PathRemapField from './PathRemapField'
import { secretInputAttrs, urlInputAttrs } from '../../util/inputAttrs'
import { primaryFill } from '../../components/buttons'
import { errorText } from '../../util/errorText'

const RUNS_PAGE = 10

// The counts a BookOrbit run reports, in the order they are shown.
const STATS: (keyof ABSImportStats)[] = ['itemsSeen', 'booksCreated', 'booksLinked', 'booksUpdated', 'authorsCreated', 'seriesCreated', 'ownedMarked', 'pendingManual', 'failed']

const secondaryBtn = 'px-4 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium'

function sameIds(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every(id => b.includes(id))
}

export default function BookOrbitTab() {
  const { t } = useTranslation()
  const fid = useId()
  const [config, setConfig] = useState<BookOrbitConfig | null>(null)
  const [draft, setDraft] = useState({ baseUrl: '', apiKey: '', pathRemap: '', libraryIds: [] as string[] })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<BookOrbitTestResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)
  const [libraries, setLibraries] = useState<BookOrbitLibrary[] | null>(null)
  const [librariesError, setLibrariesError] = useState<string | null>(null)
  // Names resolved quietly from the live library list, so the saved selection
  // and the run history read "Books" after a reload instead of "Library 1".
  const [knownLibraries, setKnownLibraries] = useState<BookOrbitLibrary[]>([])
  // Bumped by every save and every library listing. A quiet lookup that
  // started before the latest one is dropped, so a slow answer from the old
  // server cannot overwrite names from the new one.
  const namesReq = useRef(0)
  const [progress, setProgress] = useState<ABSImportProgress | null>(null)
  const [importError, setImportError] = useState<string | null>(null)
  const [runs, setRuns] = useState<ABSImportRun[]>([])
  const [runsOffset, setRunsOffset] = useState(0)
  const [moreRuns, setMoreRuns] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const loadRuns = useCallback(async (offset: number) => {
    try {
      // One extra row says whether a further page exists.
      const page = await api.bookOrbitImportRuns({ limit: RUNS_PAGE + 1, offset })
      setRuns(page.slice(0, RUNS_PAGE))
      setMoreRuns(page.length > RUNS_PAGE)
      setRunsOffset(offset)
    } catch {
      // The history is secondary; a failure here must not hide the form.
    }
  }, [])

  // Best effort: when BookOrbit cannot be reached the ids stay as they are and
  // nothing is shown, since the admin did not ask for the list.
  const resolveNames = useCallback(async (cfg: BookOrbitConfig) => {
    if (!cfg.baseUrl || !cfg.apiKeyConfigured) return
    const seq = ++namesReq.current
    try {
      const list = await api.bookOrbitLibraries()
      if (seq === namesReq.current && Array.isArray(list)) setKnownLibraries(list)
    } catch {
      // Falls back to "Library N".
    }
  }, [])

  // Each action starts from a clean slate, so a result or error from an
  // earlier one never sits beside the answer to a later one.
  const clearFeedback = () => {
    setSaveError(null)
    setTestResult(null)
    setTestError(null)
    setLibrariesError(null)
    setImportError(null)
  }

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  const poll = useCallback(() => {
    stopPolling()
    pollRef.current = setInterval(async () => {
      try {
        const next = await api.bookOrbitImportStatus()
        setProgress(next)
        if (!next.running) {
          stopPolling()
          void loadRuns(0)
        }
      } catch {
        stopPolling()
      }
    }, 1500)
  }, [stopPolling, loadRuns])

  useEffect(() => {
    api.bookOrbitConfig()
      .then(cfg => {
        setConfig(cfg)
        setDraft({ baseUrl: cfg.baseUrl ?? '', apiKey: '', pathRemap: cfg.pathRemap ?? '', libraryIds: cfg.libraryIds ?? [] })
        void resolveNames(cfg)
      })
      .catch(console.error)
      .finally(() => setLoading(false))
    api.bookOrbitImportStatus()
      .then(p => {
        setProgress(p)
        if (p.running) poll()
      })
      .catch(() => {})
    void loadRuns(0)
    return stopPolling
  }, [poll, loadRuns, stopPolling, resolveNames])

  const save = async () => {
    setSaving(true)
    clearFeedback()
    namesReq.current++
    try {
      const payload: { baseUrl: string; libraryIds: string[]; pathRemap: string; apiKey?: string } = {
        baseUrl: draft.baseUrl.trim(),
        libraryIds: draft.libraryIds,
        pathRemap: draft.pathRemap.trim(),
      }
      if (draft.apiKey.trim()) payload.apiKey = draft.apiKey.trim()
      const next = await api.bookOrbitSetConfig(payload)
      setConfig(next)
      setDraft({ baseUrl: next.baseUrl, apiKey: '', pathRemap: next.pathRemap, libraryIds: next.libraryIds })
      void resolveNames(next)
    } catch (err) {
      setSaveError(errorText(err, t('settings.bookorbit.errors.save')))
    } finally {
      setSaving(false)
    }
  }

  // The server tests the saved address only, so the address has to be saved
  // before a test means anything. A key typed but not yet saved is sent along.
  const urlSaved = Boolean(config?.baseUrl) && config?.baseUrl === draft.baseUrl.trim()
  const probeBody = () => (draft.apiKey.trim() ? { apiKey: draft.apiKey.trim() } : undefined)

  const test = async () => {
    setTesting(true)
    clearFeedback()
    try {
      setTestResult(await api.bookOrbitTest(probeBody()))
    } catch (err) {
      setTestError(errorText(err, t('settings.bookorbit.errors.test')))
    } finally {
      setTesting(false)
    }
  }

  const listLibraries = async () => {
    clearFeedback()
    namesReq.current++
    try {
      const list = await api.bookOrbitLibraries(probeBody())
      setLibraries(list)
      setKnownLibraries(list)
    } catch (err) {
      setLibrariesError(errorText(err, t('settings.bookorbit.errors.libraries')))
    }
  }

  const toggleLibrary = (id: string) => {
    setDraft(prev => ({
      ...prev,
      libraryIds: prev.libraryIds.includes(id) ? prev.libraryIds.filter(x => x !== id) : [...prev.libraryIds, id],
    }))
  }

  const unsaved = !config
    || config.baseUrl !== draft.baseUrl.trim()
    || config.pathRemap !== draft.pathRemap.trim()
    || draft.apiKey.trim() !== ''
    || !sameIds(config.libraryIds ?? [], draft.libraryIds)
  const ready = Boolean(config?.baseUrl && config.apiKeyConfigured && (config.libraryIds ?? []).length > 0) && !unsaved

  const start = async (dryRun: boolean) => {
    clearFeedback()
    try {
      setProgress(await api.bookOrbitImportStart({ dryRun }))
      poll()
    } catch (err) {
      setImportError(errorText(err, t('settings.bookorbit.errors.start')))
    }
  }

  if (loading) return <div className="text-fg-muted">{t('common.loading')}</div>

  const running = Boolean(progress?.running)
  // Libraries to tick: the ones BookOrbit just listed, or, before a listing,
  // the saved selection by id so it stays visible and can be cleared.
  const libraryName = (id: string) => (libraries ?? knownLibraries).find(lib => lib.id === id)?.name
    ?? knownLibraries.find(lib => lib.id === id)?.name
    ?? t('settings.bookorbit.libraries.unnamed', { id })
  const choices: BookOrbitLibrary[] = libraries ?? draft.libraryIds.map(id => ({ id, name: libraryName(id), folders: [] }))

  return (
    <div className="space-y-6 max-w-2xl">
      <div>
        <h3 className="text-lg font-semibold mb-1">{t('settings.tabs.bookorbit')}</h3>
        <p className="text-xs text-fg-muted">
          <RichText text={t('settings.bookorbit.intro')} components={{ site: <a href="https://github.com/bookorbit/bookorbit" target="_blank" rel="noopener noreferrer" className="underline" />, b: <strong /> }} />
        </p>
      </div>

      <div className="space-y-3">
        <div>
          <label htmlFor={`${fid}-url`} className="block text-xs font-medium mb-1">{t('settings.bookorbit.fields.serverUrl')}</label>
          <input id={`${fid}-url`}
            {...urlInputAttrs}
            type="text"
            value={draft.baseUrl}
            onChange={e => setDraft(prev => ({ ...prev, baseUrl: e.target.value }))}
            placeholder="http://bookorbit:3000"
            className={inputCls}
          />
        </div>
        <div>
          <label htmlFor={`${fid}-key`} className="block text-xs font-medium mb-1">
            {t('settings.bookorbit.fields.apiKey')}
            {config?.apiKeyConfigured && !draft.apiKey && (
              <span className="ml-1 text-fg-muted font-normal">{t('settings.bookorbit.fields.configured')}</span>
            )}
          </label>
          <input id={`${fid}-key`}
            {...secretInputAttrs}
            type="password"
            value={draft.apiKey}
            onChange={e => setDraft(prev => ({ ...prev, apiKey: e.target.value }))}
            placeholder={config?.apiKeyConfigured ? '••••••••' : 'bok_'}
            className={inputCls}
          />
          <p className="mt-1 text-[11px] text-fg-muted">
            <RichText text={t('settings.bookorbit.fields.apiKeyHint')} components={{ b: <strong /> }} />
          </p>
        </div>
        <PathRemapField
          id={`${fid}-remap`}
          label={t('settings.bookorbit.fields.pathRemap')}
          value={draft.pathRemap}
          onChange={value => setDraft(prev => ({ ...prev, pathRemap: value }))}
          placeholder="/books:/media/books"
          help={t('settings.bookorbit.fields.pathRemapHelp')}
        />
      </div>

      <div>
        <h4 className="text-sm font-semibold mb-1">{t('settings.bookorbit.libraries.heading')}</h4>
        <p className="text-xs text-fg-muted mb-2">{t('settings.bookorbit.libraries.hint')}</p>
        {choices.length === 0 ? (
          <p className="text-xs text-fg-muted">{t('settings.bookorbit.libraries.none')}</p>
        ) : (
          <ul className="space-y-1">
            {choices.map(lib => (
              <li key={lib.id}>
                <label className="flex items-start gap-2 text-sm cursor-pointer">
                  <input
                    type="checkbox"
                    className="mt-1 w-4 h-4"
                    checked={draft.libraryIds.includes(lib.id)}
                    onChange={() => toggleLibrary(lib.id)}
                  />
                  <span>
                    {lib.name}
                    {lib.folders.length > 0 && (
                      <span className="block text-[11px] text-fg-muted font-mono">{lib.folders.join(', ')}</span>
                    )}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        )}
        {librariesError && <p className="mt-2 text-red-700 dark:text-red-400 text-xs">{librariesError}</p>}
      </div>

      <div className="flex gap-2 flex-wrap">
        <button onClick={save} disabled={saving} className={`px-4 py-2 ${primaryFill} disabled:opacity-50 rounded text-sm font-medium`}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
        <button onClick={test} disabled={testing || !urlSaved} className={secondaryBtn}>
          {testing ? t('common.testing') : t('settings.bookorbit.testConnection')}
        </button>
        <button onClick={listLibraries} disabled={!urlSaved} className={secondaryBtn}>
          {t('settings.bookorbit.libraries.load')}
        </button>
      </div>
      {!urlSaved && draft.baseUrl.trim() !== '' && (
        <p className="text-[11px] text-fg-muted">{t('settings.bookorbit.saveUrlFirst')}</p>
      )}
      {saveError && <p className="text-red-700 dark:text-red-400 text-xs">{saveError}</p>}
      {testError && <p className="text-red-700 dark:text-red-400 text-xs">{testError}</p>}
      {testResult && (
        <p className="text-xs text-accent-text">
          {t('settings.bookorbit.testOk', { username: testResult.username, count: testResult.libraryCount })}
        </p>
      )}

      <div className="pt-4 border-t border-slate-200 dark:border-zinc-800 space-y-3">
        <div>
          <h4 className="text-sm font-semibold mb-1">{t('settings.bookorbit.import.heading')}</h4>
          <p className="text-xs text-fg-muted">{t('settings.bookorbit.import.hint')}</p>
        </div>
        <div className="flex gap-2 flex-wrap">
          <button onClick={() => start(true)} disabled={!ready || running} className={secondaryBtn}>
            {t('settings.bookorbit.import.preview')}
          </button>
          <button onClick={() => start(false)} disabled={!ready || running} className={`px-4 py-2 ${primaryFill} disabled:opacity-50 rounded text-sm font-medium`}>
            {running ? t('settings.bookorbit.import.running') : t('settings.bookorbit.import.run')}
          </button>
        </div>
        {!ready && !running && (
          <p className="text-[11px] text-amber-700 dark:text-amber-300">
            {unsaved ? t('settings.bookorbit.import.saveFirst') : t('settings.bookorbit.import.configureFirst')}
          </p>
        )}
        {importError && <p className="text-red-700 dark:text-red-400 text-xs">{importError}</p>}

        {progress && (progress.running || progress.stats || progress.error) && (
          <div className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 space-y-2">
            {progress.running && (
              <div className="flex justify-between text-xs text-fg-muted gap-3">
                <span>{progress.message || t('settings.bookorbit.import.working')}</span>
                <span>{t('settings.bookorbit.import.processed', { count: progress.processed })}</span>
              </div>
            )}
            {!progress.running && progress.error && (
              <p className="text-xs text-red-700 dark:text-red-400">
                {progress.dryRun ? t('settings.bookorbit.import.dryRunFailed', { error: progress.error }) : t('settings.bookorbit.import.failed', { error: progress.error })}
              </p>
            )}
            {!progress.running && !progress.error && progress.stats && (
              <p className="text-xs font-medium text-slate-700 dark:text-zinc-300">
                {progress.dryRun ? t('settings.bookorbit.import.dryRunComplete') : t('settings.bookorbit.import.complete')}
              </p>
            )}
            {!progress.running && progress.stats && <StatList stats={progress.stats} dryRun={Boolean(progress.dryRun)} />}
            {progress.results && progress.results.length > 0 && (
              <div>
                <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                  {t('settings.bookorbit.import.sample', { count: progress.results.length })}
                </p>
                <ul className="mt-1 max-h-64 space-y-1 overflow-y-auto pr-1">
                  {[...progress.results].reverse().map(result => (
                    <li key={result.itemId} className="flex justify-between gap-3 text-[11px] text-fg-muted">
                      <span className="min-w-0 flex-1">
                        <span className="block truncate">{result.title || result.itemId}</span>
                        {result.message && <span className="block text-[10px] break-words">{result.message}</span>}
                      </span>
                      <span className="flex-shrink-0 uppercase tracking-wide">{result.outcome}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </div>

      <div className="pt-4 border-t border-slate-200 dark:border-zinc-800">
        <h4 className="text-sm font-semibold mb-2">{t('settings.bookorbit.runs.heading')}</h4>
        {runs.length === 0 ? (
          <p className="text-xs text-fg-muted">{t('settings.bookorbit.runs.empty')}</p>
        ) : (
          <ul className="space-y-2">
            {runs.map(run => (
              <li key={run.id} className="rounded border border-slate-200 dark:border-zinc-800 px-3 py-2 text-xs">
                <div className="flex justify-between gap-3">
                  <span className="font-medium">
                    {run.dryRun ? t('settings.bookorbit.runs.dryRun') : t('settings.bookorbit.runs.import')}
                    {' · '}
                    {libraryName(run.libraryId)}
                  </span>
                  <span className="text-fg-muted">{new Date(run.startedAt).toLocaleString()} · {run.status}</span>
                </div>
                {run.summary?.error
                  ? <p className="mt-1 text-red-700 dark:text-red-400 break-words">{run.summary.error}</p>
                  : run.summary?.stats && <StatList stats={run.summary.stats} dryRun={run.dryRun} />}
              </li>
            ))}
          </ul>
        )}
        {(runsOffset > 0 || moreRuns) && (
          <div className="mt-2 flex gap-2">
            <button onClick={() => loadRuns(Math.max(0, runsOffset - RUNS_PAGE))} disabled={runsOffset === 0} className="text-xs underline disabled:opacity-50 disabled:no-underline">
              {t('settings.bookorbit.runs.newer')}
            </button>
            <button onClick={() => loadRuns(runsOffset + RUNS_PAGE)} disabled={!moreRuns} className="text-xs underline disabled:opacity-50 disabled:no-underline">
              {t('settings.bookorbit.runs.older')}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

function StatList({ stats, dryRun }: { stats: ABSImportStats; dryRun: boolean }) {
  const { t } = useTranslation()
  return (
    <dl className="mt-1 grid grid-cols-2 sm:grid-cols-3 gap-x-4 gap-y-0.5 text-[11px] text-fg-muted">
      {STATS.map(field => (
        <div key={field} className="flex justify-between gap-2">
          <dt>{t(`settings.bookorbit.stats.${dryRun && field === 'ownedMarked' ? 'ownedMarkedDryRun' : field}`)}</dt>
          <dd className="font-medium text-slate-700 dark:text-zinc-300">{stats[field] ?? 0}</dd>
        </div>
      ))}
    </dl>
  )
}
