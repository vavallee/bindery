/** The id the subtitle for `base` carries; give the control aria-describedby of it. */
export function fieldValueId(base: string): string {
  return `${base}-value`
}

/** aria-describedby value for a control that may already have a description. */
export function describedBy(base: string, existing?: string): string {
  return existing ? `${existing} ${fieldValueId(base)}` : fieldValueId(base)
}
