import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import SearchDebugPanel from './SearchDebugPanel'
import { SearchDebug } from '../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, fallback?: unknown) => typeof fallback === 'string' ? fallback : String(_key),
  }),
}))

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
const originalIsSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext')
const originalExecCommand = document.execCommand

const debug: SearchDebug = {
  query: {
    title: 'Dune',
    author: 'Frank Herbert',
    mediaType: 'ebook',
    year: 1965,
  },
  indexers: [
    {
      indexerId: 1,
      indexerName: 'Test Indexer',
      enabled: true,
      categories: [7020],
      resultCount: 1,
      durationMs: 42,
    },
  ],
  pipeline: {
    rawCount: 1,
    afterDedupe: 1,
    afterUsenetJunk: 1,
    afterRelevance: 1,
  },
  filters: [
    {
      title: 'Dune release',
      indexerName: 'Test Indexer',
      stage: 'relevance',
      reason: 'accepted',
    },
  ],
  startedAt: '2026-05-27T10:00:00Z',
  durationMs: 50,
}

const expectedDebugJson = JSON.stringify(debug, null, 2)

function setClipboard(writeText?: (text: string) => Promise<void>) {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: writeText ? { writeText } : undefined,
  })
}

function setSecureContext(isSecureContext: boolean) {
  Object.defineProperty(window, 'isSecureContext', {
    configurable: true,
    value: isSecureContext,
  })
}

function setExecCommand(execCommand?: (commandId: string) => boolean) {
  Object.defineProperty(document, 'execCommand', {
    configurable: true,
    value: execCommand,
  })
}

afterEach(() => {
  vi.restoreAllMocks()
  if (originalClipboard) {
    Object.defineProperty(navigator, 'clipboard', originalClipboard)
  } else {
    Reflect.deleteProperty(navigator, 'clipboard')
  }
  if (originalIsSecureContext) {
    Object.defineProperty(window, 'isSecureContext', originalIsSecureContext)
  } else {
    Reflect.deleteProperty(window, 'isSecureContext')
  }
  if (originalExecCommand) {
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: originalExecCommand,
    })
  } else {
    Reflect.deleteProperty(document, 'execCommand')
  }
})

describe('SearchDebugPanel', () => {
  it('copies debug JSON to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setSecureContext(true)
    setClipboard(writeText)

    render(<SearchDebugPanel debug={debug} resultCount={1} defaultOpen />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy debug info (JSON)' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expectedDebugJson))
    expect(screen.getByRole('button', { name: 'Copied!' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Text to copy')).toBeNull()
  })

  it('copies debug JSON with the legacy fallback when the Clipboard API is missing', async () => {
    const execCommand = vi.fn(() => true)
    setSecureContext(false)
    setClipboard()
    setExecCommand(execCommand)

    render(<SearchDebugPanel debug={debug} resultCount={1} defaultOpen />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy debug info (JSON)' }))

    await waitFor(() => expect(execCommand).toHaveBeenCalledWith('copy'))
    expect(screen.getByRole('button', { name: 'Copied!' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Text to copy')).toBeNull()
  })

  it('shows selected JSON when clipboard writes and legacy copy fail', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    const execCommand = vi.fn(() => false)
    setSecureContext(true)
    setClipboard(writeText)
    setExecCommand(execCommand)

    render(<SearchDebugPanel debug={debug} resultCount={1} defaultOpen />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy debug info (JSON)' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expectedDebugJson))
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(await screen.findByRole('status')).toHaveTextContent('Clipboard access is blocked')
    const fallback = screen.getByLabelText('Text to copy') as HTMLTextAreaElement
    expect(fallback).toHaveValue(expectedDebugJson)
    await waitFor(() => {
      expect(fallback.selectionStart).toBe(0)
      expect(fallback.selectionEnd).toBe(expectedDebugJson.length)
    })
  })
})
