import type { KeyboardEvent, MouseEvent } from 'react'

// Selection mode on a card grid: while anything is selected, a plain click on
// a card's link adds or removes that card instead of opening it, so a click
// that misses the small checkbox no longer navigates away and loses the
// selection (#2456, #3960). With nothing selected the click opens the card as
// before. Modified clicks (ctrl/cmd/shift/alt) pass through so open in a new
// tab keeps working; middle click never reaches onClick. Returns true when it
// handled the click.
export function selectingClick(e: MouseEvent, selecting: boolean, toggle: () => void): boolean {
  if (!selecting) return false
  if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false
  e.preventDefault()
  toggle()
  return true
}

// The keyboard half: Enter or Space on a focused card toggles it while
// selecting. Preventing the keydown stops the link activating, so Enter does
// not also produce a click. Outside selection mode the key is left alone and
// Enter opens the card as a link always has.
export function selectingKeyDown(e: KeyboardEvent, selecting: boolean, toggle: () => void): boolean {
  if (!selecting) return false
  if (e.key !== 'Enter' && e.key !== ' ') return false
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false
  e.preventDefault()
  toggle()
  return true
}

// Both handlers, ready to spread onto a card's <Link>.
export function selectingCardProps(selecting: boolean, toggle: () => void) {
  return {
    onClick: (e: MouseEvent) => { selectingClick(e, selecting, toggle) },
    onKeyDown: (e: KeyboardEvent) => { selectingKeyDown(e, selecting, toggle) },
  }
}
