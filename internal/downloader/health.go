package downloader

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

const (
	HealthOK       = "ok"
	HealthChecking = "checking"
	HealthError    = "error"
)

// HealthStore keeps non-persistent download-client health diagnostics.
type HealthStore struct {
	mu   sync.RWMutex
	byID map[int64]models.DownloadClientHealth
}

func NewHealthStore() *HealthStore {
	return &HealthStore{byID: make(map[int64]models.DownloadClientHealth)}
}

func (s *HealthStore) Set(id int64, health models.DownloadClientHealth) {
	if s == nil || id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = health
}

func (s *HealthStore) Delete(id int64) {
	if s == nil || id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func (s *HealthStore) Get(id int64) *models.DownloadClientHealth {
	if s == nil || id == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	health, ok := s.byID[id]
	if !ok {
		return nil
	}
	return &health
}

func (s *HealthStore) Attach(client *models.DownloadClient) {
	if s == nil || client == nil {
		return
	}
	client.Health = s.Get(client.ID)
}

func CheckingHealth() models.DownloadClientHealth {
	return models.DownloadClientHealth{Status: HealthChecking, Message: "Checking qBittorrent category path"}
}

func RefreshDownloadClientHealthAsync(parent context.Context, store *HealthStore, clients []models.DownloadClient, downloadDir, audiobookDownloadDir string) {
	if store == nil {
		return
	}
	for i := range clients {
		client := clients[i]
		if !client.Enabled || client.Type != "qbittorrent" {
			continue
		}
		store.Set(client.ID, CheckingHealth())
		go func() {
			ctx, cancel := context.WithTimeout(parent, 15*time.Second)
			defer cancel()
			store.Set(client.ID, CheckDownloadClientHealth(ctx, &client, downloadDir, audiobookDownloadDir))
		}()
	}
}

func CheckDownloadClientHealth(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir string) models.DownloadClientHealth {
	if client == nil || client.Type != "qbittorrent" {
		return models.DownloadClientHealth{Status: HealthOK, Message: "Download client path check not required"}
	}
	return checkQbittorrentCategoryPath(ctx, client, downloadDir, audiobookDownloadDir)
}

func ExpectedDownloadDirForClient(client *models.DownloadClient, downloadDir, audiobookDownloadDir string) string {
	if client == nil {
		return strings.TrimSpace(downloadDir)
	}
	if strings.Contains(strings.ToLower(client.Category), "audio") && strings.TrimSpace(audiobookDownloadDir) != "" {
		return strings.TrimSpace(audiobookDownloadDir)
	}
	return strings.TrimSpace(downloadDir)
}

func TargetDownloadDir(mediaType, downloadDir, audiobookDownloadDir string) string {
	if mediaType == models.MediaTypeAudiobook && strings.TrimSpace(audiobookDownloadDir) != "" {
		return strings.TrimSpace(audiobookDownloadDir)
	}
	return strings.TrimSpace(downloadDir)
}

func checkQbittorrentCategoryPath(ctx context.Context, client *models.DownloadClient, downloadDir, audiobookDownloadDir string) models.DownloadClientHealth {
	category := strings.TrimSpace(client.Category)
	if category == "" {
		return healthError("qBittorrent category is empty; configure a category with a save path")
	}
	expected := filepath.Clean(ExpectedDownloadDirForClient(client, downloadDir, audiobookDownloadDir))
	if expected == "." || expected == "" {
		return healthError("Bindery download directory is empty; check BINDERY_DOWNLOAD_DIR")
	}

	qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL)
	categories, err := qb.GetCategories(ctx)
	if err != nil {
		return healthError(fmt.Sprintf("qBittorrent category path check failed: %v", err))
	}
	qbCategory, ok := categories[category]
	if !ok {
		return healthError(fmt.Sprintf("qBittorrent category %q was not found; create it with save path %q", category, expected))
	}

	savePath := strings.TrimSpace(qbCategory.SavePath)
	if savePath == "" {
		message := fmt.Sprintf("qBittorrent category %q has no save path; expected %q", category, expected)
		if defaultPath, err := qb.GetDefaultSavePath(ctx); err == nil && strings.TrimSpace(defaultPath) != "" {
			message += fmt.Sprintf(" and qBittorrent default is %q", strings.TrimSpace(defaultPath))
		}
		return healthError(message)
	}

	localPath := filepath.Clean(pathmap.Parse(client.PathRemap).Apply(savePath))
	if !pathIsAtOrUnder(localPath, expected) {
		return healthError(fmt.Sprintf("qBittorrent category %q saves to %q, which maps to %q; expected a path at or under %q", category, savePath, localPath, expected))
	}

	return models.DownloadClientHealth{
		Status:  HealthOK,
		Message: fmt.Sprintf("qBittorrent category %q saves to %q", category, savePath),
	}
}

func healthError(message string) models.DownloadClientHealth {
	return models.DownloadClientHealth{Status: HealthError, Message: message}
}

// pathIsAtOrUnder reports whether candidate is equal to base or is a
// subdirectory of base. Both paths must already be filepath.Clean'd.
// A trailing separator is added to base before the prefix check so that
// "/data/downloads-extra" is not mistakenly accepted as "under" "/data/downloads".
func pathIsAtOrUnder(candidate, base string) bool {
	if candidate == base {
		return true
	}
	return strings.HasPrefix(candidate, base+string(filepath.Separator))
}
