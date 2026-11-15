#!/usr/bin/env bash
# deploy-prod-guard.sh: decide whether the deploy-prod job may stamp the
# required status contexts green on its own promote PR.
#
# Usage: deploy-prod-guard.sh COMPARE_JSON BASE_SHA COMMIT_SHA...
#
#   COMPARE_JSON  file holding the response of
#                 GET /repos/{owner}/{repo}/compare/main...{deploy branch}
#   BASE_SHA      the main commit the job branched from
#   COMMIT_SHA    every commit the job created on the branch, oldest first
#
# Exits 0 only when all of these hold, and 1 with a reason on stderr otherwise:
#   * the branch is based on BASE_SHA and BASE_SHA is still the tip of main
#     (nothing landed on main since the job branched, nothing is behind)
#   * the branch carries exactly COMMIT_SHA, in order, and nothing else
#   * every one of those commits is authored by the github-actions bot
#   * the diff touches only charts/bindery/values.yaml and/or
#     charts/bindery/Chart.yaml, and only modifies them (no add, delete or
#     rename)
#
# The status stamp is what lets this PR merge past branch protection without
# lint, validate (Go) or Security Summary ever running on it, so anything we
# cannot prove is a values only promote must not get it.
set -euo pipefail

BOT_LOGIN='github-actions[bot]'
BOT_EMAIL='41898282+github-actions[bot]@users.noreply.github.com'
ALLOWED_FILES=(charts/bindery/values.yaml charts/bindery/Chart.yaml)

fail() {
  echo "deploy-prod-guard: $*" >&2
  exit 1
}

[ "$#" -ge 3 ] || fail "usage: $0 COMPARE_JSON BASE_SHA COMMIT_SHA..."
compare_json=$1
base_sha=$2
shift 2
expected_commits=("$@")

[ -r "$compare_json" ] || fail "cannot read $compare_json"
jq -e 'type == "object"' "$compare_json" >/dev/null 2>&1 || fail "$compare_json is not a JSON object"

field() { jq -r "$1" "$compare_json"; }

# Base: branched from BASE_SHA and main has not moved since.
status=$(field '.status')
behind_by=$(field '.behind_by')
ahead_by=$(field '.ahead_by')
total_commits=$(field '.total_commits')
merge_base=$(field '.merge_base_commit.sha')
main_tip=$(field '.base_commit.sha')

[ "$status" = "ahead" ] || fail "compare status is '$status', want 'ahead'"
[ "$behind_by" = "0" ] || fail "branch is $behind_by commit(s) behind main"
[ "$merge_base" = "$base_sha" ] || fail "merge base is $merge_base, want $base_sha"
[ "$main_tip" = "$base_sha" ] || fail "main is at $main_tip, the job branched from $base_sha"

# Commits: exactly the ones this job made, in order.
want_n=${#expected_commits[@]}
[ "$ahead_by" = "$want_n" ] || fail "branch is $ahead_by commit(s) ahead, want $want_n"
[ "$total_commits" = "$want_n" ] || fail "compare lists $total_commits commit(s), want $want_n"
mapfile -t got_commits < <(field '.commits[].sha')
[ "${#got_commits[@]}" = "$want_n" ] || fail "compare returned ${#got_commits[@]} commit(s), want $want_n"
for i in "${!expected_commits[@]}"; do
  [ "${got_commits[$i]}" = "${expected_commits[$i]}" ] ||
    fail "commit $((i + 1)) is ${got_commits[$i]}, want ${expected_commits[$i]}"
done

# Authorship: every commit is the github-actions bot.
bad_authors=$(jq -r --arg login "$BOT_LOGIN" --arg email "$BOT_EMAIL" '
  .commits[]
  | select((.author.login // "") != $login or (.commit.author.email // "") != $email)
  | "\(.sha) by \(.author.login // "unknown") <\(.commit.author.email // "unknown")>"
' "$compare_json")
[ -z "$bad_authors" ] || fail "commit(s) not authored by $BOT_LOGIN: $bad_authors"

# Files: only the two chart files, only modified.
files_count=$(field '.files | length')
[ "$files_count" -gt 0 ] || fail "the branch changes no files"
allowed_json=$(printf '%s\n' "${ALLOWED_FILES[@]}" | jq -R . | jq -s .)
bad_files=$(jq -r --argjson allowed "$allowed_json" '
  .files[]
  | select((.filename as $f | $allowed | index($f) | not) or .status != "modified")
  | "\(.filename) (\(.status))"
' "$compare_json")
[ -z "$bad_files" ] || fail "disallowed file change(s): $bad_files"

echo "deploy-prod-guard: ok, $want_n bot commit(s) on $base_sha touching only $(field '[.files[].filename] | join(", ")')"
