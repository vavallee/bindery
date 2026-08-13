package downloader

import (
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// ResolveCategory picks the correct download-client category/label for a given
// media type. Audiobook downloads use CategoryAudiobook when it is non-empty;
// every other case (ebook, MediaTypeBoth, unset) falls back to Category, which
// preserves the pre-#700 behaviour for clients that have not opted in.
func ResolveCategory(client *models.DownloadClient, mediaType string) string {
	if client == nil {
		return ""
	}
	if mediaType == models.MediaTypeAudiobook && client.CategoryAudiobook != "" {
		return client.CategoryAudiobook
	}
	return client.Category
}

// FormatForCategory is the partial inverse of ResolveCategory: given the
// category a client reports for a torrent, it returns the media type that
// category was grabbed under — models.MediaTypeEbook, models.MediaTypeAudiobook,
// or "" when the category says nothing about the format.
//
// It answers only when the per-media-type category split is actually configured
// (CategoryAudiobook set and distinct from Category). Without the split every
// grab shares one category, so a match carries no format information, and a
// torrent sitting under some third category (a cross-seed Bindery failed to
// recategorise) is equally uninformative. Callers must treat "" as unknown
// rather than guessing — see importer.isBookAlreadyImported (#1885).
func FormatForCategory(client *models.DownloadClient, category string) string {
	if client == nil || client.CategoryAudiobook == "" || client.CategoryAudiobook == client.Category {
		return ""
	}
	got := strings.ToLower(strings.TrimSpace(category))
	if got == "" {
		return ""
	}
	switch got {
	case strings.ToLower(client.CategoryAudiobook):
		return models.MediaTypeAudiobook
	case strings.ToLower(client.Category):
		return models.MediaTypeEbook
	}
	return ""
}

// CategoriesToPoll returns the set of category strings GetTorrents/GetStalledIDs
// callers must poll to cover both ebook and audiobook downloads on the same
// client. When CategoryAudiobook is unset, the slice contains only Category
// (which may itself be empty — qBittorrent treats that as "all torrents").
//
// The category used at grab time is not persisted on the Download row; the
// alternative — storing the category-used on Download and looking it up per
// poll — would touch the Download schema and every grab call-site. Polling
// both categories is the smaller blast radius (closes #700). Exported so the
// importer's main poll (package importer) can reuse it.
func CategoriesToPoll(client *models.DownloadClient) []string {
	if client == nil {
		return nil
	}
	cats := []string{client.Category}
	if client.CategoryAudiobook != "" && client.CategoryAudiobook != client.Category {
		cats = append(cats, client.CategoryAudiobook)
	}
	return cats
}
