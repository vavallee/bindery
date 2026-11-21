// Files that may still write a raw palette colour (text-red-700, bg-indigo-100,
// bg-[#9c6363] and the like) instead of a colour token from src/index.css
// (#3994). Each is a colour that is not one of the status roles: a category, a
// brand or a decoration, or a state that has no role yet. semanticColourGuard
// .test.ts fails when any other file in src adds one, and when an entry here no
// longer needs its place, so the list only shrinks.
//
// Paths are relative to src.
export const RAW_STATUS_ALLOWLIST: Record<string, string> = {
  'components/MediaBadge.tsx':
    'Ebook, audiobook and both are told apart by hue (emerald, indigo, violet). They are categories, not states, so they must not share one token.',
  'components/PackLabel.tsx':
    'The "pack" tag on a search result marks a kind of release. It is amber to stand out, not to warn.',
  'components/SoulseekRelease.tsx':
    'The Soulseek badge marks a source, in the sky hue. It is a label, not a status.',
  'components/RecommendationCard.tsx':
    'The filled rating stars are decoration beside a rating that is also written as text.',
  'components/coverPalette.ts':
    'The cover placeholder palette is a set of decorative hex grounds, each pinned by its own contrast tests.',
  'components/downloadStatus.ts':
    'The external import and held queue chips (indigo, violet) are two non-terminal states told apart from the blue and sky chips by hue; no role stands for them yet.',
  'pages/HistoryPage.tsx':
    'The purple rename events and the indigo audiobook badge are a category and an event kind, not a status role.',
  'pages/BookDetailPage.tsx':
    'The purple rename dot and the indigo Audible enrich button are an event kind and a one-off action colour, not a status role.',
  'pages/AuthorDetailPage.tsx': 'The indigo audiobook badge is a media category, like MediaBadge.',
  'pages/BooksPage.tsx': 'The indigo audiobook label is a media category, like MediaBadge.',
  'pages/settings/FormatList.tsx': 'The indigo audiobook format chip is a media category, like MediaBadge.',
}
