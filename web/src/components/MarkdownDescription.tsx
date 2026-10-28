import { useEffect, useId, useMemo, useRef, useState } from 'react'
import type { CSSProperties, ReactNode } from 'react'
import { safeHref } from '../util/safeHref'

interface MarkdownDescriptionProps {
  text: string
  showMoreLabel: string
  showLessLabel: string
  className?: string
  collapsedLines?: number
}

// Reference definitions, with or without the space after the colon:
// OpenLibrary bios carry both "[1]: https://…" and "[1]:https://…". Without
// the space only a URL counts, so prose such as "[Note]:important" stays.
const referenceDefinitionRE = /^\s*\[[^\]]+\]:(?:\s+\S|(?:https?:\/\/|www\.))/i
// A citation line: "(Sources: …)", or the "([Source][1])" / "(Source.)" form
// Wikipedia-derived bios end with, which renders as a bare "(Source.)".
//
// No two whitespace runs may sit next to each other in these patterns
// (`\s*\.?\s*`, `:\s*.*`): on a long run of spaces the engine tries every way
// to split the run between them, which is quadratic. `\s*(?:\.\s*)?` and a
// bare `.*` match the same text with one way to split it.
const sourcesLineRE = /^\s*\(\s*(?:Sources?:.*|\[?Sources?\]?(?:\[[^\]]*\])?\s*(?:\.\s*)?)\)\s*$/i
// The same citation trailing the last sentence of a paragraph. Mid-line it
// has to look like a citation, a bracketed "[Source]" or a "Source." with its
// period: a sentence that ends "followed the river to its (source)" is prose.
// No leading \s*: unanchored, it made the engine rescan every whitespace run
// (a line of 50,000 spaces took seconds). The space before the citation is
// trimmed after the match instead.
const trailingSourceRE = /\(\s*(?:\[Sources?\](?:\[[^\]]{0,20}\])?\s*(?:\.\s*)?|Sources?\s*\.\s*)\)\s*$/i
// Inline tokens, in priority order: [text](url) inline link, [text][ref]
// reference link (rendered as plain text — the [ref]: defs are stripped),
// **bold**, *italic*. Link text may not contain `[`: with it allowed, a run of
// unclosed `[` made every one rescan to the end of the line (quadratic).
const inlineTokenRE = /\[([^\][]+)\]\(([^)\s]+)\)|\[([^\][]+)\]\[[^\]]+\]|\*\*([^*\n]+?)\*\*|\*([^*\n]+?)\*/g

function cleanMarkdownDescription(text: string): string {
  return text
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .split('\n')
    .filter(line => !referenceDefinitionRE.test(line) && !sourcesLineRE.test(line))
    .map(line => (trailingSourceRE.test(line) ? line.replace(trailingSourceRE, '').trimEnd() : line))
    .join('\n')
    .trim()
}

function descriptionParagraphs(text: string): string[] {
  const cleaned = cleanMarkdownDescription(text)
  if (!cleaned) return []
  return cleaned
    .split(/\n\s*\n/)
    .map(paragraph => paragraph.split('\n').map(line => line.trim()).filter(Boolean).join(' '))
    .filter(Boolean)
}

function parseInlineMarkdown(text: string, keyPrefix: string): ReactNode[] {
  const nodes: ReactNode[] = []
  let lastIndex = 0

  for (const match of text.matchAll(inlineTokenRE)) {
    const index = match.index ?? 0
    if (index > lastIndex) {
      nodes.push(text.slice(lastIndex, index))
    }

    if (match[1] !== undefined) {
      // [text](url) inline link. Render an anchor only for safe http(s) URLs;
      // otherwise fall back to the link text so nothing dangerous is emitted.
      const href = safeHref(match[2])
      const inner = parseInlineMarkdown(match[1], `${keyPrefix}-link-${index}`)
      nodes.push(
        href ? (
          <a
            key={`${keyPrefix}-link-${index}`}
            href={href}
            target="_blank"
            rel="noopener noreferrer nofollow"
            className="text-accent-text underline hover:no-underline"
          >
            {inner}
          </a>
        ) : (
          <span key={`${keyPrefix}-link-${index}`}>{inner}</span>
        ),
      )
    } else if (match[3] !== undefined) {
      nodes.push(
        <span key={`${keyPrefix}-ref-${index}`}>
          {parseInlineMarkdown(match[3], `${keyPrefix}-ref-${index}`)}
        </span>,
      )
    } else if (match[4] !== undefined) {
      nodes.push(<strong key={`${keyPrefix}-strong-${index}`}>{match[4]}</strong>)
    } else if (match[5] !== undefined) {
      nodes.push(<em key={`${keyPrefix}-em-${index}`}>{match[5]}</em>)
    }

    lastIndex = index + match[0].length
  }

  if (lastIndex < text.length) {
    nodes.push(text.slice(lastIndex))
  }

  return nodes.length > 0 ? nodes : [text]
}

export default function MarkdownDescription({
  text,
  showMoreLabel,
  showLessLabel,
  className = '',
  collapsedLines = 6,
}: MarkdownDescriptionProps) {
  const contentId = useId()
  const contentRef = useRef<HTMLDivElement>(null)
  const paragraphs = useMemo(() => descriptionParagraphs(text), [text])
  const [expanded, setExpanded] = useState(false)
  const [canExpand, setCanExpand] = useState(false)
  const collapsedStyle: CSSProperties | undefined = expanded
    ? undefined
    : {
        display: '-webkit-box',
        WebkitLineClamp: collapsedLines,
        WebkitBoxOrient: 'vertical',
        overflow: 'hidden',
      }

  useEffect(() => {
    setExpanded(false)
    setCanExpand(false)
  }, [text])

  useEffect(() => {
    if (expanded) return

    const measure = () => {
      const el = contentRef.current
      if (!el) return
      setCanExpand(el.scrollHeight > el.clientHeight + 1)
    }

    measure()
    const frame = window.requestAnimationFrame(measure)
    const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(measure) : null
    if (contentRef.current) observer?.observe(contentRef.current)
    window.addEventListener('resize', measure)

    return () => {
      window.cancelAnimationFrame(frame)
      observer?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [expanded, paragraphs])

  if (paragraphs.length === 0) return null

  return (
    <div className={className}>
      <div
        id={contentId}
        ref={contentRef}
        style={collapsedStyle}
        className="text-sm text-slate-700 dark:text-zinc-300 leading-relaxed"
      >
        {paragraphs.map((paragraph, index) => (
          <span key={index}>
            {index > 0 && (
              <>
                <br />
                <br />
              </>
            )}
            {parseInlineMarkdown(paragraph, `paragraph-${index}`)}
          </span>
        ))}
      </div>
      {canExpand && (
        <button
          type="button"
          aria-controls={contentId}
          aria-expanded={expanded}
          onClick={() => setExpanded(value => !value)}
          className="mt-1 text-xs font-medium text-slate-600 hover:text-slate-900 dark:text-zinc-400 dark:hover:text-white"
        >
          {expanded ? showLessLabel : showMoreLabel}
        </button>
      )}
    </div>
  )
}
