// Package downloader provides a unified interface for dispatching download
// requests to different download clients (SABnzbd, Transmission, qBittorrent).
package downloader

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/downloader/transmission"
	"github.com/vavallee/bindery/internal/models"
)

type LiveStatus struct {
	Percentage string
	TimeLeft   string
	Speed      string
}

type SendResult struct {
	RemoteID      string
	Protocol      string
	UsesTorrentID bool
}

func IsTorrentClient(clientType string) bool {
	return clientType == "transmission" || clientType == "qbittorrent"
}

func ProtocolForClient(clientType string) string {
	if IsTorrentClient(clientType) {
		return "torrent"
	}
	return "usenet"
}

func TestClient(ctx context.Context, client *models.DownloadClient) error {
	switch client.Type {
	case "transmission":
		trans := transmission.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		return trans.Test(ctx)
	case "qbittorrent":
		qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		return qb.Test(ctx)
	default:
		sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.UseSSL)
		return sab.Test(ctx)
	}
}

func SendDownload(ctx context.Context, client *models.DownloadClient, sourceURL, title string) (*SendResult, error) {
	result := &SendResult{
		Protocol:      ProtocolForClient(client.Type),
		UsesTorrentID: IsTorrentClient(client.Type),
	}

	switch client.Type {
	case "transmission":
		trans := transmission.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		// Transmission's download-dir must be an absolute path. The Category
		// field is repurposed as an optional path override for Transmission; if
		// the user left it as a bare label (e.g. "books") we pass "" so
		// Transmission falls back to its own configured default directory.
		transDL := client.Category
		if !strings.HasPrefix(transDL, "/") {
			transDL = ""
		}
		torrentID, err := trans.AddTorrent(ctx, sourceURL, transDL)
		if err != nil {
			return nil, err
		}
		if torrentID == 0 {
			return nil, fmt.Errorf("downloader accepted request but did not return a trackable torrent ID")
		}
		result.RemoteID = strconv.FormatInt(torrentID, 10)
		return result, nil
	case "qbittorrent":
		qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		hash, err := qb.AddTorrent(ctx, sourceURL, client.Category, "")
		if err != nil {
			return nil, err
		}
		hash = strings.ToLower(strings.TrimSpace(hash))
		if hash == "" {
			return nil, fmt.Errorf("downloader accepted request but did not return a trackable torrent ID")
		}
		result.RemoteID = hash
		return result, nil
	default:
		sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.UseSSL)
		resp, err := sab.AddURL(ctx, sourceURL, title, client.Category, 0)
		if err != nil {
			return nil, err
		}
		if len(resp.NzoIDs) > 0 {
			result.RemoteID = resp.NzoIDs[0]
		}
		return result, nil
	}
}

func RemoveDownload(ctx context.Context, client *models.DownloadClient, dl *models.Download, deleteFiles bool) error {
	switch client.Type {
	case "transmission":
		if dl.TorrentID == nil || *dl.TorrentID == "" {
			return nil
		}
		torrentID, err := strconv.ParseInt(*dl.TorrentID, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid transmission torrent id %q: %w", *dl.TorrentID, err)
		}
		trans := transmission.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		return trans.RemoveTorrent(ctx, torrentID, deleteFiles)
	case "qbittorrent":
		if dl.TorrentID == nil || *dl.TorrentID == "" {
			return nil
		}
		qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		return qb.DeleteTorrent(ctx, *dl.TorrentID, deleteFiles)
	default:
		if dl.SABnzbdNzoID == nil || *dl.SABnzbdNzoID == "" {
			return nil
		}
		sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.UseSSL)
		return sab.Delete(ctx, *dl.SABnzbdNzoID, deleteFiles)
	}
}

func GetLiveStatuses(ctx context.Context, client *models.DownloadClient) (map[string]LiveStatus, bool, error) {
	if IsTorrentClient(client.Type) {
		statuses, err := getTorrentLiveStatuses(ctx, client)
		return statuses, true, err
	}
	statuses, err := getSABLiveStatuses(ctx, client)
	return statuses, false, err
}

func getSABLiveStatuses(ctx context.Context, client *models.DownloadClient) (map[string]LiveStatus, error) {
	sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.UseSSL)
	queue, err := sab.GetQueue(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]LiveStatus, len(queue.Slots))
	for _, slot := range queue.Slots {
		out[slot.NzoID] = LiveStatus{
			Percentage: slot.Percentage,
			TimeLeft:   slot.TimeLeft,
			Speed:      queue.Speed,
		}
	}
	return out, nil
}

func getTorrentLiveStatuses(ctx context.Context, client *models.DownloadClient) (map[string]LiveStatus, error) {
	if client.Type == "transmission" {
		trans := transmission.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
		torrents, err := trans.GetTorrents(ctx, client.Category)
		if err != nil {
			return nil, err
		}

		out := make(map[string]LiveStatus, len(torrents))
		for _, t := range torrents {
			id := strconv.FormatInt(t.ID, 10)
			out[id] = LiveStatus{
				Percentage: fmt.Sprintf("%.1f", t.PercentDone*100),
				TimeLeft:   etaToTimeLeft(t.ETA),
				Speed:      bytesPerSecondToString(t.DownloadRate),
			}
		}
		return out, nil
	}

	qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)
	torrents, err := qb.GetTorrents(ctx, client.Category)
	if err != nil {
		return nil, err
	}

	out := make(map[string]LiveStatus, len(torrents))
	for _, t := range torrents {
		out[strings.ToLower(t.Hash)] = LiveStatus{
			Percentage: fmt.Sprintf("%.1f", t.Progress*100),
			TimeLeft:   etaToTimeLeft(int64(t.ETA)),
		}
	}
	return out, nil
}

func etaToTimeLeft(etaSeconds int64) string {
	if etaSeconds <= 0 {
		return ""
	}
	h := etaSeconds / 3600
	m := (etaSeconds % 3600) / 60
	s := etaSeconds % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func bytesPerSecondToString(v int64) string {
	if v <= 0 {
		return ""
	}
	if v >= 1024*1024 {
		return fmt.Sprintf("%.1f MB/s", float64(v)/float64(1024*1024))
	}
	if v >= 1024 {
		return fmt.Sprintf("%.1f KB/s", float64(v)/1024)
	}
	return fmt.Sprintf("%d B/s", v)
}
