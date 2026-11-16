import { beforeEach, describe, expect, it, vi } from 'vitest'

import { reorganizeApi } from './reorganize'

const fetchMock = vi.fn()

function lastBody(): unknown {
  const init = fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1] as RequestInit
  return JSON.parse(init.body as string)
}

describe('reorganizeApi', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ moves: [], summary: {} }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
  })

  it('previews with the scope and id', async () => {
    await reorganizeApi.preview('book', 7)
    expect(String(fetchMock.mock.calls[0][0])).toContain('/reorganize/preview?scope=book&id=7')
  })

  it('omits confirmExternalMode unless asked', async () => {
    await reorganizeApi.apply([1, 2])
    expect(lastBody()).toEqual({ fileIds: [1, 2] })
  })

  it('sends confirmExternalMode when confirmed', async () => {
    await reorganizeApi.apply([1], true)
    expect(lastBody()).toEqual({ fileIds: [1], confirmExternalMode: true })
  })
})
