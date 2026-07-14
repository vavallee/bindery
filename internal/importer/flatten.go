package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// audioFlattenExtensions lists the audio extensions that participate in
// multi-disc audiobook flattening. It deliberately mirrors the audio set used
// by detectDownloadFormat so the flattener and the format detector agree on
// what counts as an audio track.
var audioFlattenExtensions = map[string]bool{
	".mp3": true, ".m4b": true, ".m4a": true, ".aac": true,
	".flac": true, ".ogg": true, ".opus": true,
}

// discDirPattern matches a directory component that names a disc, e.g.
// "Disc 1", "Disk 02", "CD 3", "CD3", "Part 4", "Volume 2", "Vol. 3". The
// number is captured. Matching is anchored loosely: the keyword can carry a
// trailing label ("Disc 1 - Intro") as long as the number follows the keyword.
var discDirPattern = regexp.MustCompile(`(?i)\b(?:disc|disk|cd|part|volume|vol)\.?\s*0*([0-9]{1,3})\b`)

// trackNumPattern matches a leading or keyword-introduced track number in a
// file's base name, e.g. "Track 01.mp3", "Chapter 02.mp3", "01 - Title.mp3",
// "1-05 - Title.mp3" (disc-track combined). The LAST captured group is the
// track number; a leading "<n>-" disc prefix is allowed and ignored here
// because disc ordering is resolved from the directory.
var trackNumPattern = regexp.MustCompile(`(?i)^(?:track|chapter|chap|part|ch)\.?\s*0*([0-9]{1,4})\b`)

// leadingNumPattern matches a bare leading number, optionally preceded by a
// "<disc>-" or "<disc>." prefix: "05 - Title.mp3", "1-05 Title.mp3",
// "1.05 Title.mp3". The final group is the track number.
var leadingNumPattern = regexp.MustCompile(`^(?:[0-9]{1,2}[-.])?0*([0-9]{1,4})\b`)

// flattenTrack is one audio file selected for flattening, carrying the
// detected disc/track ordering keys and its source path.
type flattenTrack struct {
	src   string // absolute source path
	rel   string // path relative to the source root (tiebreaker, deterministic)
	disc  int    // detected disc number; 0 when none detected
	track int    // detected track number; 0 when none detected
}

// extractDiscNumber returns the disc number encoded in any component of relDir
// (the directory path of an audio file relative to the audiobook source root),
// or 0 when none of the components name a disc. The deepest matching component
// wins so "CD 1/Disc 2/.." style nesting resolves to the most specific disc.
func extractDiscNumber(relDir string) int {
	if relDir == "" || relDir == "." {
		return 0
	}
	disc := 0
	for _, comp := range strings.Split(relDir, string(filepath.Separator)) {
		if m := discDirPattern.FindStringSubmatch(comp); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				disc = n
			}
		}
	}
	return disc
}

// extractTrackNumber returns the track number encoded in a file's base name
// (extension stripped by the caller is not required), or 0 when none is found.
// Keyword forms ("Track 01", "Chapter 2") win over a bare leading number.
func extractTrackNumber(base string) int {
	name := strings.TrimSuffix(base, filepath.Ext(base))
	name = strings.TrimSpace(name)
	if m := trackNumPattern.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := leadingNumPattern.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

// collectFlattenTracks walks srcRoot, returning every audio file it finds as a
// flattenTrack with disc/track ordering keys resolved, sorted deterministically
// by (disc, track, rel). Symlinks are skipped (never followed). The returned
// slice is the flat playback order the caller will rename to Part NNN.
func collectFlattenTracks(srcRoot string) ([]flattenTrack, error) {
	var tracks []flattenTrack
	err := filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Skip symlinks: never follow them into or out of the source tree.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !audioFlattenExtensions[ext] {
			return nil
		}
		rel, relErr := filepath.Rel(srcRoot, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}
		tracks = append(tracks, flattenTrack{
			src:   path,
			rel:   rel,
			disc:  extractDiscNumber(filepath.Dir(rel)),
			track: extractTrackNumber(filepath.Base(path)),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tracks, func(i, j int) bool {
		a, b := tracks[i], tracks[j]
		if a.disc != b.disc {
			return a.disc < b.disc
		}
		if a.track != b.track {
			return a.track < b.track
		}
		return a.rel < b.rel
	})
	return tracks, nil
}

// isMultiDiscAudiobook reports whether the audio files under srcRoot span two
// or more distinct disc directories — the only shape flattening targets. A
// single disc folder, or a flat folder with no disc directories at all, is left
// untouched (returns false) so existing single-disc imports are unchanged.
func isMultiDiscAudiobook(srcRoot string) bool {
	tracks, err := collectFlattenTracks(srcRoot)
	if err != nil || len(tracks) < 2 {
		return false
	}
	discs := map[int]struct{}{}
	for _, tr := range tracks {
		if tr.disc > 0 {
			discs[tr.disc] = struct{}{}
		}
	}
	return len(discs) >= 2
}

// flattenAudiobookDir places every audio track found under srcRoot into destDir
// as a flat "Part 001.ext", "Part 002.ext", … sequence using copy or hardlink
// only — the source is never moved, so the download keeps seeding. Non-audio
// sidecar files (cover art, cue sheets, NFOs) at the source root are carried
// across verbatim where their names do not collide with a generated Part name.
//
// mode must be "copy" or "hardlink"; any other value is rejected so the
// backend enforces the import-mode restriction even if a stale setting slips
// through. destDir must not already exist (the caller resolves collisions via
// UniqueDir) and is created here.
//
// The placement honours the same containment guarantee as the rest of the
// importer: every destination path is a direct child of destDir (Part NNN has
// no separators), so book-derived strings cannot escape the library.
func flattenAudiobookDir(ctx context.Context, mode, srcRoot, destDir string) error {
	if mode != "copy" && mode != "hardlink" {
		return fmt.Errorf("flatten supports copy/hardlink only, got %q", mode)
	}
	tracks, err := collectFlattenTracks(srcRoot)
	if err != nil {
		return fmt.Errorf("collect tracks: %w", err)
	}
	if len(tracks) == 0 {
		return fmt.Errorf("no audio tracks found under %q", srcRoot)
	}
	if _, statErr := os.Stat(destDir); statErr == nil {
		return fmt.Errorf("destination already exists: %s", destDir)
	}
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	// reserved tracks every generated Part filename so a sidecar copy can never
	// clobber one, and so a re-run never silently overwrites.
	reserved := make(map[string]struct{}, len(tracks))
	for i, tr := range tracks {
		if err := ctx.Err(); err != nil {
			_ = os.RemoveAll(destDir)
			return err
		}
		ext := strings.ToLower(filepath.Ext(tr.src))
		name := fmt.Sprintf("Part %03d%s", i+1, ext)
		reserved[name] = struct{}{}
		dst := filepath.Join(destDir, name)
		if err := placeFlattened(ctx, mode, tr.src, dst); err != nil {
			_ = os.RemoveAll(destDir)
			return fmt.Errorf("place %q: %w", tr.rel, err)
		}
	}

	// Carry root-level sidecars (cover.jpg, *.cue, *.nfo) across so players that
	// rely on embedded art still find it. Only regular files directly at the
	// source root are considered; nested disc-folder sidecars are skipped to
	// avoid name collisions and noise. Failures here are non-fatal — the audio
	// is already placed and a missing cover must not block the import.
	carryRootSidecars(ctx, mode, srcRoot, destDir, reserved)
	return nil
}

// placeFlattened copies or hardlinks a single track to dst.
func placeFlattened(ctx context.Context, mode, src, dst string) error {
	switch mode {
	case "hardlink":
		return HardlinkFile(src, dst)
	default: // "copy" — guarded by flattenAudiobookDir
		return CopyFileCtx(ctx, src, dst)
	}
}

// carryRootSidecars copies non-audio regular files that live directly at the
// source root into destDir, skipping symlinks, directories, audio files
// (already flattened) and any name already reserved by a generated Part. It is
// best-effort: individual failures are swallowed so a missing sidecar never
// fails an otherwise-successful audio import.
func carryRootSidecars(ctx context.Context, mode, srcRoot, destDir string, reserved map[string]struct{}) {
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if audioFlattenExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		if isDownloadArtifact(name) {
			continue // receipts/repair files never enter the library (#1542)
		}
		if _, taken := reserved[name]; taken {
			continue
		}
		dst := filepath.Join(destDir, name)
		if _, statErr := os.Stat(dst); statErr == nil {
			continue // never overwrite an existing destination file
		}
		src := filepath.Join(srcRoot, name)
		if mode == "hardlink" {
			_ = HardlinkFile(src, dst)
		} else {
			_ = CopyFileCtx(ctx, src, dst)
		}
	}
}
