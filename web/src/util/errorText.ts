// errorText is the text to show for a caught error: its own message when it
// is an Error, otherwise the caller's (translated) fallback.
export function errorText(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}
