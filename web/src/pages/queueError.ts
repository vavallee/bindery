// Queue error messages can be raw upstream bodies (e.g. an indexer 403 that
// returns a full HTML page). summarizeError strips tags, collapses whitespace,
// and truncates so the queue cell shows a readable one-liner instead of dumping
// thousands of characters of markup; the full text stays available behind the
// details expander in QueuePage.
export const ERROR_SUMMARY_LEN = 200

export function summarizeError(msg: string): string {
  const stripped = msg.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim()
  return stripped.length > ERROR_SUMMARY_LEN ? stripped.slice(0, ERROR_SUMMARY_LEN) + '…' : stripped
}
