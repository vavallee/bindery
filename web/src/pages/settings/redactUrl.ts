// A URL saved as http://user:pass@host/path?token=x must not put the
// credentials on screen or in a tooltip (#3989). For a value that looks like a
// URL this shows scheme://host/path and nothing else:
//   - userinfo is always stripped;
//   - the query and fragment are always dropped, whether or not there was
//     userinfo, because tokens ride there too;
//   - leading and trailing whitespace is ignored, so it cannot defeat the match.
// URL looking means scheme://..., //host/..., or a scheme-less user:pass@host
// (userinfo containing a colon, so a plain email address is left alone).
// Anything with spaces is free text and is left alone. If the value carries
// credentials but no host can be found (user:pass@), it is treated like a
// secret and the caller shows "Set".

export interface RedactedValue {
  text: string
  /** The text differs from what was saved, so a title carrying it is not offered. */
  redacted: boolean
  /** Credentials with nothing safe to show: render as a secret. */
  secret: boolean
}

const SCHEME_URL = /^([a-z][a-z0-9+.-]*:)?\/\/(.*)$/i
const BARE_USERINFO = /^[^\s/?#@]*:[^\s/?#@]*@.*$/

export function redactUrlUserinfo(value: string): RedactedValue {
  const v = value.trim()
  if (v === '' || /\s/.test(v)) return { text: value, redacted: false, secret: false }

  let prefix = ''
  let rest: string
  const m = SCHEME_URL.exec(v)
  if (m) {
    prefix = `${m[1] ?? ''}//`
    rest = m[2]
  } else if (BARE_USERINFO.test(v)) {
    rest = v
  } else {
    return { text: v === value ? value : v, redacted: false, secret: false }
  }

  // Authority ends at the first / ? or #; path is what follows up to ? or #.
  const end = rest.search(/[/?#]/)
  const authority = end === -1 ? rest : rest.slice(0, end)
  const afterAuthority = end === -1 ? '' : rest.slice(end)
  const at = authority.lastIndexOf('@')
  const hasUserinfo = at !== -1
  const host = hasUserinfo ? authority.slice(at + 1) : authority
  const path = afterAuthority.replace(/[?#].*$/s, '')

  if (hasUserinfo && host === '') return { text: '', redacted: true, secret: true }
  const text = `${prefix}${host}${path}`
  return { text, redacted: text !== value, secret: false }
}
