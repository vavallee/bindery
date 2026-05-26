package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type DownloadClientRepo struct {
	db *sql.DB
}

const downloadClientSelectColumns = `
	id, name, type, host, port, api_key, use_ssl, url_base, username, password,
	category, category_audiobook, path_remap, priority, enabled, created_at, updated_at`

func isCredentialClient(clientType string) bool {
	return clientType == "qbittorrent" || clientType == "transmission"
}

// legacyCredentialURLBase reports whether a download-client row was written by
// an older version of bindery that stored qBittorrent/Transmission credentials
// in the url_base and api_key columns instead of the dedicated username and
// password columns.
//
// Detection requires all three signals to agree:
//   - username is empty or equals url_base (url_base held the username)
//   - url_base is non-empty (there is actually something to migrate)
//   - apiKey is non-empty (the legacy schema also stored password in api_key;
//     if api_key is empty, the row is a modern client with a real url_base path,
//     not a migrated credential pair)
//
// Scoping apiKey into the guard fixes the regression described in #423:
// the previous implementation only looked at username and url_base, so a modern
// client with a bare url_base (e.g. "qbit") and an empty api_key would be
// misidentified as a legacy row and have its url_base silently cleared on read.
func legacyCredentialURLBase(username, urlBase, apiKey string) bool {
	urlBase = strings.TrimSpace(urlBase)
	if urlBase == "" || strings.HasPrefix(urlBase, "/") {
		return false
	}
	username = strings.TrimSpace(username)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return false
	}
	return username == "" || username == urlBase
}

func hydrateClientCredentials(c *models.DownloadClient) {
	switch c.Type {
	case "qbittorrent", "transmission":
		// Backward compatibility: older rows stored credentials in url_base/api_key.
		// Only migrate when the row looks like a genuine legacy row (url_base and
		// api_key both populated); leave modern rows with a real url_base path
		// and dedicated username/password columns untouched. (closes #423)
		if legacyCredentialURLBase(c.Username, c.URLBase, c.APIKey) {
			c.Username = strings.TrimSpace(c.URLBase)
		}
		if c.Password == "" {
			c.Password = c.APIKey
		}
		if legacyCredentialURLBase(c.Username, c.URLBase, c.APIKey) {
			c.URLBase = ""
		}
	case "nzbget", "deluge":
		// These clients use username/password directly — preserve as stored.
	default:
		// SABnzbd authenticates via api_key, not username/password.
		c.Username = ""
		c.Password = ""
	}
}

func normalizeClientCredentialStorage(c *models.DownloadClient) {
	if !isCredentialClient(c.Type) {
		return
	}
	// Backward compatibility: accept legacy payloads that sent credentials in
	// urlBase/apiKey. Use the same legacyCredentialURLBase guard as the read
	// path so that a client saved with a bare url_base and no api_key does not
	// have its url_base silently migrated into username on write. (closes #422)
	if legacyCredentialURLBase(c.Username, c.URLBase, c.APIKey) {
		c.Username = strings.TrimSpace(c.URLBase)
	}
	if c.Password == "" && c.APIKey != "" {
		c.Password = c.APIKey
	}
}

func NewDownloadClientRepo(db *sql.DB) *DownloadClientRepo {
	return &DownloadClientRepo{db: db}
}

func (r *DownloadClientRepo) List(ctx context.Context) ([]models.DownloadClient, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+downloadClientSelectColumns+`
		FROM download_clients ORDER BY priority`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []models.DownloadClient
	for rows.Next() {
		var c models.DownloadClient
		var enabled, useSSL int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.CategoryAudiobook, &c.PathRemap, &c.Priority,
			&enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		c.UseSSL = useSSL == 1
		hydrateClientCredentials(&c)
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

func (r *DownloadClientRepo) GetByID(ctx context.Context, id int64) (*models.DownloadClient, error) {
	var c models.DownloadClient
	var enabled, useSSL int
	err := r.db.QueryRowContext(ctx, `
		SELECT `+downloadClientSelectColumns+`
		FROM download_clients WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.CategoryAudiobook, &c.PathRemap, &c.Priority,
			&enabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled == 1
	c.UseSSL = useSSL == 1
	hydrateClientCredentials(&c)
	return &c, nil
}

func (r *DownloadClientRepo) GetFirstEnabled(ctx context.Context) (*models.DownloadClient, error) {
	var c models.DownloadClient
	var enabled, useSSL int
	err := r.db.QueryRowContext(ctx, `
		SELECT `+downloadClientSelectColumns+`
		FROM download_clients WHERE enabled=1 ORDER BY priority LIMIT 1`).
		Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.CategoryAudiobook, &c.PathRemap, &c.Priority,
			&enabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled == 1
	c.UseSSL = useSSL == 1
	hydrateClientCredentials(&c)
	return &c, nil
}

// GetFirstEnabledByProtocol returns the highest-priority enabled client that
// matches the given protocol ("usenet" → sabnzbd, "torrent" → qbittorrent).
// Returns (nil, nil) if no matching client is configured.
func (r *DownloadClientRepo) GetFirstEnabledByProtocol(ctx context.Context, protocol string) (*models.DownloadClient, error) {
	var c models.DownloadClient
	var enabled, useSSL int
	var err error
	if protocol == "torrent" {
		err = r.db.QueryRowContext(ctx, `
			SELECT `+downloadClientSelectColumns+`
			FROM download_clients WHERE enabled=1 AND type IN (?, ?, ?) ORDER BY priority LIMIT 1`, "qbittorrent", "transmission", "deluge").
			Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
				&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.CategoryAudiobook, &c.PathRemap, &c.Priority,
				&enabled, &c.CreatedAt, &c.UpdatedAt)
	} else {
		err = r.db.QueryRowContext(ctx, `
			SELECT `+downloadClientSelectColumns+`
			FROM download_clients WHERE enabled=1 AND type IN (?, ?) ORDER BY priority LIMIT 1`, "sabnzbd", "nzbget").
			Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
				&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.CategoryAudiobook, &c.PathRemap, &c.Priority,
				&enabled, &c.CreatedAt, &c.UpdatedAt)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	c.Enabled = enabled == 1
	c.UseSSL = useSSL == 1
	hydrateClientCredentials(&c)
	return &c, nil
}

// GetEnabledByProtocol returns all enabled clients matching the given protocol,
// ordered by priority. Used when multiple clients of the same type exist and
// the caller needs to pick the best one by category.
func (r *DownloadClientRepo) GetEnabledByProtocol(ctx context.Context, protocol string) ([]models.DownloadClient, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if protocol == "torrent" {
		rows, err = r.db.QueryContext(ctx, `
			SELECT `+downloadClientSelectColumns+`
			FROM download_clients WHERE enabled=1 AND type IN (?, ?, ?) ORDER BY priority`, "qbittorrent", "transmission", "deluge")
	} else {
		rows, err = r.db.QueryContext(ctx, `
			SELECT `+downloadClientSelectColumns+`
			FROM download_clients WHERE enabled=1 AND type IN (?, ?) ORDER BY priority`, "sabnzbd", "nzbget")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []models.DownloadClient
	for rows.Next() {
		var c models.DownloadClient
		var enabled, useSSL int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.CategoryAudiobook, &c.PathRemap, &c.Priority,
			&enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		c.UseSSL = useSSL == 1
		hydrateClientCredentials(&c)
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

func (r *DownloadClientRepo) Create(ctx context.Context, c *models.DownloadClient) error {
	normalizeClientCredentialStorage(c)
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO download_clients (name, type, host, port, api_key, use_ssl, url_base, username, password, category, category_audiobook, path_remap, priority, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Type, c.Host, c.Port, c.APIKey, c.UseSSL, c.URLBase, c.Username, c.Password, c.Category, c.CategoryAudiobook, c.PathRemap, c.Priority, c.Enabled, now, now)
	if err != nil {
		return fmt.Errorf("create download client: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get download client id: %w", err)
	}
	c.ID = id
	c.CreatedAt = now
	c.UpdatedAt = now
	return nil
}

func (r *DownloadClientRepo) Update(ctx context.Context, c *models.DownloadClient) error {
	normalizeClientCredentialStorage(c)
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE download_clients SET name=?, type=?, host=?, port=?, api_key=?, use_ssl=?,
		                            url_base=?, username=?, password=?, category=?, category_audiobook=?, path_remap=?, priority=?, enabled=?, updated_at=?
		WHERE id=?`,
		c.Name, c.Type, c.Host, c.Port, c.APIKey, c.UseSSL, c.URLBase, c.Username, c.Password, c.Category, c.CategoryAudiobook, c.PathRemap, c.Priority, c.Enabled, now, c.ID)
	return err
}

func (r *DownloadClientRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM download_clients WHERE id=?", id)
	return err
}

// PickClientForMediaType selects the best client from a list for the given
// media type. The explicit per-media-type CategoryAudiobook field (added in
// #700) is the strongest signal: a client with that field populated can
// handle audiobooks regardless of its primary Category name. As a fallback
// for clients that have not opted into the new field, we keep the legacy
// fuzzy "audio in category" heuristic so existing single-client setups keep
// working.
func PickClientForMediaType(clients []models.DownloadClient, mediaType string) *models.DownloadClient {
	if len(clients) == 0 {
		return nil
	}
	if len(clients) == 1 {
		return &clients[0]
	}
	// First pass: prefer a client whose explicit fields match.
	for i := range clients {
		if mediaType == models.MediaTypeAudiobook && strings.TrimSpace(clients[i].CategoryAudiobook) != "" {
			return &clients[i]
		}
	}
	// Second pass: legacy heuristic for clients without CategoryAudiobook set.
	for i := range clients {
		cat := strings.ToLower(clients[i].Category)
		if mediaType == models.MediaTypeAudiobook && strings.Contains(cat, "audio") {
			return &clients[i]
		}
		if mediaType != models.MediaTypeAudiobook && !strings.Contains(cat, "audio") {
			return &clients[i]
		}
	}
	return &clients[0]
}
