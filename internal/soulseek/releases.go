// Package soulseek turns slskd search responses into release objects for
// interactive search (#1717). It is the search source half of the integration:
// the slskd package talks to the daemon, this package decides what a peer's
// folder of files means as a release.
//
// Everything a peer sends is untrusted. Filenames are used for display, for
// the extension and for author and title parsing only, and are never joined to
// a local path. Nothing here invents metadata: an author or title the parser
// cannot find stays empty.
package soulseek

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Protocol is the release protocol of a Soulseek result.
const Protocol = "soulseek"

// GUIDPrefix starts every Soulseek result GUID.
const GUIDPrefix = "slskd:"

const (
	maxReleases   = 150
	maxTitleRunes = 240
	maxUserRunes  = 64
)

// ebookFormats and audioFormats are the extensions that make a file part of a
// release. Anything else a peer shares (covers, cue sheets, music, text files)
// is dropped.
var (
	ebookFormats = map[string]bool{"epub": true, "azw3": true, "azw": true, "mobi": true, "pdf": true, "djvu": true, "fb2": true}
	audioFormats = map[string]bool{"m4b": true, "m4a": true, "mp3": true, "ogg": true, "opus": true}
)

type groupKey struct{ user, dir, ext string }

type group struct {
	resp  slskd.SearchResponse
	dir   string
	ext   string
	files []slskd.SearchFile
	paths []string
	size  int64
}

// Releases groups the files of responses by peer, folder and format and returns
// one release per group, best placed peers first. mediaType narrows what is
// kept: "ebook" keeps only ebook formats, "audiobook" only audio, anything else
// keeps both. name labels every result (IndexerName).
func Releases(name string, responses []slskd.SearchResponse, mediaType string) []newznab.SearchResult {
	groups := map[groupKey]*group{}
	var order []groupKey
	for _, resp := range responses {
		user := cleanText(resp.Username, maxUserRunes)
		if user == "" {
			continue
		}
		for _, f := range resp.Files {
			if f.Size <= 0 {
				continue
			}
			p := strings.ReplaceAll(f.Filename, `\`, "/")
			ext := fileExt(f, p)
			if !wantFormat(ext, mediaType) {
				continue
			}
			dir := ""
			if i := strings.LastIndex(p, "/"); i >= 0 {
				dir = p[:i]
			}
			k := groupKey{user: user, dir: dir, ext: ext}
			g := groups[k]
			if g == nil {
				g = &group{resp: resp, dir: dir, ext: ext}
				groups[k] = g
				order = append(order, k)
			}
			g.files = append(g.files, f)
			g.paths = append(g.paths, p)
			g.size += f.Size
		}
	}

	out := make([]newznab.SearchResult, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k].release(k.user, name))
	}
	// Peers who would start now come first, then the shorter queue, then the
	// faster peer. The ranker is stable, so this is the tiebreak among releases
	// it scores alike; free slot and queue length stay out of the score itself.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Soulseek, out[j].Soulseek
		if a.FreeSlot != b.FreeSlot {
			return a.FreeSlot
		}
		if a.QueueLength != b.QueueLength {
			return a.QueueLength < b.QueueLength
		}
		if a.UploadSpeed != b.UploadSpeed {
			return a.UploadSpeed > b.UploadSpeed
		}
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].GUID < out[j].GUID
	})
	if len(out) > maxReleases {
		out = out[:maxReleases]
	}
	return out
}

func (g *group) release(user, name string) newznab.SearchResult {
	// Title: the file's own name for a single file, the folder's name for a
	// folder of parts, because the ranker and relevance filter read only the
	// title. The format is appended as a token so the format term can see it.
	var source string
	if len(g.paths) == 1 {
		source = path.Base(g.paths[0])
	} else {
		source = path.Base(g.dir)
		if g.dir == "" || source == "." || source == "/" {
			source = path.Base(g.paths[0])
		}
	}
	parsed := importer.ParseFilename(source)
	display := strings.TrimSuffix(source, path.Ext(source))
	if len(g.paths) == 1 {
		display = trimKnownExt(source, g.ext)
	}
	title := cleanText(display, maxTitleRunes)
	title = strings.TrimSpace(title + " [" + strings.ToUpper(g.ext) + "]")

	mediaType := models.MediaTypeEbook
	if audioFormats[g.ext] {
		mediaType = models.MediaTypeAudiobook
	}

	sorted := append([]string(nil), g.paths...)
	sort.Strings(sorted)
	h := sha256.New()
	h.Write([]byte(user))
	for _, p := range sorted {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return newznab.SearchResult{
		GUID:        GUIDPrefix + hex.EncodeToString(h.Sum(nil))[:32],
		IndexerName: name,
		Title:       title,
		Size:        g.size,
		Author:      cleanText(parsed.Author, maxTitleRunes),
		BookTitle:   cleanText(parsed.Title, maxTitleRunes),
		Protocol:    Protocol,
		MediaType:   mediaType,
		Soulseek: &newznab.SoulseekInfo{
			Username:    user,
			FreeSlot:    g.resp.HasFreeUploadSlot,
			QueueLength: max(g.resp.QueueLength, 0),
			UploadSpeed: max(g.resp.UploadSpeed, 0),
			FileCount:   len(g.files),
			Format:      g.ext,
		},
	}
}

func wantFormat(ext, mediaType string) bool {
	if !models.IsBookFilePath("x." + ext) {
		return false
	}
	switch mediaType {
	case models.MediaTypeEbook:
		return ebookFormats[ext]
	case models.MediaTypeAudiobook:
		return audioFormats[ext]
	}
	return ebookFormats[ext] || audioFormats[ext]
}

// fileExt is the file's extension, lower case without the dot. slskd reports it
// per file, but it is peer supplied, so the filename's own extension is the
// fallback and the two must not disagree about what the file is.
func fileExt(f slskd.SearchFile, p string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f.Extension), "."))
	}
	return ext
}

func trimKnownExt(name, ext string) string {
	if strings.EqualFold(strings.TrimPrefix(path.Ext(name), "."), ext) {
		return name[:len(name)-len(path.Ext(name))]
	}
	return name
}

// cleanText drops control and format characters from peer supplied text, folds
// runs of white space and cuts it to limit runes.
func cleanText(s string, limit int) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		n++
		if n >= limit {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
