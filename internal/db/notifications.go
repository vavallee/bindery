package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type NotificationRepo struct {
	db *sql.DB
}

func NewNotificationRepo(db *sql.DB) *NotificationRepo {
	return &NotificationRepo{db: db}
}

func (r *NotificationRepo) List(ctx context.Context) ([]models.Notification, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, method, headers, topic,
		       on_grab, on_import, on_upgrade, on_failure, on_health,
		       enabled, created_at, updated_at
		FROM notifications ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notifications []models.Notification
	for rows.Next() {
		n, err := r.scanRow(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, *n)
	}
	return notifications, rows.Err()
}

func (r *NotificationRepo) GetByID(ctx context.Context, id int64) (*models.Notification, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, type, url, method, headers, topic,
		       on_grab, on_import, on_upgrade, on_failure, on_health,
		       enabled, created_at, updated_at
		FROM notifications WHERE id=?`, id)

	n, err := r.scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

func (r *NotificationRepo) Create(ctx context.Context, n *models.Notification) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO notifications (name, type, url, method, headers, topic,
		                           on_grab, on_import, on_upgrade, on_failure, on_health,
		                           enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Name, n.Type, n.URL, n.Method, n.Headers, n.Topic,
		n.OnGrab, n.OnImport, n.OnUpgrade, n.OnFailure, n.OnHealth,
		n.Enabled, now, now)
	if err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	id, _ := result.LastInsertId()
	n.ID = id
	n.CreatedAt = now
	n.UpdatedAt = now
	return nil
}

func (r *NotificationRepo) Update(ctx context.Context, n *models.Notification) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE notifications
		SET name=?, type=?, url=?, method=?, headers=?, topic=?,
		    on_grab=?, on_import=?, on_upgrade=?, on_failure=?, on_health=?,
		    enabled=?, updated_at=?
		WHERE id=?`,
		n.Name, n.Type, n.URL, n.Method, n.Headers, n.Topic,
		n.OnGrab, n.OnImport, n.OnUpgrade, n.OnFailure, n.OnHealth,
		n.Enabled, now, n.ID)
	if err != nil {
		return fmt.Errorf("update notification: %w", err)
	}
	n.UpdatedAt = now
	return nil
}

func (r *NotificationRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM notifications WHERE id=?", id)
	return err
}

// scanner is a common interface satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

func (r *NotificationRepo) scanRow(s scanner) (*models.Notification, error) {
	var n models.Notification
	var onGrab, onImport, onUpgrade, onFailure, onHealth, enabled int
	if err := s.Scan(
		&n.ID, &n.Name, &n.Type, &n.URL, &n.Method, &n.Headers, &n.Topic,
		&onGrab, &onImport, &onUpgrade, &onFailure, &onHealth,
		&enabled, &n.CreatedAt, &n.UpdatedAt,
	); err != nil {
		return nil, err
	}
	n.OnGrab = onGrab == 1
	n.OnImport = onImport == 1
	n.OnUpgrade = onUpgrade == 1
	n.OnFailure = onFailure == 1
	n.OnHealth = onHealth == 1
	n.Enabled = enabled == 1
	return &n, nil
}
