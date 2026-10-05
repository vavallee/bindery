// Package pathmap rewrites paths between external service mount points and
// Bindery-visible mount points.
package pathmap

import (
	"fmt"
	"path"
	"strings"
)

// Remapper rewrites paths according to comma-separated from:to prefix rules.
// Rules are applied by longest source prefix first, so more-specific mappings
// win over broader ones regardless of declaration order.
type Remapper struct {
	rules []remapRule
}

// remapRule is one from:to prefix pair. Either side may be a Windows path
// (`S:\Downloads`, or a network share such as `\\nas\books`), which is
// matched case-insensitively and treats `\` and `/` as interchangeable
// separators, the way Windows itself does. POSIX sides stay
// case-sensitive and `/`-only.
type remapRule struct {
	from string
	to   string

	fromWin bool
	toWin   bool

	// fromSep/toSep are the separator the operator wrote on that side, so a
	// path rebuilt onto a Windows side keeps the style it was configured with.
	fromSep string
	toSep   string

	// fromKey/toKey are the comparison forms: for a Windows side, separators
	// folded to `/` and ASCII letters folded to lower case. Folding preserves
	// byte length, so an index into the key is also an index into the original.
	fromKey string
	toKey   string
}

// Parse accepts a comma-separated list of `from:to` pairs, e.g.
// `/downloads:/media,/srv/sab:/mnt/sab`. Windows drive letters are understood
// on either side (`S:\Downloads:/downloads`): the colon of a drive designator
// is not treated as the pair separator. Network shares (`\\nas\books`,
// `//nas/books`) count as Windows sides too. Empty or malformed entries are
// skipped. A nil-safe zero Remapper is returned on empty input.
func Parse(spec string) *Remapper {
	r := &Remapper{}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		rawFrom, rawTo, ok := splitPair(entry)
		if !ok {
			continue
		}
		fromWin, toWin := isWindowsSide(rawFrom), isWindowsSide(rawTo)
		from := cleanPrefix(rawFrom, fromWin)
		to := cleanPrefix(rawTo, toWin)
		if from == "" || to == "" {
			continue
		}
		r.rules = append(r.rules, remapRule{
			from:    from,
			to:      to,
			fromWin: fromWin,
			toWin:   toWin,
			fromSep: separatorStyle(rawFrom),
			toSep:   separatorStyle(rawTo),
			fromKey: matchKey(from, fromWin),
			toKey:   matchKey(to, toWin),
		})
	}
	r.sort()
	return r
}

// Validate checks that spec is a comma-separated list of non-empty from:to
// pairs. It validates format only, not filesystem existence.
func Validate(spec string) error {
	for i, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		from, to, ok := splitPair(pair)
		if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			if isWindowsSide(pair) {
				// The commonest form of this mistake is pasting only the
				// client-side Windows path and expecting Bindery to infer the
				// rest, so name the working shape rather than just refusing.
				if !IsWindowsPath(pair) {
					return fmt.Errorf(`pair %d %q is not in 'from:to' format; a network share needs the Bindery-visible path after it, e.g. '\\nas\books:/books'`, i+1, pair)
				}
				return fmt.Errorf(`pair %d %q is not in 'from:to' format; a Windows path needs the Bindery-visible path after it, e.g. 'S:\Downloads:/downloads'`, i+1, pair)
			}
			return fmt.Errorf("pair %d %q is not in 'from:to' format", i+1, pair)
		}
		for _, side := range []string{from, to} {
			if isIncompleteShare(side) {
				return fmt.Errorf(`pair %d %q: %q is not a complete network share; write the server and the share, e.g. '\\nas\books'`, i+1, pair, strings.TrimSpace(side))
			}
		}
	}
	return nil
}

// Apply rewrites p according to the first matching from prefix. A rule matches
// when p is exactly the source prefix or when p continues past it at a
// separator boundary. A Windows source matches case-insensitively and accepts
// either separator, so a client that reports `S:/Downloads/Book` still matches
// a rule written as `S:\Downloads`. The result is rendered for the destination
// platform: a Windows source mapped to a POSIX destination yields forward
// slashes. If no rule matches, p is returned unchanged.
func (r *Remapper) Apply(p string) string {
	out, _ := r.ApplyMatched(p)
	return out
}

// ApplyMatched is Apply that also reports whether a rule matched. A rule that
// maps a prefix onto itself (`/downloads:/downloads`) matches while leaving
// the path unchanged, which comparing Apply's result with p cannot tell apart
// from no rule matching at all.
func (r *Remapper) ApplyMatched(p string) (string, bool) {
	if r == nil || len(r.rules) == 0 || p == "" {
		return p, false
	}
	for _, rule := range r.rules {
		rest, ok := matchPrefix(p, rule.fromKey, rule.fromWin)
		if !ok {
			continue
		}
		if rest == "" {
			return rule.to, true
		}
		return joinRemainder(rule.to, rule.toWin, rule.toSep, rest, rule.fromWin), true
	}
	return p, false
}

// ApplyInverse rewrites p in the opposite direction, from a Bindery-visible
// path back to the external service mount point. It round-trips Apply,
// including the separator style the Windows side was configured with.
func (r *Remapper) ApplyInverse(p string) string {
	out, _ := r.ApplyInverseMatched(p)
	return out
}

// ApplyInverseMatched is ApplyInverse that also reports whether a rule
// matched, for the same reason as ApplyMatched.
func (r *Remapper) ApplyInverseMatched(p string) (string, bool) {
	if r == nil || len(r.rules) == 0 || p == "" {
		return p, false
	}
	var best *remapRule
	var bestRest string
	for i := range r.rules {
		rule := &r.rules[i]
		rest, ok := matchPrefix(p, rule.toKey, rule.toWin)
		if !ok {
			continue
		}
		if best == nil || len(rule.to) > len(best.to) {
			best, bestRest = rule, rest
		}
	}
	if best == nil {
		return p, false
	}
	if bestRest == "" {
		return best.from, true
	}
	return joinRemainder(best.from, best.fromWin, best.fromSep, bestRest, best.toWin), true
}

// Empty reports whether the remapper has no rules.
func (r *Remapper) Empty() bool {
	return r == nil || len(r.rules) == 0
}

// IsWindowsPath reports whether p starts with a Windows drive designator — a
// single ASCII letter, a colon, then a separator (`S:\Downloads`, `S:/Downloads`).
// Callers use it to explain that a remap is mandatory when a download client
// reports a drive path that cannot exist on Bindery's filesystem.
func IsWindowsPath(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) < 3 || p[1] != ':' {
		return false
	}
	if !isASCIILetter(p[0]) {
		return false
	}
	return p[2] == '\\' || p[2] == '/'
}

// IsUNCPath reports whether p is a Windows network share path with both a
// server and a share segment: `\\nas\books`, `//nas/books`, or the extended
// form `\\?\UNC\nas\books`. A forward-slash path needs both segments to
// count, so a POSIX path such as `//data` stays POSIX.
func IsUNCPath(p string) bool {
	host, share, ok := uncParts(strings.TrimSpace(p))
	return ok && host != "" && share != ""
}

// isWindowsSide reports whether one side of a remap rule lives in a Windows
// namespace: a drive letter, a network share, or anything opening with `\\`
// (an extended `\\?\C:\` path, or a share missing its share segment, which
// Validate rejects). A leading `\\` is never a meaningful POSIX prefix.
func isWindowsSide(p string) bool {
	p = strings.TrimSpace(p)
	return IsWindowsPath(p) || IsUNCPath(p) || strings.HasPrefix(p, `\\`)
}

// uncParts splits a share path into its server and share segments. ok is
// false when p does not open with two separators, or when it is an
// extended local path (`\\?\C:\`, `\\.\device`) rather than a share.
func uncParts(p string) (host, share string, ok bool) {
	if len(p) < 3 || !isSep(p[0]) || !isSep(p[1]) {
		return "", "", false
	}
	rest := p[2:]
	if len(rest) >= 6 && matchKey(rest[:6], true) == "?/unc/" {
		rest = rest[6:]
	}
	host, rest, _ = cutSep(rest)
	if host == "?" || host == "." {
		return "", "", false
	}
	share, _, _ = cutSep(rest)
	return host, share, true
}

// isIncompleteShare reports a side that opens with `\\` but is missing the
// server or the share (`\\nas`, `\\nas\`, `\\?\UNC\nas`), which Windows
// cannot open as a folder. Extended local paths (`\\?\C:\`) are left alone.
func isIncompleteShare(p string) bool {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, `\\`) {
		return false
	}
	host, share, ok := uncParts(p)
	if !ok && len(p) >= 4 && (p[2] == '?' || p[2] == '.') && isSep(p[3]) {
		return false
	}
	return !ok || host == "" || share == ""
}

func cutSep(s string) (before, after string, found bool) {
	if i := strings.IndexAny(s, `/\`); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

func isSep(c byte) bool { return c == '/' || c == '\\' }

func (r *Remapper) sort() {
	for i := 1; i < len(r.rules); i++ {
		for j := i; j > 0 && len(r.rules[j].from) > len(r.rules[j-1].from); j-- {
			r.rules[j], r.rules[j-1] = r.rules[j-1], r.rules[j]
		}
	}
}

// splitPair splits an entry into its from and to halves at the first colon that
// is not part of a Windows drive designator. Reported ok only when both halves
// are non-empty.
func splitPair(entry string) (string, string, bool) {
	start := 0
	switch {
	case IsWindowsPath(entry):
		// Skip past `X:` so the drive colon is never the split point.
		start = 2
	case isWindowsSide(entry) && len(entry) > 4 && entry[2] == '?' && isSep(entry[3]) && IsWindowsPath(entry[4:]):
		// The same for an extended drive path, `\\?\X:\...`.
		start = 6
	}
	colon := strings.Index(entry[start:], ":")
	if colon < 0 {
		return "", "", false
	}
	colon += start
	from, to := entry[:colon], entry[colon+1:]
	if from == "" || to == "" {
		return "", "", false
	}
	return from, to, true
}

// matchPrefix reports whether p sits at or under the prefix key, returning the
// remainder (which starts with a separator) when it does. The remainder is
// sliced out of p, so it keeps p's original separators and case.
func matchPrefix(p, key string, win bool) (string, bool) {
	probe := matchKey(p, win)
	if probe == key {
		return "", true
	}
	if strings.HasPrefix(probe, key+"/") {
		return p[len(key):], true
	}
	return "", false
}

// matchKey folds a path into its comparison form. Windows paths fold `\` to `/`
// and ASCII upper case to lower; POSIX paths are returned verbatim because
// Linux filesystems are case-sensitive and `\` is an ordinary filename byte.
// The fold is byte-for-byte, so key offsets stay valid in the original string.
func matchKey(p string, win bool) string {
	if !win {
		return p
	}
	b := []byte(p)
	for i := range b {
		switch {
		case b[i] == '\\':
			b[i] = '/'
		case b[i] >= 'A' && b[i] <= 'Z':
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// joinRemainder appends rest to target. rest is normalised first when it came
// off a Windows source, because filepath.Join on a Linux binary does not treat
// `\` as a separator and would otherwise bake it into the POSIX result.
//
// A share target keeps its two leading separators out of path.Join, which
// would otherwise collapse `//nas/books` to `/nas/books`, no longer a share.
func joinRemainder(target string, targetWin bool, targetSep, rest string, sourceWin bool) string {
	if sourceWin {
		rest = strings.ReplaceAll(rest, `\`, "/")
	}
	if !targetWin {
		return path.Join(target, rest)
	}
	slashed := strings.ReplaceAll(target, `\`, "/")
	lead := ""
	if strings.HasPrefix(slashed, "//") {
		lead, slashed = "//", slashed[2:]
	}
	joined := lead + path.Join(slashed, rest)
	if targetSep == `\` {
		return strings.ReplaceAll(joined, "/", `\`)
	}
	return joined
}

// separatorStyle reports the separator the operator wrote, so a rebuilt Windows
// path is handed back in the shape it was configured with.
func separatorStyle(value string) string {
	if strings.Contains(value, `\`) {
		return `\`
	}
	return "/"
}

func cleanPrefix(value string, win bool) string {
	value = strings.TrimSpace(value)
	if win {
		value = strings.TrimRight(value, `/\`)
	} else {
		value = strings.TrimRight(value, "/")
	}
	if value == "" {
		return ""
	}
	return value
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
