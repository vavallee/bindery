import { describe, it, expect } from 'vitest'
import { redactUrlUserinfo } from './redactUrl'

const shown = (v: string) => redactUrlUserinfo(v).text

describe('redactUrlUserinfo (#3989)', () => {
  const cases: Array<[string, string]> = [
    ['http://u:p@h/x', 'http://h/x'],
    ['https://user:pa55@host.example:8080/api/v1?token=abc#frag', 'https://host.example:8080/api/v1'],
    ['//u:p@h/x', '//h/x'],
    ['u:p@h', 'h'],
    ['u:p@h/p?q=1', 'h/p'],
    ['  http://u:p@h/', 'http://h/'],
    ['http://u:p@h/  ', 'http://h/'],
    ['http://h/a?token=abc', 'http://h/a'],
    ['http://h/a#token=abc', 'http://h/a'],
    ['http://h/a?x=a@b', 'http://h/a'],
    ['http://h?x=a@b', 'http://h'],
    ['HTTP://U:P@Host/x', 'HTTP://Host/x'],
    ['ftp://u:p@files.example/pub', 'ftp://files.example/pub'],
    ['http://u:p@[::1]:8080/x', 'http://[::1]:8080/x'],
    ['http://us%40er:p@h/x', 'http://h/x'],
    ['postgres://dbuser:dbpass@db:5432/bindery?sslmode=disable', 'postgres://db:5432/bindery'],
    ['http://tok@h/x', 'http://h/x'],
    ['http://a:b@c@h/x', 'http://h/x'],
    // Left alone: an @ in the path is not userinfo.
    ['http://h/users/@me', 'http://h/users/@me'],
    ['http://calibre:8099/lib', 'http://calibre:8099/lib'],
  ]
  it.each(cases)('%s shows %s', (input, expected) => {
    expect(shown(input)).toBe(expected)
  })

  it('never leaves a credential or query token in any shape above', () => {
    for (const [input] of cases) {
      expect(shown(input)).not.toMatch(/pa55|dbpass|:p@|token|u:p|a@b|tok@|us%40er/)
    }
  })

  it('flags changed values, so no title is offered for them', () => {
    expect(redactUrlUserinfo('http://u:p@h/').redacted).toBe(true)
    expect(redactUrlUserinfo('http://h/a?x=1').redacted).toBe(true)
    expect(redactUrlUserinfo('http://h/a').redacted).toBe(false)
  })

  it('treats credentials with no host as a secret', () => {
    expect(redactUrlUserinfo('http://u:p@')).toMatchObject({ secret: true, redacted: true })
    expect(redactUrlUserinfo('u:p@')).toMatchObject({ secret: true })
  })

  it('leaves free text, emails and plain values alone', () => {
    expect(shown('a plain sentence with u:p@h in it')).toBe('a plain sentence with u:p@h in it')
    expect(shown('me@example.com')).toBe('me@example.com')
    expect(shown('/data/books')).toBe('/data/books')
    expect(shown('12h')).toBe('12h')
  })
})
