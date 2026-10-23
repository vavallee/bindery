package downloader

import (
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/pathmap"
)

// Where a remap candidate came from, for the UI to label.
const (
	RemapSourceEbookDir       = "ebookDownloadDir"
	RemapSourceAudiobookDir   = "audiobookDownloadDir"
	RemapNoteSamePath         = "samePath"
	RemapNoteUnsupportedChars = "unsupportedChars"
	RemapSourceLibrary        = "library"
)

// remapPreviewName is the stand in file name the before and after preview
// shows under the folder, so the join reads as a path to a real item.
const remapPreviewName = "Example Book"

// RemapRoot is one folder on Bindery's own filesystem that a client's
// completed folder could be the same storage as.
type RemapRoot struct {
	Path   string
	Source string
}

// RemapCandidate is one offered right hand side for a path remap, with the
// join previewed. It is information only: it carries no remap string, so
// nothing here can be replayed as a write. The UI composes the remap and
// saves it through the normal download client update route.
type RemapCandidate struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	// Before is a sample path as the client names it, After the same path as
	// Bindery would read it under this candidate.
	Before string `json:"before"`
	After  string `json:"after"`
}

// SupportsRemapAssistant reports whether the remap assistant may be offered
// for a client type: the types CheckCompletedPathVisibility can introspect and
// that report a static completed folder. For every other type there is no
// remote path to seed from, so the assistant does not appear rather than guess.
func SupportsRemapAssistant(clientType string) bool {
	switch clientType {
	case "qbittorrent", "nzbget", "rtorrent":
		return true
	default:
		return false
	}
}

// RemapCandidates previews mapping remotePath onto each root. The preview goes
// through pathmap, the same parser the importer uses, so a Windows drive or
// share on the client side previews exactly as it will resolve. Roots that are
// empty or repeat an earlier one once cleaned are dropped. An unsupported type
// or a blank remote path yields nil. The second result is a note when nothing
// is offered for a reason worth telling the user: RemapNoteSamePath when a
// folder is identical on both sides (mount it, do not remap it), or
// RemapNoteUnsupportedChars when a path holds a comma or a colon.
func RemapCandidates(clientType, remotePath string, roots []RemapRoot) ([]RemapCandidate, string) {
	remotePath = strings.TrimSpace(remotePath)
	if !SupportsRemapAssistant(clientType) || remotePath == "" {
		return nil, ""
	}
	remote := pathmap.CleanClientPath(remotePath)
	if hasRemapSyntaxChar(remote) {
		return nil, RemapNoteUnsupportedChars
	}
	before := pathmap.ClientPathJoin(remote, remapPreviewName)
	var out []RemapCandidate
	seen := map[string]bool{}
	identity, skipped := false, false
	for _, root := range roots {
		if strings.TrimSpace(root.Path) == "" {
			continue
		}
		local := filepath.Clean(strings.TrimSpace(root.Path))
		if seen[local] {
			continue
		}
		seen[local] = true
		if hasRemapSyntaxChar(local) {
			skipped = true
			continue
		}
		after, matched := pathmap.Parse(remote + ":" + local).ApplyMatched(before)
		if !matched {
			continue
		}
		if after == before {
			// The remap would change nothing: the client and Bindery already
			// use the same path, so the folder needs mounting, not remapping.
			identity = true
			continue
		}
		out = append(out, RemapCandidate{Path: local, Source: root.Source, Before: before, After: after})
	}
	if len(out) > 0 {
		return out, ""
	}
	switch {
	case identity:
		return nil, RemapNoteSamePath
	case skipped:
		return nil, RemapNoteUnsupportedChars
	}
	return nil, ""
}

// hasRemapSyntaxChar reports whether p holds a character the comma separated
// from:to syntax cannot carry. A Windows drive designator's own colon is part
// of the path, not a separator.
func hasRemapSyntaxChar(p string) bool {
	if pathmap.IsWindowsPath(p) {
		p = p[2:]
	}
	return strings.ContainsAny(p, ",:")
}
