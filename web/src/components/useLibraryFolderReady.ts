import { useEffect, useState } from 'react'
import { api } from '../api/client'

// Reports whether imports have a library folder to land in, from the server's
// setup state (hasLibraryFolder): a root folder exists, or BINDERY_LIBRARY_DIR
// is a writable directory. A root folder row is not required on its own,
// because the importer falls back to BINDERY_LIBRARY_DIR for any author
// without one, so an install with that folder mounted is already set up.
//
// The server answers from the blocked check's cached, time bounded storage
// probe, so this never reaches /system/storage, which probes the media mounts
// uncached on every call.
//
// Returns null until known. A failed request, or a server that left the field
// out (non admin, or no probe answer yet), reports true: guidance that cannot
// be checked is left out rather than shown wrongly.
export function useLibraryFolderReady(): boolean | null {
  const [ready, setReady] = useState<boolean | null>(null)

  useEffect(() => {
    let cancelled = false
    api.setupState()
      .then(s => { if (!cancelled) setReady(s.hasLibraryFolder !== false) })
      .catch(() => { if (!cancelled) setReady(true) })
    return () => { cancelled = true }
  }, [])

  return ready
}
