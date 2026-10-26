package importer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Post-download language enforcement for audiobooks (#3497).
//
// #2998 holds an EPUB to the author's allowed languages using its dc:language.
// An audiobook release whose name does not say its language got no such check:
// "Dungeon Crawler Carl EspanolNeutro m4b" passed the name filter and imported
// as an English book. This is the same check for audio files, with the same
// allowed set (languageAllowance), the same normalisation and the same
// outcome: the release is blocklisted, the book stays Wanted, nothing is placed.
//
// What differs is the evidence. Audio files rarely declare a language, so
// absence of a tag is never a verdict, and a folder of tracks is judged by the
// majority of the tracks that do declare one, so one mistagged track does not
// block a release. A manual import is never refused, as for EPUBs.

// maxLanguageTagReads bounds how many tracks are read for a language. The
// verdict is a majority, so an even sample decides it as well as every file.
const maxLanguageTagReads = 50

// languageTagReader reads one file's tags for the language check. A var so a
// test can count and record the reads.
var languageTagReader = ReadAudioTags

// checkAudiobookLanguage compares the languages the tracks of an audiobook
// download declare with the languages its book may be in.
//
// Each audio file counts as allowed when ANY language it declares is allowed,
// as disallowed when it declares at least one specific language and none is
// allowed, and as unknown (no vote) when it declares none or only "und", "mul"
// and the like. The release is rejected only when files voted and the
// disallowed ones are a strict majority of the votes; a tie passes. A release
// where no file declares a language, the usual case, is never rejected.
//
// Unwired (no metadata profile repo), with no book, or with every language
// allowed, it never objects.
func (s *Scanner) checkAudiobookLanguage(ctx context.Context, book *models.Book, author *models.Author, files []string) languageCheck {
	if s.metadataProfiles == nil || book == nil {
		return languageCheck{}
	}
	lc, profileAllowed, source := s.languageAllowance(ctx, book, author)
	if len(profileAllowed) == 0 {
		return languageCheck{}
	}
	audio := make([]string, 0, len(files))
	for _, f := range files {
		if IsAudioTagFile(f) {
			audio = append(audio, f)
		}
	}
	if len(audio) > maxLanguageTagReads {
		audio = sampleEvenly(audio, maxLanguageTagReads)
	}
	budgetCtx, cancel := context.WithTimeout(ctx, contentCheckBudget)
	defer cancel()

	allowedVotes, disallowedVotes := 0, 0
	for _, f := range audio {
		if budgetCtx.Err() != nil {
			// Out of time or cancelled: an incomplete sample is not a verdict.
			return languageCheck{allowed: lc.allowed}
		}
		// Not s.audioTags: that hook counts the content check's reads, which
		// this check does not add to.
		t, err := languageTagReader(f)
		if err != nil {
			continue
		}
		var langs []string
		for _, l := range strings.Split(t.Language, "/") {
			// Audio tags are sloppy ("xxx", "unk", "abc"): only a value the
			// ISO 639 tables resolve is a vote, anything else abstains.
			if models.IsKnownLanguage(l) {
				langs = append(langs, models.NormalizeLanguageCode(l))
			}
		}
		langs = definiteLanguages(langs)
		switch {
		case len(langs) == 0:
		case slices.ContainsFunc(langs, lc.allows):
			allowedVotes++
		default:
			disallowedVotes++
			for _, l := range langs {
				if !slices.Contains(lc.declared, l) {
					lc.declared = append(lc.declared, l)
				}
			}
		}
	}
	if disallowedVotes == 0 || disallowedVotes <= allowedVotes {
		lc.declared = nil
		return lc
	}
	lc.reject = true
	lc.reason = fmt.Sprintf("audio files declare %s, but %s allows only %s. "+
		"Not imported, and the release was blocklisted so the next search picks another. "+
		"To keep these files anyway, use Match to book in the Queue or manual import",
		languageList(lc.declared), source, languageList(profileAllowed))
	slog.Debug("audiobook language check", "bookID", book.ID, "allowedVotes", allowedVotes, "disallowedVotes", disallowedVotes)
	return lc
}
