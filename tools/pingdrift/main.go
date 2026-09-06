// Command pingdrift compares the bindery-ping image pinned in
// deploy/telemetry-server.yaml against the build the live server reports.
//
// It exists because that deployment is applied by hand: the host is not in
// the ArgoCD application (which tracks charts/bindery only) and CI has no
// kubectl or SSH to it, only an HTTPS token. The pin sat at the 2026-07-11
// build for 41 days while main shipped a new ping log line, a Debian 13 base
// and a crypto bump, and nothing said so. This turns that into a daily
// failure.
//
// It is a Go tool rather than shell in a workflow step so the parsing and
// comparison are unit-testable, matching tools/licensegen.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"
)

// imageRE matches the pinned bindery-ping image line and captures the sha.
//
// A regexp rather than a YAML parse on purpose: pulling in a YAML library
// for one line would make it a direct dependency, with the licence check and
// THIRD_PARTY_LICENSES regeneration that implies, to read a field whose
// shape this repo controls. The hazards a parser would have handled are
// covered instead: `^\s*image:` will not match a `#` commented line, and
// pinnedImageSHA refuses a file with more than one match rather than
// silently taking the first, so adding a second bindery-ping container fails
// loudly here instead of being ignored.
var imageRE = regexp.MustCompile(`(?m)^\s*image:\s*ghcr\.io/[^/\s]+/bindery-ping:sha-([0-9a-f]{7,40})\s*$`)

// errNoPin distinguishes "the manifest does not pin a sha" (someone moved it
// back to :latest, which defeats the whole check) from a read failure.
var errNoPin = errors.New("no sha-pinned bindery-ping image found")

// pinnedImageSHA returns the sha the manifest pins bindery-ping to.
func pinnedImageSHA(manifest []byte) (string, error) {
	m := imageRE.FindAllSubmatch(manifest, -1)
	switch len(m) {
	case 0:
		return "", errNoPin
	case 1:
		return string(m[0][1]), nil
	default:
		return "", fmt.Errorf("found %d sha-pinned bindery-ping images, expected exactly one", len(m))
	}
}

// compareSHA reports whether the running build matches the pin.
//
// The comparison is on the shorter of the two, because the manifest may
// carry a short sha while the server reports the full one (or the reverse).
func compareSHA(pinned, live string) error {
	// A build from before the stamping change reports the "unknown" default,
	// or nothing at all on a build from before the field existed. Both mean
	// the running image is older than the change that added the field, so
	// both are drift rather than an inconclusive result.
	switch live {
	case "":
		return errors.New("the running server reports no build sha at all, so it predates the build_sha field entirely")
	case "unknown":
		return errors.New("the running server reports build sha \"unknown\", so it was built without BUILD_SHA and predates the stamping")
	}

	n := len(pinned)
	if len(live) < n {
		n = len(live)
	}
	if pinned[:n] != live[:n] {
		return fmt.Errorf("running %s but the manifest pins %s", live, pinned)
	}
	return nil
}

// liveBuildSHA reads build_sha from the token-gated stats endpoint. The sha
// is not on public /health deliberately: an exact build identifier tells a
// stranger which advisories apply, and this caller already holds the token.
func liveBuildSHA(url, token string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}

	var body struct {
		BuildSHA string `json:"build_sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode %s: %w", url, err)
	}
	return body.BuildSHA, nil
}

func main() {
	manifestPath := os.Getenv("PING_MANIFEST")
	if manifestPath == "" {
		manifestPath = "deploy/telemetry-server.yaml"
	}
	statsURL := os.Getenv("PING_STATS_URL")
	if statsURL == "" {
		statsURL = "https://api.getbindery.dev/api/stats"
	}
	token := os.Getenv("TELEMETRY_STATS_TOKEN")
	if token == "" {
		fail("TELEMETRY_STATS_TOKEN is not set, cannot read the running build")
	}

	raw, err := os.ReadFile(manifestPath) // #nosec G304 -- path is a repo file, operator supplied
	if err != nil {
		fail("read %s: %v", manifestPath, err)
	}
	pinned, err := pinnedImageSHA(raw)
	if err != nil {
		fail("%s: %v", manifestPath, err)
	}

	live, err := liveBuildSHA(statsURL, token)
	if err != nil {
		fail("%v", err)
	}

	fmt.Printf("pinned in %s: %s\n", manifestPath, pinned)
	fmt.Printf("running:     %s\n", orPlaceholder(live))

	if err := compareSHA(pinned, live); err != nil {
		fail("bindery-ping drift: %v. Deploy the pinned image: kubectl apply -f %s", err, manifestPath)
	}
	fmt.Println("in step")
}

func orPlaceholder(s string) string {
	if s == "" {
		return "<absent>"
	}
	return s
}

// fail writes a GitHub Actions error annotation and exits non-zero.
func fail(format string, args ...any) {
	fmt.Printf("::error::"+format+"\n", args...)
	os.Exit(1)
}
