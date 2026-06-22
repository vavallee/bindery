// safeHref returns url only when it is an http(s) URL. Strings that reach an
// <a href> from an upstream source we don't control — an indexer's release
// infoUrl, a metadata provider's description link — could otherwise be a
// `javascript:` / `data:` URI and execute on click. Anything that isn't a
// plain http(s) URL collapses to '' so the caller renders no link.
export function safeHref(url: string | undefined | null): string {
  return url && /^https?:\/\//i.test(url) ? url : ''
}
