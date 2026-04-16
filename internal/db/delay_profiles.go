package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

type DelayProfileRepo struct {
	db *sql.DB
}

func NewDelayProfileRepo(db *sql.DB) *DelayProfileRepo {
	return &DelayProfileRepo{db: db}
}

func (r *DelayProfileRepo) List(ctx context.Context) ([]models.DelayProfile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, usenet_delay, torrent_delay, preferred_protocol, enable_usenet,
		       enable_torrent, "order", created_at
		FROM delay_profiles ORDER BY "order"`)
	if err != nil {
		return nil, fmt.Errorf("list delay profiles: %w", err)
	}
	defer rows.Close()

	var profiles []models.DelayProfile
	for rows.Next() {
		p, err := scanDelayProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func (r *DelayProfileRepo) GetByID(ctx context.Context, id int64) (*models.DelayProfile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, usenet_delay, torrent_delay, preferred_protocol, enable_usenet,
		       enable_torrent, "order", created_at
		FROM delay_profiles WHERE id=?`, id)
	if err != nil {
		return nil, fmt.Errorf("get delay profile %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	p, err := scanDelayProfile(rows)
	if err != nil {
		return nil, err
	}
	return &p, rows.Err()
}

func (r *DelayProfileRepo) Create(ctx context.Context, p *models.DelayProfile) error {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO delay_profiles (usenet_delay, torrent_delay, preferred_protocol,
		                            enable_usenet, enable_torrent, "order")
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.UsenetDelay, p.TorrentDelay, p.PreferredProtocol,
		p.EnableUsenet, p.EnableTorrent, p.Order)
	if err != nil {
		return fmt.Errorf("create delay profile: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get delay profile id: %w", err)
	}
	p.ID = id
	return nil
}

func (r *DelayProfileRepo) Update(ctx context.Context, p *models.DelayProfile) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE delay_profiles SET usenet_delay=?, torrent_delay=?, preferred_protocol=?,
		                          enable_usenet=?, enable_torrent=?, "order"=?
		WHERE id=?`,
		p.UsenetDelay, p.TorrentDelay, p.PreferredProtocol,
		p.EnableUsenet, p.EnableTorrent, p.Order, p.ID)
	if err != nil {
		return fmt.Errorf("update delay profile %d: %w", p.ID, err)
	}
	return nil
}

func (r *DelayProfileRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM delay_profiles WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete delay profile %d: %w", id, err)
	}
	return nil
}

func scanDelayProfile(rows *sql.Rows) (models.DelayProfile, error) {
	var p models.DelayProfile
	var enableUsenet, enableTorrent int
	err := rows.Scan(
		&p.ID, &p.UsenetDelay, &p.TorrentDelay, &p.PreferredProtocol,
		&enableUsenet, &enableTorrent, &p.Order, &p.CreatedAt,
	)
	if err != nil {
		return p, fmt.Errorf("scan delay profile: %w", err)
	}
	p.EnableUsenet = enableUsenet == 1
	p.EnableTorrent = enableTorrent == 1
	return p, nil
}
