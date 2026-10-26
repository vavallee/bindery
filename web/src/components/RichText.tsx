import { cloneElement, Fragment, isValidElement, type ReactElement } from 'react'

// Renders an already translated string, mapping its <name>text</name> tags to
// the given elements, so a sentence with a link or code span stays one string
// for a translator, such as "Run <code>calibredb</code> on PATH". Anything
// that is not a mapped tag renders as plain text; no HTML is interpreted.
export default function RichText({ text, components }: { text: string; components: Record<string, ReactElement> }) {
  const parts = []
  let last = 0
  for (const m of text.matchAll(/<(\w+)>(.*?)<\/\1>/g)) {
    const el = components[m[1]]
    if (!isValidElement(el)) continue
    parts.push(text.slice(last, m.index), cloneElement(el, { key: m.index }, m[2]))
    last = m.index + m[0].length
  }
  parts.push(text.slice(last))
  return <Fragment>{parts}</Fragment>
}
