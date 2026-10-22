import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import MarkdownDescription from './MarkdownDescription'

const renderText = (text: string) =>
  render(<MarkdownDescription text={text} showMoreLabel="more" showLessLabel="less" />).container.textContent ?? ''

describe('MarkdownDescription citation noise', () => {
  // The OpenLibrary bio for George Orwell showed "(Source.)" and a raw
  // "[1]:https://en.wikipedia.org/…" line: the definition has no space after
  // the colon, and the citation is a reference link, not "(Sources: …)".
  it('drops a reference definition with no space after the colon', () => {
    const out = renderText('Eric Arthur Blair was an English novelist.\n\n([Source][1].)\n\n[1]:https://en.wikipedia.org/wiki/George_Orwell')
    expect(out).toContain('Eric Arthur Blair was an English novelist.')
    expect(out).not.toContain('Source')
    expect(out).not.toContain('wikipedia.org')
  })

  it('drops a bare "(Source.)" line and one trailing the last sentence', () => {
    expect(renderText('A novelist.\n(Source.)')).not.toContain('Source')
    expect(renderText('A novelist. ([Source][1])\n\n[1]: https://example.org')).not.toContain('Source')
    expect(renderText('A novelist. (Source.)')).not.toContain('Source')
  })

  it('keeps a parenthesised "source" that ends a sentence of prose', () => {
    expect(renderText('They followed the river to its (source)')).toContain('(source)')
    expect(renderText('They followed the river to its (Sources)')).toContain('(Sources)')
  })

  it('keeps a parenthesis that is not a citation', () => {
    expect(renderText('He wrote 1984 (published in 1949).')).toContain('(published in 1949)')
  })

  it('keeps prose that only looks like a reference definition', () => {
    expect(renderText('[Note]:important context follows.')).toContain('[Note]:important')
    expect(renderText('[1]:www.example.org')).not.toContain('example.org')
  })

  // The trailing-citation pattern used to start with an unanchored \s*, which
  // rescanned every whitespace run: a 50,000 space line took about 2.4 s.
  // Each input took seconds with two whitespace runs side by side in a pattern.
  it.each([
    ['plain text', 'x' + ' '.repeat(50_000) + 'y'],
    ['an unclosed citation', '(Source' + ' '.repeat(50_000)],
    ['a citation with text before the paren', '(Source' + ' '.repeat(50_000) + 'x)'],
    ['an unclosed sources line', '(Sources:' + ' '.repeat(50_000)],
    ['a bracketed citation left open', 'x ([Source]' + ' '.repeat(50_000) + '.'],
    ['a dotted citation left open', 'x (Source' + ' '.repeat(50_000) + '.' + ' '.repeat(50_000)],
    ['(Source[ repeated', '(Source['.repeat(50_000)],
    ['(Source[x repeated', '(Source[x'.repeat(50_000)],
  ])('cleans a 50,000 space line with %s in linear time', (_case, line) => {
    const start = performance.now()
    renderText(line)
    expect(performance.now() - start).toBeLessThan(250)
  })
})
