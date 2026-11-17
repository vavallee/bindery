import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TFunction } from 'i18next'
import { api } from '../api/client'
import { grabErrorText } from './forceGrab'
import { humanizeEventType, isHeldEvent, parseEventData } from './historyEvents'

const t = ((key: string, opts?: { status?: number }) => {
  if (key === 'search.grabFailedStatus') return `The server could not start that download (${opts?.status}). Try again.`
  if (key === 'search.grabFailedNetwork') return 'Could not reach the server.'
  return 'Grab failed'
}) as unknown as TFunction

const req = { guid: 'g', title: 'T', nzbUrl: 'u', size: 1, indexerId: 1, protocol: 'usenet' as const }

afterEach(() => vi.unstubAllGlobals())

describe('grabErrorText through the real request path', () => {
  it('names the status for a 503 with an empty body and no status text', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 503, statusText: '' })))
    const err = await api.grab(req).catch(e => e)
    expect(grabErrorText(err, t)).toBe('The server could not start that download (503). Try again.')
  })

  it('uses the JSON error of a 400', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: 'bad release' }), { status: 400, headers: { 'Content-Type': 'application/json' } }),
    ))
    const err = await api.grab(req).catch(e => e)
    expect(grabErrorText(err, t)).toBe('bad release')
  })

  it('has a sentence for a network failure', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const err = await api.grab(req).catch(e => e)
    expect(grabErrorText(err, t)).toBe('Could not reach the server.')
  })
})

describe('history event helpers', () => {
  it('humanizes an unknown type', () => {
    expect(humanizeEventType('somethingNewHappened')).toBe('Something New Happened')
  })
  it('treats only a drop-hold or importHeld folder import as held', () => {
    expect(isHeldEvent('downloadFolderImported', { mode: 'drop-hold' })).toBe(true)
    expect(isHeldEvent('downloadFolderImported', { status: 'importHeld' })).toBe(true)
    expect(isHeldEvent('downloadFolderImported', { mode: 'drop' })).toBe(false)
    expect(isHeldEvent('bookImported', { mode: 'drop-hold' })).toBe(false)
  })
})

describe('parseEventData', () => {
  it.each(['null', '"x"', 'x', '5', '[]', '', '{bad'])('returns an empty object for %j', raw => {
    expect(parseEventData(raw)).toEqual({})
    expect(isHeldEvent('downloadFolderImported', parseEventData(raw))).toBe(false)
  })
  it('parses an object', () => {
    expect(parseEventData('{"mode":"drop-hold"}')).toEqual({ mode: 'drop-hold' })
  })
})
