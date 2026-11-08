import { useEffect, useState } from 'react'
import { api } from '../api/client'

// Reports whether imports have a library folder to land in: at least one root
// folder, or BINDERY_LIBRARY_DIR present and writable. A root folder row is
// not required on its own, because the importer falls back to
// BINDERY_LIBRARY_DIR for any author without one (effectiveLibraryDir in
// internal/importer/scanner.go), so an install with that folder mounted is
// already set up and must not be asked to add a root folder.
//
// Pass enabled=false for non admins: they cannot add root folders and cannot
// read /system/storage, so there is nothing to ask them. The answer is then
// true without any request.
//
// Returns null until known. A failed lookup reports true, like useNeedsSetup:
// guidance that cannot be checked is left out rather than shown wrongly.
export function useLibraryFolderReady(enabled: boolean): boolean | null {
  const [ready, setReady] = useState<boolean | null>(null)

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    api.listRootFolders()
      .then(async folders => {
        if (folders.length > 0) return true
        const storage = await api.getStorage()
        const library = storage.dirs.find(d => d.name === 'library')
        return !!library && library.exists && library.writable
      })
      .then(r => { if (!cancelled) setReady(r) })
      .catch(() => { if (!cancelled) setReady(true) })
    return () => { cancelled = true }
  }, [enabled])

  return enabled ? ready : true
}
