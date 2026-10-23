import type { TestingLibraryMatchers } from '@testing-library/jest-dom/matchers'

// vitest 5 changed Assertion to Assertion<R extends void | Promise<void>, T>,
// and @testing-library/jest-dom still augments the old Assertion<T = any>, so
// its matchers stop type-checking. Re-augment the new shape here.
declare module 'vitest' {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any, @typescript-eslint/no-empty-object-type, @typescript-eslint/no-unused-vars
  interface Assertion<R extends void | Promise<void> = void, T = unknown> extends TestingLibraryMatchers<any, R> {}
  // eslint-disable-next-line @typescript-eslint/no-explicit-any, @typescript-eslint/no-empty-object-type
  interface AsymmetricMatchersContaining extends TestingLibraryMatchers<any, void> {}
}
