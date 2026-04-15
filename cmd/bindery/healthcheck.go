package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/vavallee/bindery/internal/config"
)

// runHealthcheck hits /api/v1/health on the local port and exits 0 on HTTP
// 200, else 1. Driven by the Docker HEALTHCHECK directive — kept dependency-
// free (no DB, no slog setup) so it's fast and can run under a readonly FS.
func runHealthcheck() {
	cfg := config.Load()
	url := fmt.Sprintf("http://127.0.0.1:%s/api/v1/health", cfg.Port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	status := resp.StatusCode
	_ = resp.Body.Close()
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: got %d\n", status)
		os.Exit(1)
	}
}
