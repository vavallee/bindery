import { describe, it, expect } from 'vitest'
import {
  DOWNLOAD_STATUSES,
  FAILED_STATUSES,
  RETRYABLE_STATUSES,
  downloadStatusBadge,
  isMatchable,
  isRetryable,
} from './downloadStatus'

// Stub t: return the key so we can assert which label path was taken without
// depending on the loaded i18n resources.
const t = ((key: string) => key) as unknown as Parameters<typeof downloadStatusBadge>[1]

// Every DownloadState constant declared in internal/models/download_state.go.
// Copied deliberately: if a state is added there and not here, the coverage
// test below fails and whoever added it gets told to give it a label and a chip
// instead of shipping a grey pill reading the raw enum (#2339).
const GO_DOWNLOAD_STATES = [
  'grabbed',
  'downloading',
  'completed',
  'importPending',
  'importing',
  'imported',
  'failed',
  'importFailed',
  'importBlocked',
  'importExternal',
  'importHeld',
]

describe('DOWNLOAD_STATUSES', () => {
  it('covers every state internal/models/download_state.go defines', () => {
    for (const state of GO_DOWNLOAD_STATES) {
      expect(DOWNLOAD_STATUSES[state], `no table entry for "${state}"`).toBeDefined()
    }
  })

  it('has no entry the Go side does not define', () => {
    expect(Object.keys(DOWNLOAD_STATUSES).sort()).toEqual([...GO_DOWNLOAD_STATES].sort())
  })

  it('gives every state a label key and a chip class', () => {
    for (const [state, entry] of Object.entries(DOWNLOAD_STATUSES)) {
      expect(entry.labelKey, state).toBeTruthy()
      expect(entry.chip, state).toBeTruthy()
    }
  })

  it('explains the two non-terminal hand-off states', () => {
    expect(DOWNLOAD_STATUSES.importExternal.descriptionKey).toBeTruthy()
    expect(DOWNLOAD_STATUSES.importHeld.descriptionKey).toBeTruthy()
  })
})

describe('derived state sets', () => {
  it('counts the three failed states', () => {
    expect([...FAILED_STATUSES].sort()).toEqual(['failed', 'importBlocked', 'importFailed'])
  })

  // internal/db/downloads.go ResetImportRetry updates
  // "WHERE ... status IN (StateImportFailed, StateImportBlocked)", so the bulk
  // retry must offer exactly those two — #2336 shipped with importFailed only.
  it('matches the states ResetImportRetry accepts', () => {
    expect([...RETRYABLE_STATUSES].sort()).toEqual(['importBlocked', 'importFailed'])
    expect(isRetryable('importBlocked')).toBe(true)
    expect(isRetryable('importFailed')).toBe(true)
    expect(isRetryable('failed')).toBe(false)
  })

  it('treats importFailed and importBlocked as matchable and nothing else', () => {
    expect(isMatchable('importFailed')).toBe(true)
    expect(isMatchable('importBlocked')).toBe(true)
    expect(isMatchable('failed')).toBe(false)
    expect(isMatchable('importExternal')).toBe(false)
  })
})

describe('downloadStatusBadge', () => {
  it('reuses the existing history keys where they already say the same thing', () => {
    expect(downloadStatusBadge('grabbed', t).label).toBe('history.events.grabbed')
    expect(downloadStatusBadge('importFailed', t).label).toBe('history.events.importFailed')
  })

  it('labels the two states that used to render as raw enum text (#2339)', () => {
    const ext = downloadStatusBadge('importExternal', t)
    expect(ext.label).toBe('queue.status.importExternal')
    expect(ext.label).not.toBe('importExternal')
    expect(ext.chip).not.toBe('')
    expect(ext.description).toBe('queue.status.importExternalHint')

    const held = downloadStatusBadge('importHeld', t)
    expect(held.label).toBe('queue.status.importHeld')
    expect(held.description).toBe('queue.status.importHeldHint')
  })

  it('falls back to the raw status on a muted chip for an unknown state', () => {
    const b = downloadStatusBadge('weird', t)
    expect(b.label).toBe('weird')
    expect(b.chip).toMatch(/slate|zinc/)
    expect(b.description).toBeUndefined()
  })
})
