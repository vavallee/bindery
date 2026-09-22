import { describe, it, expect } from 'vitest'
import { partitionItems, summarise, moveWithin } from './qualitySummary'

const ebook = ['azw3', 'epub', 'mobi', 'pdf']
const audio = ['flac', 'm4b', 'mp3']

describe('partitionItems', () => {
  it('splits by media type and keeps the stored order inside each list', () => {
    const { ebook: e, audio: a, other } = partitionItems([
      { quality: 'm4b', allowed: true },
      { quality: 'pdf', allowed: false },
      { quality: 'epub', allowed: true },
      { quality: 'opus', allowed: true },
      { quality: 'flac', allowed: false },
    ], ebook, audio)
    expect(e.map(i => i.quality)).toEqual(['pdf', 'epub'])
    expect(a.map(i => i.quality)).toEqual(['m4b', 'flac'])
    // A token neither list knows is carried through untouched so an edit
    // never drops what a third party client stored.
    expect(other).toEqual([{ quality: 'opus', allowed: true }])
  })
})

describe('summarise', () => {
  it('names the ticked formats in order and the unticked ones separately', () => {
    expect(summarise([
      { quality: 'azw3', allowed: true },
      { quality: 'pdf', allowed: false },
      { quality: 'epub', allowed: true },
    ])).toEqual({ kind: 'prefer', first: 'azw3', rest: ['epub'], never: ['pdf'] })
  })

  it('reports a single ticked format without a "then"', () => {
    expect(summarise([{ quality: 'epub', allowed: true }]))
      .toEqual({ kind: 'prefer', first: 'epub', rest: [], never: [] })
  })

  it('reports an empty list as no opinion', () => {
    expect(summarise([])).toEqual({ kind: 'noOpinion' })
  })

  it('reports a list with nothing ticked as none allowed', () => {
    expect(summarise([{ quality: 'epub', allowed: false }, { quality: 'pdf', allowed: false }]))
      .toEqual({ kind: 'noneAllowed' })
  })
})

describe('moveWithin', () => {
  it('swaps with the neighbour and stops at either end', () => {
    const list = [{ quality: 'a', allowed: true }, { quality: 'b', allowed: true }, { quality: 'c', allowed: true }]
    expect(moveWithin(list, 0, 1).map(i => i.quality)).toEqual(['b', 'a', 'c'])
    expect(moveWithin(list, 2, -1).map(i => i.quality)).toEqual(['a', 'c', 'b'])
    expect(moveWithin(list, 0, -1)).toBe(list)
    expect(moveWithin(list, 2, 1)).toBe(list)
  })
})
