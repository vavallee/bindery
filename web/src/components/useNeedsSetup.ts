import { useEffect, useState } from 'react'
import { api } from '../api/client'

// Returns true once we've confirmed the instance has NO indexers AND NO
// download clients configured. Used to decide whether to show the first-run
// onboarding guidance on the Authors/Books empty states. Defaults to false
// (and stays false on any error) so the guidance never blocks or replaces the
// normal empty state if the checks fail.
export function useNeedsSetup(): boolean {
  const [needsSetup, setNeedsSetup] = useState(false)

  useEffect(() => {
    let cancelled = false
    Promise.all([api.listIndexers(), api.listDownloadClients()])
      .then(([indexers, clients]) => {
        if (cancelled) return
        setNeedsSetup(indexers.length === 0 && clients.length === 0)
      })
      .catch(() => {
        // Fall back to the normal empty state on failure.
        if (!cancelled) setNeedsSetup(false)
      })
    return () => { cancelled = true }
  }, [])

  return needsSetup
}
