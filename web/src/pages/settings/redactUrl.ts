// A URL saved as http://user:pass@host/path must not put the credentials on
// screen or in the tooltip. Userinfo is dropped, and so is the query and
// fragment of such a URL (they are where tokens usually sit); the caller then
// leaves the full text out of the title.
const URL_LIKE = /^([a-z][a-z0-9+.-]*:\/\/)([^/?#]*)([^?#]*)/i
export function redactUrlUserinfo(value: string): { text: string; redacted: boolean } {
  const m = URL_LIKE.exec(value)
  if (!m || !m[2].includes('@')) return { text: value, redacted: false }
  return { text: `${m[1]}${m[2].slice(m[2].lastIndexOf('@') + 1)}${m[3]}`, redacted: true }
}
