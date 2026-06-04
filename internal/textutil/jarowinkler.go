package textutil

// JaroWinkler returns the Jaro-Winkler similarity between s and t (range 0–1).
// Both inputs should be pre-normalised (e.g. lowercased) by the caller.
// The Winkler scaling factor p=0.1 is applied for up to 4 common prefix chars.
func JaroWinkler(s, t string) float64 {
	if s == t {
		return 1
	}
	// Index over runes, not bytes: multibyte UTF-8 (CJK, accented Latin) must
	// be compared codepoint-by-codepoint or the scores are meaningless.
	sr, tr := []rune(s), []rune(t)
	sl, tl := len(sr), len(tr)
	if sl == 0 || tl == 0 {
		return 0
	}

	matchWindow := max(sl, tl)/2 - 1
	if matchWindow < 0 {
		matchWindow = 0
	}

	// Stack-allocated scratch for the common case (titles, author names);
	// avoids two heap allocations per call on the library-scan hot path.
	// Inputs longer than the buffer fall back to heap allocation.
	var sBuf, tBuf [256]bool
	var sMatch, tMatch []bool
	if sl <= len(sBuf) {
		sMatch = sBuf[:sl]
	} else {
		sMatch = make([]bool, sl)
	}
	if tl <= len(tBuf) {
		tMatch = tBuf[:tl]
	} else {
		tMatch = make([]bool, tl)
	}
	m := 0
	for i := 0; i < sl; i++ {
		lo := max(0, i-matchWindow)
		hi := min(tl, i+matchWindow+1)
		for j := lo; j < hi; j++ {
			if tMatch[j] || sr[i] != tr[j] {
				continue
			}
			sMatch[i] = true
			tMatch[j] = true
			m++
			break
		}
	}
	if m == 0 {
		return 0
	}

	transpositions := 0
	k := 0
	for i := 0; i < sl; i++ {
		if !sMatch[i] {
			continue
		}
		for !tMatch[k] {
			k++
		}
		if sr[i] != tr[k] {
			transpositions++
		}
		k++
	}

	jaro := (float64(m)/float64(sl) + float64(m)/float64(tl) + float64(m-transpositions/2)/float64(m)) / 3

	prefix := 0
	for i := 0; i < 4 && i < sl && i < tl; i++ {
		if sr[i] != tr[i] {
			break
		}
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}
