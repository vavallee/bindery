package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/vavallee/bindery/internal/config"
)

// healthcheckURL is the address the Docker HEALTHCHECK polls. With
// BINDERY_URL_BASE set the whole router is mounted under that prefix
// (mountUnderURLBase), so the prefix has to be part of the path: without it the
// outer router answers 404 and the container reports unhealthy (#3352).
// urlBase is config.Config.URLBase, which is empty or starts with "/" and never
// ends with one.
func healthcheckURL(port, urlBase string) string {
	return fmt.Sprintf("http://127.0.0.1:%s%s/api/v1/health", port, urlBase)
}

// runHealthcheck hits /api/v1/health on the local port, under BINDERY_URL_BASE
// when one is set, and exits 0 on HTTP 200, else 1. Driven by the Docker
// HEALTHCHECK directive — kept dependency-free (no DB, no slog setup) so it's
// fast and can run under a readonly FS.
func runHealthcheck() {
	cfg := config.Load()
	url := healthcheckURL(cfg.Port, cfg.URLBase)

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
