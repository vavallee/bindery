package importer

import (
	"path/filepath"
	"strings"
)

// Remapper rewrites paths reported by the download client into paths visible
// inside bindery's own filesystem. Needed when the download client and bindery
// run in separate containers/pods that mount the same storage at different
// mount points — SABnzbd may report `/downloads/complete/X` while bindery sees
// the same files at `/media/complete/X`.
//
// Rules are applied by longest source prefix first, so more-specific mappings
// win over broader ones regardless of declaration order.
type Remapper struct {
	rules []remapRule
}

type remapRule struct {
	from string
	to   string
}

// ParseRemap accepts a comma-separated list of `from:to` pairs, e.g.
// `/downloads:/media,/srv/sab:/mnt/sab`. Empty or malformed entries are
// skipped. A nil-safe zero Remapper is returned on empty input; Apply on
// a zero Remapper returns the path unchanged.
func ParseRemap(spec string) *Remapper {
	r := &Remapper{}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		colon := strings.Index(entry, ":")
		if colon <= 0 || colon == len(entry)-1 {
			continue
		}
		from := strings.TrimRight(strings.TrimSpace(entry[:colon]), "/")
		to := strings.TrimRight(strings.TrimSpace(entry[colon+1:]), "/")
		if from == "" || to == "" {
			continue
		}
		r.rules = append(r.rules, remapRule{from: from, to: to})
	}
	// Sort longest prefix first so specific rules beat general ones.
	for i := 1; i < len(r.rules); i++ {
		for j := i; j > 0 && len(r.rules[j].from) > len(r.rules[j-1].from); j-- {
			r.rules[j], r.rules[j-1] = r.rules[j-1], r.rules[j]
		}
	}
	return r
}

// Apply rewrites p according to the first matching rule. A rule matches when
// p is exactly the source prefix or when it starts with source prefix + "/".
// If no rule matches (or the Remapper is nil/empty), p is returned unchanged.
func (r *Remapper) Apply(p string) string {
	if r == nil || len(r.rules) == 0 || p == "" {
		return p
	}
	for _, rule := range r.rules {
		if p == rule.from {
			return rule.to
		}
		if strings.HasPrefix(p, rule.from+"/") {
			return filepath.Join(rule.to, strings.TrimPrefix(p, rule.from))
		}
	}
	return p
}

// Empty reports whether the remapper has no rules.
func (r *Remapper) Empty() bool {
	return r == nil || len(r.rules) == 0
}
