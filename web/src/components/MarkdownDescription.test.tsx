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
  })

  it('keeps a parenthesis that is not a citation', () => {
    expect(renderText('He wrote 1984 (published in 1949).')).toContain('(published in 1949)')
  })
})
