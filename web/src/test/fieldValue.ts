import { expect } from 'vitest'

const CONTROL = 'input, select, textarea, button, [role="group"], [role="radiogroup"], [role="switch"]'

/**
 * Fails when a field value subtitle is not wired to its own control. For every
 * [data-testid="field-value"]:
 *   - some control (input, select, textarea, button, group, radiogroup or
 *     switch) names it in aria-describedby;
 *   - that control sits in the same field block as the subtitle, meaning the
 *     smallest ancestor holding both contains no other subtitle, so a
 *     describedby cannot point across to a neighbouring field's line;
 *   - no control names more than one subtitle.
 * Returns the subtitle texts keyed by id for assertions.
 */
export function expectFieldValuesWired(root: ParentNode): Record<string, string> {
  const out: Record<string, string> = {}
  const subs = Array.from(root.querySelectorAll<HTMLElement>('[data-testid="field-value"]'))
  expect(subs.length).toBeGreaterThan(0)
  const described = Array.from(root.querySelectorAll<HTMLElement>('[aria-describedby]'))
  for (const s of subs) {
    expect(s.id, 'subtitle needs an id').not.toBe('')
    const owners = described.filter(el => el.matches(CONTROL) && el.getAttribute('aria-describedby')!.split(/\s+/).includes(s.id))
    expect(owners.length, `no control describes subtitle ${s.id}`).toBeGreaterThan(0)
    for (const owner of owners) {
      const ids = owner.getAttribute('aria-describedby')!.split(/\s+/)
      const subIds = ids.filter(id => subs.some(x => x.id === id))
      expect(subIds, `control for ${s.id} describes another subtitle`).toEqual([s.id])
      let block: HTMLElement | null = s.parentElement
      while (block && !block.contains(owner)) block = block.parentElement
      expect(block, `${s.id} and its control share no block`).toBeTruthy()
      expect(
        block!.querySelectorAll('[data-testid="field-value"]').length,
        `${s.id} and its control share a block with another subtitle`,
      ).toBe(1)
    }
    out[s.id] = s.textContent ?? ''
  }
  return out
}
