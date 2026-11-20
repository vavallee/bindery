package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/dirperm"
	"github.com/vavallee/bindery/internal/models"
)

// discDirPattern matches a directory component that names a disc, e.g.
// "Disc 1", "Disk 02", "CD 3", "CD3", "Part 4", "Volume 2", "Vol. 3", and the
// Norwegian and Danish "Del 2" (part). The number is captured. Matching is
// anchored loosely: the keyword can carry a trailing label ("Disc 1 - Intro")
// or follow the title ("Fjellvinden CD1", "Fjellvinden_Cd 01") as long as the
// number follows the keyword. The keyword is bounded by anything but a letter
// or digit rather than \b, because \b counts "_" as a word character and
// release names often join the title and the disc with one.
var discDirPattern = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:disc|disk|cd|part|del|volume|vol)\.?\s*0*([0-9]{1,3})(?:$|[^\p{L}\p{N}])`)

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
		if !models.IsAudioPath(path) {
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
	sortFlattenTracks(tracks)
	return tracks, nil
}

// sortFlattenTracks puts tracks in playback order: by disc, then track
// number, then relative path so the order is deterministic.
func sortFlattenTracks(tracks []flattenTrack) {
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
	return flattenAudiobookDirNamed(ctx, mode, srcRoot, destDir, partName)
}

// partName is the default "Part NNN.ext" naming: it mirrors the historical
// multi-disc flatten.
func partName(index, _ int, ext string) string {
	return fmt.Sprintf("Part %03d%s", index+1, ext)
}

// flattenAudiobookDirNamed is flattenAudiobookDir with a caller-supplied namer,
// so both the built-in "Part NNN" multi-disc flatten and the opt-in per-file
// audiobook naming template (#1126) share one deterministic placement path.
// nameFor receives the 0-based playback index, the number of tracks, and the
// lowercased source extension (with leading dot) and returns the destination
// basename. The count lets the template namer give a folder holding a single
// track the same name as a lone audiobook file (#2900).
func flattenAudiobookDirNamed(ctx context.Context, mode, srcRoot, destDir string, nameFor func(index, count int, ext string) string) error {
	_, err := flattenAudiobookDirFiltered(ctx, mode, srcRoot, destDir, nameFor, isDownloadArtifact)
	return err
}

// flattenAudiobookDirFiltered is flattenAudiobookDirNamed with the root files
// left behind chosen by the caller, as copyDirFiltered is for copyDirContext.
// See flattenSidecarSkip.
//
// On success it also returns the source path of every file it placed: each
// track, and each root sidecar that really arrived. A move flatten deletes
// exactly those from the source afterwards (removeFlattenedSource), so a file
// the flatten never carried is not destroyed with the folder.
func flattenAudiobookDirFiltered(ctx context.Context, mode, srcRoot, destDir string, nameFor func(index, count int, ext string) string, skip func(string) bool) ([]string, error) {
	if mode != "copy" && mode != "hardlink" {
		return nil, fmt.Errorf("flatten supports copy/hardlink only, got %q", mode)
	}
	tracks, err := collectFlattenTracks(srcRoot)
	if err != nil {
		return nil, fmt.Errorf("collect tracks: %w", err)
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("no audio tracks found under %q", srcRoot)
	}
	if _, statErr := os.Stat(destDir); statErr == nil {
		return nil, fmt.Errorf("destination already exists: %s", destDir)
	}
	if err := os.MkdirAll(destDir, dirperm.Library()); err != nil {
		return nil, fmt.Errorf("create dest dir: %w", err)
	}
	placed := make([]string, 0, len(tracks))

	// reserved tracks every generated filename so a sidecar copy can never
	// clobber one, and so a re-run never silently overwrites.
	reserved := make(map[string]struct{}, len(tracks))
	for i, tr := range tracks {
		if err := ctx.Err(); err != nil {
			_ = os.RemoveAll(destDir)
			return nil, err
		}
		ext := strings.ToLower(filepath.Ext(tr.src))
		name := filepath.Base(nameFor(i, len(tracks), ext))
		if _, clash := reserved[name]; clash {
			// A template that omits {Part} would collapse every track to one
			// name and silently drop all but the last. Fail loudly instead.
			_ = os.RemoveAll(destDir)
			return nil, fmt.Errorf("audiobook file template produced a duplicate name %q for %q — include {Part}", name, tr.rel)
		}
		reserved[name] = struct{}{}
		dst := filepath.Join(destDir, name)
		if err := placeFlattened(ctx, mode, tr.src, dst); err != nil {
			_ = os.RemoveAll(destDir)
			return nil, fmt.Errorf("place %q: %w", tr.rel, err)
		}
		placed = append(placed, tr.src)
	}

	// Carry root-level sidecars (cover.jpg, *.cue, *.nfo) across so players that
	// rely on embedded art still find it. Only regular files directly at the
	// source root are considered; nested disc-folder sidecars are skipped to
	// avoid name collisions and noise. Failures here are non-fatal — the audio
	// is already placed and a missing cover must not block the import. A
	// sidecar that did not arrive is left out of the placed list, so a move
	// keeps it in the source instead of deleting it.
	//
	// The one exception is an archive volume (#3203): it only gets here when
	// skip let it through, which is a move, and a move deletes the source
	// folder afterwards. Failing to carry a volume then fails the flatten, so
	// the source is kept rather than destroying the archive.
	carried, err := carryRootSidecars(ctx, mode, srcRoot, destDir, reserved, skip)
	if err != nil {
		removeFlattenDest(destDir)
		return nil, err
	}
	return append(placed, carried...), nil
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
// fails an otherwise-successful audio import. Archive volumes are the
// exception: skip lets them through only for a move, which deletes the source
// afterwards, so a volume that cannot be carried (or whose name is already
// taken by a different file) is returned as an error.
//
// It returns the source path of every sidecar that is now at the destination,
// which is what tells a move flatten which root files it may delete.
func carryRootSidecars(ctx context.Context, mode, srcRoot, destDir string, reserved map[string]struct{}, skip func(string) bool) ([]string, error) {
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return nil, nil
	}
	var carried []string
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if models.IsAudioPath(name) {
			continue
		}
		if skip(name) {
			continue // receipts/repair files never enter the library (#1542)
		}
		if _, taken := reserved[name]; taken {
			continue
		}
		dst := filepath.Join(destDir, name)
		src := filepath.Join(srcRoot, name)
		if _, statErr := os.Stat(dst); statErr == nil {
			// Never overwrite an existing destination file. A volume that is
			// already there as an identical copy is carried; a different file
			// under its name would leave the volume only in the source.
			if isArchivePart(name) {
				if !sameFileContent(srcRoot, destDir, name) {
					return nil, fmt.Errorf("carry archive volume %q: a different file is already at the destination", name)
				}
				carried = append(carried, src)
			}
			continue
		}
		var cerr error
		if mode == "hardlink" {
			cerr = HardlinkFile(src, dst)
		} else {
			cerr = CopyFileCtx(ctx, src, dst)
		}
		if cerr != nil {
			if isArchivePart(name) {
				return nil, fmt.Errorf("carry archive volume %q: %w", name, cerr)
			}
			continue
		}
		carried = append(carried, src)
	}
	return carried, nil
}

// removeFlattenedSource is the delete half of a move mode flatten, which
// places by copy. The flatten carries the tracks and the root sidecars only,
// so the source can still hold regular files that never reached the library:
// anything that is not audio inside a subfolder (a booklet, a cover per disc,
// an archive volume) and a root sidecar whose copy failed. Deleting the whole
// folder destroyed those.
//
// The source folder is removed wholesale only when every regular file in it
// was either placed or is one skip drops on purpose (receipts and repair
// files, which a move never carries, see flattenSidecarSkip). Otherwise the
// placed and skipped files are removed one by one, emptied folders are pruned,
// and the files that were not placed stay where they are. Their paths,
// relative to srcRoot, are returned so the caller can report them. Nothing is
// carried into the library here: a flatten keeps nested sidecars out of the
// flat folder on purpose (two discs each ship a cover.jpg), and copy and
// hardlink imports leave them with the client too.
//
// A source that cannot be walked is left whole, since what it holds is
// unknown.
func removeFlattenedSource(srcRoot string, placed []string, skip func(string) bool) ([]string, error) {
	wasPlaced := make(map[string]struct{}, len(placed))
	for _, p := range placed {
		wasPlaced[filepath.Clean(p)] = struct{}{}
	}
	var done, dirs, left []string
	err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if path != srcRoot {
				dirs = append(dirs, path)
			}
		case !d.Type().IsRegular():
			// A symlink or a special file was never a candidate for
			// placement, so it does not hold the folder back.
		default:
			if _, ok := wasPlaced[filepath.Clean(path)]; ok || skip(d.Name()) {
				done = append(done, path)
				return nil
			}
			rel, relErr := filepath.Rel(srcRoot, path)
			if relErr != nil {
				rel = path
			}
			left = append(left, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk source: %w", err)
	}
	if len(left) == 0 {
		return nil, os.RemoveAll(srcRoot)
	}
	var firstErr error
	for _, f := range done {
		if rmErr := os.Remove(f); rmErr != nil && !os.IsNotExist(rmErr) && firstErr == nil {
			firstErr = rmErr
		}
	}
	// Deepest first. os.Remove refuses a folder that still holds something,
	// which is what keeps the folders of the files left behind.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
	return left, firstErr
}

// sameFileContent reports whether the file called name is readable and byte for
// byte equal in dirA and dirB. Both are read through os.Root, so a symlink in
// either tree cannot lead the read out of it.
func sameFileContent(dirA, dirB, name string) bool {
	read := func(dir string) ([]byte, bool) {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, false
		}
		defer func() { _ = root.Close() }()
		f, err := root.Open(name)
		if err != nil {
			return nil, false
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(f)
		return b, err == nil
	}
	a, okA := read(dirA)
	b, okB := read(dirB)
	return okA && okB && bytes.Equal(a, b)
}

// removeFlattenDest undoes a failed flatten by removing the files it placed
// and then its destination folder, which it created itself. Copy and hardlink
// leave the sources in place, and a move flatten only deletes its source after
// success, so the placed files are redundant. It goes through
// rollbackPlacedFiles, which removes through os.Root and never recurses.
func removeFlattenDest(destDir string) {
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return
	}
	var names []string
	if f, err := root.Open("."); err == nil {
		if entries, err := f.ReadDir(-1); err == nil {
			for _, e := range entries {
				names = append(names, e.Name())
			}
		}
		_ = f.Close()
	}
	_ = root.Close()
	rollbackPlacedFiles(destDir, names)
}

// flattenSidecarSkip picks the root files a flatten leaves behind for the
// import mode. A move flattens by copying and then deletes the source folder,
// so it may skip only receipt and repair files: an archive volume left behind
// would be deleted with the folder (#3203). Copy and hardlink keep the source,
// so archive volumes stay out of the library with the client.
func flattenSidecarSkip(mode string) func(string) bool {
	if mode == "move" {
		return isReceiptOrRepairFile
	}
	return isDownloadArtifact
}

// finishFlattenMove removes the source of a move mode flatten once its files
// are in the library, and logs what it had to leave. See
// removeFlattenedSource.
func finishFlattenMove(srcRoot string, placed []string, skip func(string) bool) {
	left, err := removeFlattenedSource(srcRoot, placed, skip)
	if err != nil {
		slog.Warn("flatten: could not remove source after move-mode flatten", "src", srcRoot, "error", err)
	}
	if len(left) > 0 {
		slog.Warn("flatten: source folder kept because it holds files the flatten did not place in the library",
			"src", srcRoot, "count", len(left), "files", left)
	}
}
