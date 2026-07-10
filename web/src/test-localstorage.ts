// Deterministic in-memory localStorage, installed before any other setup file
// (and before msw is imported). Node's experimental WebStorage — enabled on
// some CI runners — exposes a native `globalThis.localStorage` getter that
// returns undefined (or throws) unless --localstorage-file is passed, shadowing
// jsdom's implementation. That made tests touching `localStorage` pass locally
// but fail in CI. Installing our own polyfill first removes the dependency on
// whichever storage backend the runtime happens to expose.
const store = new Map<string, string>()

const storage: Storage = {
  get length() {
    return store.size
  },
  clear: () => store.clear(),
  getItem: key => (store.has(key) ? store.get(key)! : null),
  key: index => Array.from(store.keys())[index] ?? null,
  removeItem: key => {
    store.delete(key)
  },
  setItem: (key, value) => {
    store.set(String(key), String(value))
  },
}

Object.defineProperty(globalThis, 'localStorage', { value: storage, configurable: true })
if (typeof window !== 'undefined') {
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
}
