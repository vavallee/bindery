import { useEffect, useId, useMemo, useRef, useState } from 'react'
import type { CSSProperties, ReactNode } from 'react'

interface MarkdownDescriptionProps {
  text: string
  showMoreLabel: string
  showLessLabel: string
  className?: string
  collapsedLines?: number
}

const referenceDefinitionRE = /^\s*\[[^\]]+\]:\s+\S+/
const sourcesLineRE = /^\s*\(Sources?:\s*.*\)\s*$/i
const inlineTokenRE = /\[([^\]]+)\]\[[^\]]+\]|\*\*([^*\n]+?)\*\*|\*([^*\n]+?)\*/g

function cleanMarkdownDescription(text: string): string {
  return text
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .split('\n')
    .filter(line => !referenceDefinitionRE.test(line) && !sourcesLineRE.test(line))
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
      nodes.push(
        <span key={`${keyPrefix}-ref-${index}`}>
          {parseInlineMarkdown(match[1], `${keyPrefix}-ref-${index}`)}
        </span>,
      )
    } else if (match[2] !== undefined) {
      nodes.push(<strong key={`${keyPrefix}-strong-${index}`}>{match[2]}</strong>)
    } else if (match[3] !== undefined) {
      nodes.push(<em key={`${keyPrefix}-em-${index}`}>{match[3]}</em>)
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
