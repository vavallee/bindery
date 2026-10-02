// Sets the theme class before the first paint. This mirrors readInitial()
// in src/theme.ts: a stored choice wins, otherwise the OS preference.
// useTheme applies the same class from an effect, which runs after the
// first paint, so without this the document shows its light background
// for a frame before flipping (index.css sets html.dark's background).
//
// A classic script loaded from index.html's <head>, not a module and not
// inline. A module script is deferred and would run too late to help. An
// inline one is refused by the CSP (script-src 'self'), which is #2911.
// web/src/theme.bootstrap.test.ts asserts this stays in agreement with
// readInitial(), and slices the source between the two markers below, so
// keep them.
/* theme-bootstrap:start */
try {
  var saved = null
  try { saved = localStorage.getItem('bindery.theme') } catch (e) { /* storage blocked */ }
  var dark = saved === 'dark' || (saved !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
} catch (e) { /* leave the light default in place */ }
/* theme-bootstrap:end */
