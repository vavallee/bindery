package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

// ClientPathInfo is where a download client puts completed downloads.
type ClientPathInfo struct {
	// Path is the folder in the client's own filesystem namespace. Empty when
	// the client exposes no usable folder.
	Path string
	// Source names where Path came from, as an English phrase for a sentence
	// ("the save path Bindery sends", "the category save path").
	Source string
	// Category is the category or label the path was resolved for, exactly
	// as the grab sends it.
	Category string
	// Note and NoteFix are set when a grab will not behave the way the
	// configuration suggests, or when part of the answer could not be checked.
	Note    string
	NoteFix string
}

// ClientTypeName is the display name for a download client type.
func ClientTypeName(clientType string) string {
	switch clientType {
	case "qbittorrent":
		return "qBittorrent"
	case "transmission":
		return "Transmission"
	case "deluge":
		return "Deluge"
	case "rtorrent":
		return "rTorrent"
	case "nzbget":
		return "NZBGet"
	default:
		return "SABnzbd"
	}
}

// CompletedPath is the client default completed folder the Test button and
// the health job check: the qBittorrent category save path (or the default
// save path when the category sets none), NZBGet's DestDir for the category,
// and rTorrent's directory.default. Other types answer with no path. Its
// behaviour is unchanged from before the diagnose action; GrabSavePath is the
// folder a grab actually lands in.
//
// An error means the client would not answer. A zero Path with no error means
// the client answered but has no usable folder.
func CompletedPath(ctx context.Context, client *models.DownloadClient) (ClientPathInfo, error) {
	if client == nil {
		return ClientPathInfo{}, nil
	}
	switch client.Type {
	case "qbittorrent":
		category := strings.TrimSpace(client.Category)
		if category == "" {
			return ClientPathInfo{}, nil
		}
		qb := QbittorrentFor(client)
		categories, err := qb.GetCategories(ctx)
		if err != nil {
			return ClientPathInfo{}, err
		}
		qbCategory, ok := categories[category]
		if !ok {
			return ClientPathInfo{}, nil
		}
		if savePath := strings.TrimSpace(qbCategory.SavePath); savePath != "" {
			return ClientPathInfo{Path: savePath, Source: "the category save path"}, nil
		}
		if defaultPath, err := qb.GetDefaultSavePath(ctx); err == nil && strings.TrimSpace(defaultPath) != "" {
			return ClientPathInfo{Path: strings.TrimSpace(defaultPath), Source: "the client default"}, nil
		}
		return ClientPathInfo{}, nil
	case "nzbget":
		dir, err := NzbgetFor(client).CompletedDir(ctx, client.Category)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: strings.TrimSpace(dir), Source: "DestDir"}, nil
	case "rtorrent":
		dir, err := RtorrentFor(client).DefaultDirectory(ctx)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: strings.TrimSpace(dir), Source: "the client default"}, nil
	default:
		return ClientPathInfo{}, nil
	}
}

// GrabSavePath returns the folder a grab of mediaType sent through this client
// actually finishes in, worked out from the same inputs SendDownload uses:
// ResolveCategory for the category or label, and torrentSavePath for the save
// path Bindery sends. downloadDir and audiobookDownloadDir are Bindery's own
// BINDERY_DOWNLOAD_DIR and BINDERY_AUDIOBOOK_DOWNLOAD_DIR, and globalRemap is
// BINDERY_DOWNLOAD_PATH_REMAP, which the sent save path falls back to.
//
// An error means the client would not answer (for SABnzbd, typically an NZB
// only API key). A zero Path with no error means the client answered but
// exposes no usable folder, for example a category it does not have.
func GrabSavePath(ctx context.Context, client *models.DownloadClient, mediaType, downloadDir, audiobookDownloadDir, globalRemap string) (ClientPathInfo, error) {
	if client == nil {
		return ClientPathInfo{}, nil
	}
	// No trimming anywhere below: SendDownload sends the category and the
	// save path exactly as they are, so the doctor has to use them the same
	// way to reach the same folder.
	category := ResolveCategory(client, mediaType)
	opts := SendOptions{MediaType: mediaType, DownloadDir: downloadDir, AudiobookDownloadDir: audiobookDownloadDir, GlobalRemap: globalRemap}
	sent := sentSavePath{
		path:  torrentSavePath(client, opts),
		local: TargetDownloadDir(mediaType, downloadDir, audiobookDownloadDir),
	}
	if sent.path != "" {
		opts.GlobalRemap = ""
		sent.viaGlobal = sent.path != torrentSavePath(client, opts)
	}
	info := ClientPathInfo{Category: category}
	switch client.Type {
	case "qbittorrent":
		return qbittorrentGrabPath(ctx, client, info, sent)
	case "rtorrent":
		// rTorrent receives d.directory.set whenever Bindery has a download
		// folder, so directory.default only matters without one.
		dir, err := RtorrentFor(client).DefaultDirectory(ctx)
		if sent.path != "" {
			info.Path, info.Source = sent.path, "the save path Bindery sends"
			if err == nil {
				sent.noteAgainstDefault(&info, "rTorrent", dir)
			}
			return info, nil
		}
		if err != nil {
			return info, err
		}
		info.Path, info.Source = strings.TrimSpace(dir), "the client default"
		return info, nil
	case "transmission":
		// SendDownload passes client.Category (never the audiobook category)
		// as download-dir when it is an absolute path.
		info.Category = client.Category
		if strings.HasPrefix(info.Category, "/") {
			info.Path, info.Source = info.Category, "the save path Bindery sends"
			return info, nil
		}
		dir, err := TransmissionFor(client).DownloadDir(ctx)
		if err != nil {
			return info, err
		}
		info.Path, info.Source = dir, "the client default"
		return info, nil
	case "deluge":
		loc, err := DelugeFor(client).DownloadLocation(ctx, category)
		if err != nil {
			return info, err
		}
		info.Path, info.Source, info.Note, info.NoteFix = loc.Path, loc.Source, loc.Note, loc.NoteFix
		return info, nil
	case "nzbget":
		dir, source, err := NzbgetFor(client).GrabDestDir(ctx, category)
		if err != nil {
			return info, err
		}
		info.Path, info.Source = strings.TrimSpace(dir), source
		return info, nil
	default:
		dir, fromCategory, err := SabnzbdFor(client).CompleteDir(ctx, category)
		if err != nil {
			return info, err
		}
		info.Path, info.Source = dir, "the client completed folder"
		if fromCategory {
			info.Source = "the category folder"
		}
		return info, nil
	}
}

// sentSavePath is the save path a grab sends (torrentSavePath), with what the
// diagnose action needs to judge it.
type sentSavePath struct {
	path      string // as sent to the client; "" when Bindery sends none
	local     string // Bindery's own folder it was made from
	viaGlobal bool   // BINDERY_DOWNLOAD_PATH_REMAP produced it
}

// noteAgainstDefault sets a note on info for the signature of a global remap
// meant for a different client (#2665): the global remap produced the folder
// Bindery sends, yet the client's own default save folder sits on Bindery's
// own folder (that folder, inside it or above it) rather than on the folder
// it is sent. The global remap always matches a sent folder, because its
// Bindery side is Bindery's own folder, so a remap written for SABnzbd also
// rewrites what an rTorrent mounting Bindery's path directly is sent.
//
// Anything else is left alone, because a default unrelated to both folders is
// normal: a dedicated Bindery folder next to the client's default, or
// separate incomplete and complete folders. A default that is not an
// absolute path (rTorrent ships with "./", and "~/download" is common) says
// nothing about mounts and is not checked, nor is a folder from the client's
// own remap, which is the operator's explicit choice.
func (s sentSavePath) noteAgainstDefault(info *ClientPathInfo, clientName, def string) {
	def = strings.TrimSpace(def)
	if !s.viaGlobal || !pathmap.IsAbsClientPath(def) || s.local == "" {
		return
	}
	if !clientPathsOverlap(def, s.local) || clientPathsOverlap(def, s.path) {
		return
	}
	info.Note = fmt.Sprintf("BINDERY_DOWNLOAD_PATH_REMAP turns Bindery's folder %q into %q for %s, but %s's own default save folder %q is on Bindery's path, so the global remap may be meant for a different client. If %s really keeps this storage at %q, ignore this.", s.local, s.path, clientName, clientName, def, clientName, s.path)
	info.NoteFix = fmt.Sprintf("If %s sees Bindery's download folder at the same path, set the path remap %q on this client, so it keeps that path instead of using the global remap.", clientName, s.local+":"+s.local)
}

// clientPathsOverlap reports whether two paths in a download client's
// namespace are the same folder or one lies inside the other. Windows paths
// compare without regard to case or separator style.
func clientPathsOverlap(a, b string) bool {
	norm := func(p string) string {
		c := pathmap.CleanClientPath(p)
		if pathmap.IsWindowsPath(c) || pathmap.IsUNCPath(c) {
			c = strings.ToLower(strings.ReplaceAll(c, `\`, "/"))
		}
		return strings.TrimRight(c, "/")
	}
	a, b = norm(a), norm(b)
	under := func(child, parent string) bool {
		return child == parent || strings.HasPrefix(child, parent+"/")
	}
	return under(a, b) || under(b, a)
}

// qbittorrentGrabPath follows addTorrentFields: with a category Bindery turns
// on automatic torrent management and sends no save path, so the category
// decides. Without one it sends the save path, or leaves the client default.
func qbittorrentGrabPath(ctx context.Context, client *models.DownloadClient, info ClientPathInfo, sent sentSavePath) (ClientPathInfo, error) {
	qb := QbittorrentFor(client)
	if info.Category == "" {
		def, err := qb.GetDefaultSavePath(ctx)
		if sent.path != "" {
			info.Path, info.Source = sent.path, "the save path Bindery sends"
			if err == nil {
				sent.noteAgainstDefault(&info, "qBittorrent", def)
			}
			return info, nil
		}
		if err != nil {
			return info, err
		}
		info.Path, info.Source = strings.TrimSpace(def), "the client default"
		return info, nil
	}
	categories, err := qb.GetCategories(ctx)
	if err != nil {
		return info, err
	}
	cat, ok := categories[info.Category]
	if !ok {
		return info, nil
	}
	savePath := strings.TrimSpace(cat.SavePath)
	if pathmap.IsAbsClientPath(savePath) {
		info.Path, info.Source = savePath, "the category save path"
		return info, nil
	}
	def, err := qb.GetDefaultSavePath(ctx)
	if err != nil {
		return info, err
	}
	if def = strings.TrimSpace(def); def == "" {
		return info, nil
	}
	// qBittorrent resolves a relative category save path under its default
	// save path, and an empty one as the category name under it.
	if savePath != "" {
		info.Path, info.Source = pathmap.JoinClientPath(def, savePath), "the category save path, under the client default save path"
		return info, nil
	}
	info.Path, info.Source = pathmap.JoinClientPath(def, info.Category), "the client default save path plus the category name"
	return info, nil
}

// TestConnection checks the client answers with the stored credentials and
// nothing more. TestClient also validates SABnzbd and NZBGet categories, which
// the diagnose action reports as a separate check so a missing category is not
// shown as a connection failure.
func TestConnection(ctx context.Context, client *models.DownloadClient) error {
	switch client.Type {
	case "nzbget":
		return NzbgetFor(client).Test(ctx)
	case "qbittorrent", "transmission", "deluge", "rtorrent":
		return TestClient(ctx, client)
	default:
		return SabnzbdFor(client).Test(ctx)
	}
}

// CategoryReport is the result of CheckCategories.
type CategoryReport struct {
	// Checked is false for client types without a category list Bindery can
	// read (Transmission, rTorrent), and for Deluge when its Label plugin
	// will not list labels.
	Checked bool
	// Wanted is the distinct non-empty categories configured in Bindery.
	Wanted []string
	// Missing is the subset of Wanted the client does not define.
	Missing []string
	// Existing is every category the client defines, sorted.
	Existing []string
}

// CheckCategories compares the categories configured in Bindery with the ones
// the client defines. Matching is exact, as it is at grab time; a nested
// qBittorrent category such as "books/ebooks" is its own key and matches.
// Deluge labels are compared as deluge.LabelID, the lowercase form grabs send
// them in, and Wanted holds that form.
func CheckCategories(ctx context.Context, client *models.DownloadClient) (CategoryReport, error) {
	report := CategoryReport{}
	for _, c := range []string{client.Category, client.CategoryAudiobook} {
		if client.Type == "deluge" {
			c = deluge.LabelID(c)
		}
		// Compared exactly as grabs send it; a category with spaces around it
		// does not match the client's, and the grab would not either.
		if strings.TrimSpace(c) != "" && !slices.Contains(report.Wanted, c) {
			report.Wanted = append(report.Wanted, c)
		}
	}
	var existing []string
	switch client.Type {
	case "qbittorrent":
		cats, err := QbittorrentFor(client).GetCategories(ctx)
		if err != nil {
			return report, err
		}
		for name := range cats {
			existing = append(existing, name)
		}
	case "nzbget":
		cats, err := NzbgetFor(client).ListCategories(ctx)
		if err != nil {
			return report, err
		}
		existing = cats
	case "deluge":
		labels, err := DelugeFor(client).Labels(ctx)
		if err != nil {
			// Almost always the Label plugin being off. The report stays
			// unchecked, and the diagnose action's advice for an unchecked
			// Deluge list is to turn the plugin on.
			return report, nil
		}
		existing = labels
	case "transmission", "rtorrent":
		return report, nil
	default:
		cats, err := SabnzbdFor(client).GetCategories(ctx)
		if err != nil {
			return report, err
		}
		existing = cats
	}
	report.Checked = true
	sort.Strings(existing)
	report.Existing = existing
	for _, w := range report.Wanted {
		if !slices.Contains(existing, w) {
			report.Missing = append(report.Missing, w)
		}
	}
	return report, nil
}

// FindCaseInsensitivePathUnder is findCaseInsensitivePath confined to base: it
// lists only base and folders beneath it, never an ancestor, and it does not
// follow a symlink out of that tree. p must be at or under base, and base must
// exist, or it reports nothing. The diagnose action uses this form because the
// path came from a download client.
func FindCaseInsensitivePathUnder(base, p string) (resolved, divergedAt string) {
	base, p = filepath.Clean(base), filepath.Clean(p)
	if !filepath.IsAbs(base) || !PathIsAtOrUnder(p, base) {
		return "", ""
	}
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		return "", ""
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, base), string(filepath.Separator))
	return walkCaseInsensitive(base, rest, false)
}

// PathIsAtOrUnder reports whether candidate is base or lies beneath it. Both
// must already be filepath.Clean'd.
func PathIsAtOrUnder(candidate, base string) bool {
	return pathIsAtOrUnder(candidate, base)
}
