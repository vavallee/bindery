#!/usr/bin/env bash
# Tests for deploy-prod-guard.sh. Run: bash scripts/ci/deploy-prod-guard_test.sh
#
# The fixture is the real compare response for the v1.50.0 promote PR (#3818),
# trimmed to the fields the guard reads. Each case mutates it with jq and checks
# whether the guard accepts or refuses. Set GUARD_TEST_VERBOSE=1 to print the
# guard's reason for every case.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
guard="$here/deploy-prod-guard.sh"
fixture="$here/testdata/compare-v1.50.0.json"

BASE=26cbcb2744c1a3f34056a2af21392fafe9c8f43b
C1=a1af08942b6bbb651a5ff02f6166c737de77a6f4
C2=3c53b48eb923f8efa18d0b04a97cf2140a5a345a

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

pass=0
failed=0

# check NAME WANT JQ_FILTER [GUARD_ARGS...]
#   WANT is accept or refuse. GUARD_ARGS default to "$BASE $C1 $C2".
check() {
  local name=$1 want=$2 filter=$3
  shift 3
  local args=("$@")
  [ "${#args[@]}" -gt 0 ] || args=("$BASE" "$C1" "$C2")
  jq "$filter" "$fixture" >"$tmp/case.json"
  local got
  if bash "$guard" "$tmp/case.json" "${args[@]}" >"$tmp/out" 2>&1; then
    got=accept
  else
    got=refuse
  fi
  if [ -n "${GUARD_TEST_VERBOSE:-}" ]; then sed 's/^/     /' "$tmp/out"; fi
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
    echo "ok   $name"
  else
    failed=$((failed + 1))
    echo "FAIL $name: want $want, got $got"
    sed 's/^/     /' "$tmp/out"
  fi
}

check "real v1.50.0 promote" accept '.'
check "values.yaml only" accept '.files |= map(select(.filename == "charts/bindery/values.yaml"))'
check "Chart.yaml only" accept '.files |= map(select(.filename == "charts/bindery/Chart.yaml"))'

check "extra workflow file" refuse '.files += [{"filename": ".github/workflows/ci.yml", "status": "modified"}]'
check "extra Go file" refuse '.files += [{"filename": "cmd/bindery/main.go", "status": "modified"}]'
check "values-dev.yaml instead" refuse '.files[1].filename = "charts/bindery/values-dev.yaml"'
check "chart template" refuse '.files[0].filename = "charts/bindery/templates/deployment.yaml"'
check "values.yaml removed" refuse '.files[1].status = "removed"'
check "values.yaml renamed in" refuse '.files[1].status = "renamed"'
check "no files" refuse '.files = []'

check "third commit" refuse '.commits += [.commits[1] | .sha = "1111111111111111111111111111111111111111"] | .ahead_by = 3 | .total_commits = 3'
check "commit swapped" refuse '.commits[1].sha = "2222222222222222222222222222222222222222"'
check "commits reordered" refuse '.commits |= reverse'
check "only one commit" refuse '.commits |= .[:1] | .ahead_by = 1 | .total_commits = 1'
check "ahead_by mismatch" refuse '.ahead_by = 3'
check "human author login" refuse '.commits[1].author.login = "someone"'
check "human author email" refuse '.commits[0].commit.author.email = "someone@example.com"'
check "no GitHub user on commit" refuse '.commits[0].author = null'

check "behind main" refuse '.behind_by = 1 | .status = "diverged"'
check "main moved on" refuse '.base_commit.sha = "3333333333333333333333333333333333333333"'
check "different merge base" refuse '.merge_base_commit.sha = "4444444444444444444444444444444444444444"'
check "identical status" refuse '.status = "identical"'

check "wrong BASE_SHA argument" refuse '.' 5555555555555555555555555555555555555555 "$C1" "$C2"
check "expects one more commit" refuse '.' "$BASE" "$C1" "$C2" 6666666666666666666666666666666666666666
check "missing commit arguments" refuse '.' "$BASE"

# The deploy jobs' gh_get helper exits 1 on a non 404 API error. That exit is
# lost when the substitution is used directly as a [ or if argument (set -e
# does not fire there), so a 5xx would read as "not found". Every call must
# assign to a variable first.
workflow="$here/../../.github/workflows/ci.yml"
if masked=$(grep -nE '(\[|if|&&|\|\|)[^=]*\$\(gh_get' "$workflow"); then
  failed=$((failed + 1))
  echo "FAIL gh_get used inside a condition, assign it to a variable first:"
  printf '     %s\n' "${masked//$'\n'/$'\n'     }"
else
  pass=$((pass + 1))
  echo "ok   gh_get results are assigned before they are tested"
fi

echo
echo "$pass passed, $failed failed"
[ "$failed" -eq 0 ]
