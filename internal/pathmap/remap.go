// Package pathmap rewrites paths between external service mount points and
// Bindery-visible mount points.
package pathmap

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Remapper rewrites paths according to comma-separated from:to prefix rules.
// Rules are applied by longest source prefix first, so more-specific mappings
// win over broader ones regardless of declaration order.
type Remapper struct {
	rules []remapRule
}

type remapRule struct {
	from string
	to   string
}

// Parse accepts a comma-separated list of `from:to` pairs, e.g.
// `/downloads:/media,/srv/sab:/mnt/sab`. Empty or malformed entries are
// skipped. A nil-safe zero Remapper is returned on empty input.
func Parse(spec string) *Remapper {
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
		from := cleanPrefix(entry[:colon])
		to := cleanPrefix(entry[colon+1:])
		if from == "" || to == "" {
			continue
		}
		r.rules = append(r.rules, remapRule{from: from, to: to})
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
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("pair %d %q is not in 'from:to' format", i+1, pair)
		}
	}
	return nil
}

// Apply rewrites p according to the first matching from prefix. A rule matches
// when p is exactly the source prefix or when it starts with source prefix + "/".
// If no rule matches, p is returned unchanged.
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

// ApplyInverse rewrites p in the opposite direction, from a Bindery-visible
// path back to the external service mount point.
func (r *Remapper) ApplyInverse(p string) string {
	if r == nil || len(r.rules) == 0 || p == "" {
		return p
	}
	var best *remapRule
	for _, rule := range r.rules {
		if p == rule.to || strings.HasPrefix(p, rule.to+"/") {
			if best == nil || len(rule.to) > len(best.to) {
				rule := rule
				best = &rule
			}
		}
	}
	if best != nil {
		if p == best.to {
			return best.from
		}
		return filepath.Join(best.from, strings.TrimPrefix(p, best.to))
	}
	return p
}

// Empty reports whether the remapper has no rules.
func (r *Remapper) Empty() bool {
	return r == nil || len(r.rules) == 0
}

func (r *Remapper) sort() {
	for i := 1; i < len(r.rules); i++ {
		for j := i; j > 0 && len(r.rules[j].from) > len(r.rules[j-1].from); j-- {
			r.rules[j], r.rules[j-1] = r.rules[j-1], r.rules[j]
		}
	}
}

func cleanPrefix(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, "/")
	if value == "" {
		return ""
	}
	return value
}
