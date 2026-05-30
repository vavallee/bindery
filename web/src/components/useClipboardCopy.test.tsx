import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ClipboardManualFallback from './ClipboardManualFallback'
import { useClipboardCopy } from './useClipboardCopy'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, fallback?: unknown) => typeof fallback === 'string' ? fallback : String(_key),
  }),
}))

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
const originalIsSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext')
const originalExecCommand = document.execCommand

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

function restoreClipboard() {
  if (originalClipboard) {
    Object.defineProperty(navigator, 'clipboard', originalClipboard)
  } else {
    Reflect.deleteProperty(navigator, 'clipboard')
  }
}

function restoreIsSecureContext() {
  if (originalIsSecureContext) {
    Object.defineProperty(window, 'isSecureContext', originalIsSecureContext)
  } else {
    Reflect.deleteProperty(window, 'isSecureContext')
  }
}

function restoreExecCommand() {
  if (originalExecCommand) {
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: originalExecCommand,
    })
  } else {
    Reflect.deleteProperty(document, 'execCommand')
  }
}

function ClipboardHarness({ text = 'copy me' }: { text?: string }) {
  const clipboard = useClipboardCopy()

  return (
    <div>
      <button type="button" onClick={() => clipboard.copy(text)}>
        Copy
      </button>
      <span data-testid="status">{clipboard.status}</span>
      {clipboard.status === 'manual' && (
        <ClipboardManualFallback text={clipboard.manualText} />
      )}
    </div>
  )
}

afterEach(() => {
  vi.restoreAllMocks()
  restoreClipboard()
  restoreIsSecureContext()
  restoreExecCommand()
})

describe('useClipboardCopy', () => {
  it('copies text with the Clipboard API', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setSecureContext(true)
    setClipboard(writeText)

    render(<ClipboardHarness text="clipboard text" />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('clipboard text'))
    expect(screen.getByTestId('status')).toHaveTextContent('copied')
    expect(screen.queryByLabelText('Text to copy')).toBeNull()
  })

  it('copies text with the legacy fallback when the Clipboard API is missing', async () => {
    const execCommand = vi.fn(() => true)
    setSecureContext(false)
    setClipboard()
    setExecCommand(execCommand)

    render(<ClipboardHarness text="legacy text" />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))

    await waitFor(() => expect(execCommand).toHaveBeenCalledWith('copy'))
    expect(screen.getByTestId('status')).toHaveTextContent('copied')
    expect(screen.queryByLabelText('Text to copy')).toBeNull()
  })

  it('shows selected fallback text when clipboard writes and legacy copy fail', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    const execCommand = vi.fn(() => false)
    setSecureContext(true)
    setClipboard(writeText)
    setExecCommand(execCommand)

    render(<ClipboardHarness text="manual text" />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('manual text'))
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(await screen.findByRole('status')).toHaveTextContent('Clipboard access is blocked')
    const fallback = screen.getByLabelText('Text to copy') as HTMLTextAreaElement
    expect(fallback).toHaveValue('manual text')
    await waitFor(() => {
      expect(fallback.selectionStart).toBe(0)
      expect(fallback.selectionEnd).toBe('manual text'.length)
    })
  })
})
