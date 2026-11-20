import { expect } from 'vitest'

/**
 * Fails when a field value subtitle is not wired to a control: every
 * [data-testid="field-value"] must be named by some element's aria-describedby.
 * Returns the subtitle texts keyed by id for assertions.
 */
export function expectFieldValuesWired(root: ParentNode): Record<string, string> {
  const out: Record<string, string> = {}
  const subs = Array.from(root.querySelectorAll<HTMLElement>('[data-testid="field-value"]'))
  expect(subs.length).toBeGreaterThan(0)
  for (const s of subs) {
    expect(s.id, 'subtitle needs an id').not.toBe('')
    const owner = Array.from(root.querySelectorAll<HTMLElement>('[aria-describedby]'))
      .find(el => el.getAttribute('aria-describedby')!.split(/\s+/).includes(s.id))
    expect(owner, `no control describes subtitle ${s.id}`).toBeTruthy()
    out[s.id] = s.textContent ?? ''
  }
  return out
}
