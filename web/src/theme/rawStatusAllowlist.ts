// Files that may still write a raw status palette class (text-red-700,
// bg-emerald-100, border-amber-300 and the like) instead of a status token
// from src/index.css (#3994). Each is a colour that is not a status: a
// category, a brand or a decoration. semanticColourGuard.test.ts fails when
// any other file in src adds one, and when an entry here no longer needs its
// place, so the list only shrinks.
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
}
