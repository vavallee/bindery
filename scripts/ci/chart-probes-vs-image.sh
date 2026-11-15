#!/usr/bin/env bash
# chart-probes-vs-image.sh: fail when the chart on main declares an HTTP probe
# that the image pinned in charts/bindery/values.yaml does not serve.
#
# Usage: chart-probes-vs-image.sh RENDERED_YAML BASE_URL IMAGE_TAG [SERVED_PORT]
#
#   RENDERED_YAML  output of `helm template` for charts/bindery
#   BASE_URL       where the pinned image answers, e.g. http://127.0.0.1:18787
#   IMAGE_TAG      the pinned tag, used only in messages
#   SERVED_PORT    container port that BASE_URL maps to (default 8787)
#
# Why this exists: the prod ArgoCD app syncs charts/bindery from main, but the
# image tag in values.yaml stays on the last release until deploy-prod bumps
# it. A chart change that needs a newer binary therefore runs against the old
# one. A readiness probe on a route the old image lacks (it answers 401) kept
# the prod pod from ever becoming Ready, and nothing noticed for hours.
#
# Every httpGet probe (startup, liveness, readiness) is requested and must
# answer 200. Probes that are not httpGet (tcpSocket, exec) are skipped.
set -euo pipefail

fail() {
  echo "chart-probes-vs-image: $*" >&2
  exit 1
}

[ "$#" -ge 3 ] || fail "usage: $0 RENDERED_YAML BASE_URL IMAGE_TAG [SERVED_PORT]"
rendered=$1
base_url=${2%/}
tag=$3
served_port=${4:-8787}
[ -f "$rendered" ] || fail "rendered chart not found: $rendered"

# Prints "NAME PATH PORT" for each httpGet probe on the Deployment's
# containers, with named ports resolved through containerPort. Tiny indent
# based reader because the runner has no YAML library for python3 and the
# helm output shape is regular.
probes=$(python3 -I - "$rendered" <<'PY'
import re, sys

lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
docs, cur = [], []
for ln in lines:
    if ln.strip() == "---":
        docs.append(cur)
        cur = []
    else:
        cur.append(ln)
docs.append(cur)
dep = [d for d in docs if any(re.match(r"kind:\s*Deployment\s*$", x) for x in d)]
if not dep:
    sys.exit("no Deployment in rendered chart")
body = dep[0]

def indent(s):
    return len(s) - len(s.lstrip(" "))

def block(start):
    """Lines nested deeper than body[start]."""
    base, out = indent(body[start]), []
    for ln in body[start + 1:]:
        if ln.strip() and not ln.lstrip().startswith("#") and indent(ln) <= base:
            break
        out.append(ln)
    return out

ports = {}
for i, ln in enumerate(body):
    if re.match(r"\s*ports:\s*$", ln):
        blk = block(i)
        name = None
        for b in blk:
            m = re.match(r"\s*-?\s*name:\s*(\S+)", b)
            if m:
                name = m.group(1).strip("\"'")
            m = re.match(r"\s*-?\s*containerPort:\s*(\d+)", b)
            if m and name:
                ports[name] = m.group(1)

for i, ln in enumerate(body):
    m = re.match(r"\s*(startup|liveness|readiness)Probe:\s*$", ln)
    if not m:
        continue
    blk = block(i)
    for j, b in enumerate(blk):
        if re.match(r"\s*httpGet:\s*$", b):
            sub = []
            base = indent(b)
            for c in blk[j + 1:]:
                if c.strip() and indent(c) <= base:
                    break
                sub.append(c)
            path = port = None
            for c in sub:
                pm = re.match(r"\s*path:\s*(\S+)", c)
                if pm:
                    path = pm.group(1).strip("\"'")
                pm = re.match(r"\s*port:\s*(\S+)", c)
                if pm:
                    port = pm.group(1).strip("\"'")
            if path is None or port is None:
                sys.exit("httpGet without path or port in " + m.group(1) + "Probe")
            port = ports.get(port, port)
            print(m.group(1) + "Probe", path, port)
PY
) || fail "could not read probes from $rendered"

[ -n "$probes" ] || { echo "chart-probes-vs-image: no httpGet probes in the rendered chart, nothing to check"; exit 0; }

bad=0
while read -r name path port; do
  if [ "$port" != "$served_port" ]; then
    echo "chart-probes-vs-image: FAIL $name targets port $port but the pinned image $tag serves $served_port" >&2
    bad=1
    continue
  fi
  status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$base_url$path" || true)
  if [ "$status" = "200" ]; then
    echo "ok   $name $path -> 200"
  else
    echo "FAIL $name $path -> ${status:-no response}" >&2
    echo "chart-probes-vs-image: $name path $path answered ${status:-no response} from pinned image ghcr.io/vavallee/bindery:$tag, want 200." >&2
    bad=1
  fi
done <<<"$probes"

if [ "$bad" -ne 0 ]; then
  cat >&2 <<MSG

The chart on main cannot ship a probe the pinned image does not serve.
The prod ArgoCD app syncs charts/bindery from main, but the image tag in
charts/bindery/values.yaml ($tag) only moves when a release is tagged and
deploy-prod bumps it. A probe on a route this image lacks (it answers 401 or
404) keeps the pod from ever becoming Ready, which took prod down once already.
Either keep the probe on a path $tag serves, release first and flip the probe
in a follow-up PR once prod runs the new image, or gate the change behind a
value that defaults to the old behaviour.
MSG
  exit 1
fi
