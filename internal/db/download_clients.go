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
	category, priority, enabled, created_at, updated_at`

func isCredentialClient(clientType string) bool {
	return clientType == "qbittorrent" || clientType == "transmission"
}

func hydrateClientCredentials(c *models.DownloadClient) {
	if isCredentialClient(c.Type) {
		// Backward compatibility: older rows stored credentials in url_base/api_key.
		if strings.TrimSpace(c.Username) == "" {
			c.Username = strings.TrimSpace(c.URLBase)
		}
		if c.Password == "" {
			c.Password = c.APIKey
		}
		return
	}
	// Non-credential clients should not expose username/password values.
	c.Username = ""
	c.Password = ""
}

func normalizeClientCredentialStorage(c *models.DownloadClient) {
	if !isCredentialClient(c.Type) {
		return
	}
	// Backward compatibility: accept legacy payloads that sent credentials in
	// urlBase/apiKey.
	if strings.TrimSpace(c.Username) == "" {
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
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.Priority,
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
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.Priority,
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
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.Priority,
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
	query := `
		SELECT ` + downloadClientSelectColumns + `
		FROM download_clients WHERE enabled=1 AND type=? ORDER BY priority LIMIT 1`
	var err error
	if protocol == "torrent" {
		err = r.db.QueryRowContext(ctx, `
			SELECT `+downloadClientSelectColumns+`
			FROM download_clients WHERE enabled=1 AND type IN (?, ?) ORDER BY priority LIMIT 1`, "qbittorrent", "transmission").
			Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
				&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.Priority,
				&enabled, &c.CreatedAt, &c.UpdatedAt)
	} else {
		err = r.db.QueryRowContext(ctx, query, "sabnzbd").
			Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.APIKey,
				&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.Priority,
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
			FROM download_clients WHERE enabled=1 AND type IN (?, ?) ORDER BY priority`, "qbittorrent", "transmission")
	} else {
		rows, err = r.db.QueryContext(ctx, `
			SELECT `+downloadClientSelectColumns+`
			FROM download_clients WHERE enabled=1 AND type=? ORDER BY priority`, "sabnzbd")
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
			&useSSL, &c.URLBase, &c.Username, &c.Password, &c.Category, &c.Priority,
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
		INSERT INTO download_clients (name, type, host, port, api_key, use_ssl, url_base, username, password, category, priority, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Type, c.Host, c.Port, c.APIKey, c.UseSSL, c.URLBase, c.Username, c.Password, c.Category, c.Priority, c.Enabled, now, now)
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
		                            url_base=?, username=?, password=?, category=?, priority=?, enabled=?, updated_at=?
		WHERE id=?`,
		c.Name, c.Type, c.Host, c.Port, c.APIKey, c.UseSSL, c.URLBase, c.Username, c.Password, c.Category, c.Priority, c.Enabled, now, c.ID)
	return err
}

func (r *DownloadClientRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM download_clients WHERE id=?", id)
	return err
}

// PickClientForMediaType selects the best client from a list for the given
// media type. Audiobooks prefer a client whose category contains "audio";
// other types prefer one without. Falls back to the first client.
func PickClientForMediaType(clients []models.DownloadClient, mediaType string) *models.DownloadClient {
	if len(clients) == 0 {
		return nil
	}
	if len(clients) == 1 {
		return &clients[0]
	}
	for i := range clients {
		cat := strings.ToLower(clients[i].Category)
		if mediaType == "audiobook" && strings.Contains(cat, "audio") {
			return &clients[i]
		}
		if mediaType != "audiobook" && !strings.Contains(cat, "audio") {
			return &clients[i]
		}
	}
	return &clients[0]
}
