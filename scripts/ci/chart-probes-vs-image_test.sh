#!/usr/bin/env bash
# Tests for chart-probes-vs-image.sh. Run: bash scripts/ci/chart-probes-vs-image_test.sh
#
# The fixtures are helm template output for charts/bindery (readiness path
# /api/v1/health in "served", /api/v1/health/ready in "missing"). A throwaway
# python server stands in for the pinned image: it answers 200 on
# /api/v1/health and 401 on everything else, like v1.50.0 does for a route it
# does not have. Set CHART_PROBE_TEST_VERBOSE=1 to print the script's output.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$here/chart-probes-vs-image.sh"
served="$here/testdata/deployment-probe-served.yaml"
missing="$here/testdata/deployment-probe-missing.yaml"

tmp=$(mktemp -d)
server_pid=
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

# start_server OTHER_STATUS: serve 200 on /api/v1/health, OTHER_STATUS elsewhere.
start_server() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  python3 -I - "$1" >"$tmp/port" <<'PY' &
import http.server, sys
other = int(sys.argv[1])
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if self.path == "/api/v1/health" else other)
        self.send_header("Content-Length", "0")
        self.end_headers()
    def log_message(self, *a):
        pass
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
PY
  server_pid=$!
  for _ in $(seq 1 50); do
    [ -s "$tmp/port" ] && break
    sleep 0.1
  done
  [ -s "$tmp/port" ] || { echo "fake server did not start" >&2; exit 1; }
  port=$(cat "$tmp/port")
  : >"$tmp/port"
}

pass=0
failed=0

# check NAME WANT RENDERED [EXPECTED_OUTPUT_REGEX]
check() {
  local name=$1 want=$2 file=$3 re=${4:-} got
  if bash "$script" "$file" "http://127.0.0.1:$port" 1.50.0 "${SERVED_PORT:-8787}" >"$tmp/out" 2>&1; then
    got=accept
  else
    got=refuse
  fi
  if [ -n "${CHART_PROBE_TEST_VERBOSE:-}" ]; then sed 's/^/     /' "$tmp/out"; fi
  if [ "$got" = "$want" ] && { [ -z "$re" ] || grep -Eq "$re" "$tmp/out"; }; then
    pass=$((pass + 1))
    echo "ok   $name"
  else
    failed=$((failed + 1))
    echo "FAIL $name: want $want${re:+ matching $re}, got $got"
    sed 's/^/     /' "$tmp/out"
  fi
}

start_server 401
check "every probe path is served" accept "$served" 'readinessProbe /api/v1/health -> 200'
check "readiness path answers 401" refuse "$missing" 'readinessProbe path /api/v1/health/ready answered 401 from pinned image ghcr.io/vavallee/bindery:1.50.0'
check "failure explains the release lag" refuse "$missing" 'cannot ship a probe the pinned image does not serve'
SERVED_PORT=9999 check "probe port the image does not serve" refuse "$served" 'targets port 8787 but the pinned image'

start_server 404
check "readiness path answers 404" refuse "$missing" 'answered 404'

sed 's#/api/v1/health/ready#/api/v1/health#; s#/api/v1/health$#/healthz#' "$missing" >"$tmp/liveness-missing.yaml"
check "liveness path missing too" refuse "$tmp/liveness-missing.yaml" 'livenessProbe path /healthz answered 404'

# No httpGet probes at all (tcpSocket only) is nothing to check, not a failure.
awk '/^ *(readiness|liveness)Probe:/ {skip=1; match($0,/^ */); ind=RLENGTH; next}
     skip { match($0,/^ */); if (RLENGTH > ind || $0 ~ /^ *$/) next; skip=0 }
     { print }' "$missing" >"$tmp/tcp-only.yaml"
check "no httpGet probes" accept "$tmp/tcp-only.yaml" 'no httpGet probes'

# A server that is down is a failure, not a pass.
kill "$server_pid"; wait "$server_pid" 2>/dev/null || true; server_pid=
check "image not reachable" refuse "$served" 'no response|000'

echo "$pass passed, $failed failed"
[ "$failed" -eq 0 ]
