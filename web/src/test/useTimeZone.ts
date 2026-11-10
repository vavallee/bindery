import { afterAll, beforeAll } from 'vitest'

// Node re-reads TZ when process.env.TZ is assigned, so a describe block can
// run under a chosen zone and restore the previous one afterwards. The
// project's tsconfig carries no Node types, hence the local declaration.
declare const process: { env: Record<string, string | undefined> }

export function useTimeZone(tz: string) {
  const prev = process.env.TZ
  beforeAll(() => {
    process.env.TZ = tz
  })
  afterAll(() => {
    if (prev === undefined) delete process.env.TZ
    else process.env.TZ = prev
  })
}
