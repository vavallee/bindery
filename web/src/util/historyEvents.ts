// Shared by the History page and the History section of a book page, so the
// two read the same event the same way.

export interface ParsedEventData {
  message?: string
  path?: string
  size?: number
  [k: string]: unknown
}

export function parseEventData(data: string): ParsedEventData {
  if (!data) return {}
  try { return JSON.parse(data) } catch { return {} }
}

// Pair gating parks the first format to finish and records it as a
// downloadFolderImported event with mode "drop-hold" (status importHeld).
// Nothing has been dropped at that point, so the row must not read as an
// import. The event type stays the same for the filter.
export function isHeldEvent(type: string, parsed: ParsedEventData): boolean {
  return type === 'downloadFolderImported' && (parsed.mode === 'drop-hold' || parsed.status === 'importHeld')
}

// Turns a camelCase event enum into a Title Case fallback ("downloadFailed" ->
// "Download Failed") for any type without a curated i18n label, so a
// newly-added event type never renders as a raw identifier.
export function humanizeEventType(type: string): string {
  const spaced = type.replace(/([a-z0-9])([A-Z])/g, '$1 $2')
  return spaced.charAt(0).toUpperCase() + spaced.slice(1)
}

// Events whose message is the reason something went wrong.
export const FAILURE_EVENT_TYPES = new Set(['importFailed', 'downloadFailed', 'importRejected'])
