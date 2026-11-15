package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type pingRequest struct {
	InstallID string           `json:"install_id"`
	Version   string           `json:"version"`
	OS        string           `json:"os"`
	Arch      string           `json:"arch"`
	Deploy    string           `json:"deploy"`
	Features  *featuresPayload `json:"features,omitempty"`
}

// featuresPayload mirrors the bindery client's telemetry.Features struct.
// Pointer-on-the-request so older clients (no features field) decode cleanly
// to Features == nil. Count fields use *int instead of int so we can tell
// "client reported 0 indexers" from "client didn't report this field"
// during the rollout window with mixed-version traffic; both render as
// "not reported" / "0" downstream but the distinction is preserved on the
// wire in case a future query wants it.
type featuresPayload struct {
	Indexers        *int  `json:"indexers,omitempty"`
	DownloadClients *int  `json:"download_clients,omitempty"`
	Notifications   *int  `json:"notifications,omitempty"`
	Users           *int  `json:"users,omitempty"`
	CalibreEnabled  *bool `json:"calibre_enabled,omitempty"`
	ABSEnabled      *bool `json:"abs_enabled,omitempty"`
	GrimmoryEnabled *bool `json:"grimmory_enabled,omitempty"`
	HardcoverToken  *bool `json:"hardcover_token,omitempty"`
	OIDCEnabled     *bool `json:"oidc_enabled,omitempty"`
	MultiUser       *bool `json:"multi_user,omitempty"`

	// Setup-funnel day offsets (v1.30.1+): whole days from install creation
	// to each first milestone; absent = hasn't happened. Must be listed here
	// or handlePing's normalising re-marshal would silently drop them.
	SetupIndexerDay *int `json:"setup_indexer_day,omitempty"`
	SetupClientDay  *int `json:"setup_client_day,omitempty"`
	FirstAuthorDay  *int `json:"first_author_day,omitempty"`
	FirstGrabDay    *int `json:"first_grab_day,omitempty"`
	FirstImportDay  *int `json:"first_import_day,omitempty"`
}

// validDeploys constrains the set of deploy strings we accept on /api/ping.
// Anything outside this list is replaced with "" so a malicious client can't
// pollute the chart's legend with arbitrary labels.
var validDeploys = map[string]bool{
	"kubernetes": true,
	"docker":     true,
	"binary":     true,
	"helm":       true, // Helm chart can opt-in via BINDERY_DEPLOY_METHOD=helm
	"unraid":     true, // detected from the HOST_OS=Unraid env Unraid's Docker manager sets
}

type pingResponse struct {
	LatestVersion string `json:"latest_version"`
}

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	ip := realIP(r, s.trustedProxies)
	if !s.limiter.allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var req pingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !uuidRE.MatchString(req.InstallID) {
		http.Error(w, "install_id must be a valid UUID", http.StatusBadRequest)
		return
	}
	if len(req.Version) > 64 || len(req.OS) > 32 || len(req.Arch) > 32 || len(req.Deploy) > 32 {
		http.Error(w, "field too long", http.StatusBadRequest)
		return
	}
	req.Version = normalizeVersion(req.Version)
	// Reject pings from non-release builds — the literal "dev" (local
	// `go run` / `go build` with no -ldflags), CI's "sha-abc1234" and
	// "dev-abc1234" interim images, and "v1.7.0-3-gabc1234" git-describe
	// labels for commits past a tag. Each of those becomes its own row
	// in the version histogram, and the install_id is overwhelmingly
	// throwaway (CI / dev containers that recreate the DB on every run).
	// Newer clients skip these client-side; older clients land here.
	if !isReleaseVersion(req.Version) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pingResponse{LatestVersion: s.latest()})
		return
	}
	if !validDeploys[req.Deploy] {
		req.Deploy = ""
	}

	// features is stored as JSON or NULL when the client didn't send any.
	// Re-marshal here instead of forwarding the original JSON bytes so the
	// stored payload is normalised (whitespace-stripped, key order stable)
	// regardless of what the client serialised. Any marshal error keeps the
	// column NULL so a bad features field never blocks a valid ping.
	var featuresJSON sql.NullString
	if req.Features != nil {
		if buf, err := json.Marshal(req.Features); err == nil {
			featuresJSON = sql.NullString{String: string(buf), Valid: true}
		} else {
			slog.Warn("marshal features", "id", req.InstallID[:min(8, len(req.InstallID))], "error", err)
		}
	}

	now := time.Now().UTC()
	// newInstall is true when the upsert inserted a row rather than
	// updating one. RETURNING sees the row as stored after the statement,
	// so first_seen == last_seen only on the insert path (both bound to
	// the same `now`); on the update path first_seen keeps its original
	// value. Surfaced in the ping log line so new installs can be spotted
	// in k8s logs without querying the DB.
	var newInstall bool
	err := func() error {
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := tx.QueryRowContext(r.Context(), `
			INSERT INTO installs (install_id, version, os, arch, deploy, features, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(install_id) DO UPDATE SET
				version   = excluded.version,
				os        = excluded.os,
				arch      = excluded.arch,
				deploy    = excluded.deploy,
				features  = excluded.features,
				last_seen = excluded.last_seen
			RETURNING first_seen = last_seen
		`, req.InstallID, req.Version, req.OS, req.Arch, req.Deploy, featuresJSON, now, now).Scan(&newInstall); err != nil {
			return err
		}
		// Activity ledger: attribute this install to today. Unlike
		// last_seen this row never rolls forward, so day-by-day activity
		// stays queryable after later pings. version updates on conflict
		// so the day reflects the install's latest version that day
		// (an upgrade mid-day counts once, under the new version).
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO daily_activity (day, install_id, version)
			VALUES (?, ?, ?)
			ON CONFLICT(day, install_id) DO UPDATE SET version = excluded.version
		`, now.Format("2006-01-02"), req.InstallID, req.Version); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if err != nil {
		slog.Warn("upsert install", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("ping", "id", req.InstallID[:min(8, len(req.InstallID))], "version", req.Version, "os", req.OS, "arch", req.Arch, "features", featuresJSON.Valid, "new_install", newInstall)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pingResponse{LatestVersion: s.latest()})
}

// normalizeVersion strips a leading "v" from version strings of the form
// "v1.4.4" so they collapse into the same bucket as "1.4.4". Non-release
// strings ("dev", "sha-abc1234") pass through unchanged because the "v"
// strip is gated on the next character being a digit.
func normalizeVersion(v string) string {
	if len(v) >= 2 && v[0] == 'v' && v[1] >= '0' && v[1] <= '9' {
		return v[1:]
	}
	return v
}
