import { describe, expect, it } from 'vitest'
import source from './GeneralTab.tsx?raw'

// Regenerate API key and Rotate session secret were filled amber, the loudest
// controls on the tab for two rarely used destructive actions. They use the
// shared danger outline. jsdom does no styling, so the class is the check.
describe('GeneralTab destructive key actions', () => {
  it.each([
    ['regenerate', 'Regenerate'],
    ['rotateSessionSecret', 'Rotate session secret'],
  ])('%s is a btn.danger outline, not a filled button', (handler, label) => {
    const button = source.match(new RegExp(`<button onClick=\\{${handler}\\}[^>]*>[\\s\\S]*?${label}`))?.[0]
    expect(button).toBeDefined()
    expect(button).toContain('${btn.danger}')
    expect(button).not.toContain('bg-amber-600')
  })
})
