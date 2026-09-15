import { useCallback, useState } from 'react'
import { api } from '../api/client'
import type { ScanItem } from '../api/client'

export interface UseFolderScanResult {
  path: string
  setPath: (p: string) => void
  scanning: boolean
  items: ScanItem[] | null
  truncated: boolean
  showImported: boolean
  error: string | null
  scan: () => void
  toggleShowImported: (checked: boolean) => void
  clear: () => void
}

/**
 * useFolderScan drives the bulk-folder-import scan (#2480) shared by the
 * Settings > Import "Bulk folder import" section and the /import wizard page:
 * the path input, the scan request (with the includeImported toggle,
 * default off), and the resulting items/truncated/error state. Each page
 * still builds its own per-unit resolution state (selection, format, chosen
 * book) from the fetched items via the optional onItems callback.
 */
export function useFolderScan(onItems?: (items: ScanItem[]) => void): UseFolderScanResult {
  const [path, setPath] = useState('')
  const [scanning, setScanning] = useState(false)
  const [items, setItems] = useState<ScanItem[] | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [showImported, setShowImported] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // clear drops the previous scan's results (items AND truncated together —
  // leaving truncated set with no items to explain it is a stale banner with
  // nothing beneath it) without touching path or showImported.
  const clear = useCallback(() => {
    setItems(null)
    setTruncated(false)
    setError(null)
  }, [])

  const runScan = useCallback(async (includeImported: boolean) => {
    const p = path.trim()
    if (!p) return
    setScanning(true)
    clear()
    try {
      const r = await api.scanFolder(p, { includeImported })
      setItems(r.items)
      setTruncated(r.truncated)
      onItems?.(r.items)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Scan failed')
    } finally {
      setScanning(false)
    }
  }, [path, clear, onItems])

  const scan = useCallback(() => { void runScan(showImported) }, [runScan, showImported])

  const toggleShowImported = useCallback((checked: boolean) => {
    setShowImported(checked)
    if (items) void runScan(checked)
  }, [items, runScan])

  return { path, setPath, scanning, items, truncated, showImported, error, scan, toggleShowImported, clear }
}
