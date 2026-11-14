#!/usr/bin/env bash
# Bring the newest upstream release into `bridge`, run the tests and make a tag.
#
# Usage: scripts/sync-upstream.sh [--dry-run] [--minor]
#
#   --dry-run  Do steps 1 to 8 only. Change no local branch, make no tag, push nothing.
#              A fetch into upstream/* and the tags of origin is allowed.
#   --minor    Bump the minor number of the bridge-v* tag, and set the patch number to 0.
#
# Env (for the test script):
#   SYNC_TEST_CMD     test command, default: make test
#   SYNC_RETEST_CMD   command that retries the failed packages, default:
#                     go test -count=1 -tags='testing no_ui'
#
# Exit codes: 0 for success and for "nothing to do". 1 for each stop.
set -Eeuo pipefail
# why: a failed git command must exit 1 with a message, not with its own code (often 128).
on_err() {
  echo "[sync] STOP: command failed at line $1" >&2
  exit 1
}
trap 'on_err $LINENO' ERR

DRY=0
MINOR=0
TEST_CMD="${SYNC_TEST_CMD:-make test}"
RETEST_CMD="${SYNC_RETEST_CMD:-go test -count=1 -tags='testing no_ui'}"
TMP=""

say() { echo "[sync] $*"; }
die() {
  echo "[sync] STOP: $*" >&2
  exit 1
}

LOG=""
cleanup() {
  [ -z "$LOG" ] || rm -f "$LOG"
  if [ -n "$TMP" ] && [ -d "$TMP" ]; then
    git worktree remove --force "$TMP" >/dev/null 2>&1 || true
    rm -rf "$TMP"
    git worktree prune >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY=1 ;;
    --minor) MINOR=1 ;;
    -h | --help)
      sed -n '2,15p' "$0"
      exit 0
      ;;
    *) die "unknown argument: $arg" ;;
  esac
done

TOP="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not in a git repo"
cd "$TOP"

# Step 1: start state.
branch="$(git symbolic-ref --short -q HEAD || true)"
[ "$branch" = "bridge" ] || die "branch 'bridge' is not checked out (now: ${branch:-detached})"
[ -z "$(git status --porcelain)" ] || die "the work tree is not clean"
say "step 1: on bridge, work tree clean"

# Step 2: fetch.
git fetch --no-tags upstream
git fetch --tags origin
say "step 2: fetched upstream (no tags) and the tags of origin"
# why: a stale local bridge or main makes the atomic push fail after the local refs moved.
git rev-parse -q --verify refs/heads/main >/dev/null ||
  die "local branch main is missing. Run: git branch main origin/main"
git merge-base --is-ancestor refs/remotes/origin/bridge HEAD ||
  die "local bridge does not hold origin/bridge. Run: git pull"

# Step 3: highest plain vX.Y.Z tag of upstream.
line="$(git ls-remote --tags --refs upstream |
  awk '$2 ~ /^refs\/tags\/v[0-9]+\.[0-9]+\.[0-9]+$/ {sub("refs/tags/", "", $2); print $2, $1}' |
  sort -V | tail -n 1)"
[ -n "$line" ] || die "upstream has no plain vX.Y.Z tag"
release="${line% *}"
tagsha="${line#* }"
git fetch --no-tags upstream "$tagsha"
commit="$(git rev-parse "FETCH_HEAD^{commit}")"
say "step 3: newest upstream release is $release ($commit)"

# Step 4: the release must be on upstream/main.
git merge-base --is-ancestor "$commit" refs/remotes/upstream/main ||
  die "$release ($commit) is not an ancestor of upstream/main"
say "step 4: $release is an ancestor of upstream/main"

# Step 5: is the release already in bridge?
in_bridge=0
if git merge-base --is-ancestor "$commit" HEAD; then in_bridge=1; fi
if [ "$in_bridge" = 1 ] && [ -n "$(git tag --points-at HEAD --list 'bridge-v*')" ]; then
  say "step 5: nothing to do ($release is in bridge and bridge has a bridge-v* tag)"
  exit 0
fi
say "step 5: release in bridge: $([ "$in_bridge" = 1 ] && echo yes || echo no)"

# New tag name.
last="$(git tag --list 'bridge-v*' | grep -E '^bridge-v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"
if [ -z "$last" ]; then
  newtag="bridge-v0.1.0"
else
  ver="${last#bridge-v}"
  IFS=. read -r major minor patch <<<"$ver"
  if [ "$MINOR" = 1 ]; then
    newtag="bridge-v${major}.$((minor + 1)).0"
  else
    newtag="bridge-v${major}.${minor}.$((patch + 1))"
  fi
fi

# Step 6: main must fast-forward to upstream/main.
git merge-base --is-ancestor refs/heads/main refs/remotes/upstream/main ||
  die "main cannot fast-forward to upstream/main"
say "step 6: main can fast-forward to upstream/main"

# Step 7: merge in a temporary worktree.
TMP="$(mktemp -d)"
git worktree add --detach "$TMP" refs/heads/bridge >/dev/null
if [ "$in_bridge" = 1 ]; then
  say "step 7: release already in bridge, no merge"
else
  if ! git -C "$TMP" merge --no-ff -m "chore(fork): merge upstream $release" "$commit" >/dev/null 2>&1; then
    files="$(git -C "$TMP" diff --name-only --diff-filter=U)"
    git -C "$TMP" merge --abort >/dev/null 2>&1 || true
    echo "[sync] conflict in:" >&2
    echo "$files" | sed 's/^/[sync]   /' >&2
    die "merge conflict with $release. Merge aborted"
  fi
  say "step 7: merged $release into a copy of bridge"
fi
tested="$(git -C "$TMP" rev-parse HEAD)"

# Step 8: tests.
mkdir -p "$TMP/internal/site/dist"
touch "$TMP/internal/site/dist/index.html"
log="$(mktemp)"
LOG="$log"
say "step 8: run tests: $TEST_CMD"
rc=0
(cd "$TMP" && bash -c "$TEST_CMD") >"$log" 2>&1 || rc=$?
cat "$log"
if [ "$rc" -ne 0 ]; then
  if awk -F'\t' '$1 == "FAIL" && $2 ~ / \[/ {f=1} END {exit !f}' "$log"; then
    die "tests failed with a build or setup error"
  fi
  mapfile -t pkgs < <(awk -F'\t' '$1 == "FAIL" && NF >= 2 {print $2}' "$log" | sort -u)
  [ "${#pkgs[@]}" -gt 0 ] || die "tests failed and the output names no failed package"
  say "step 8: retry the failed packages: ${pkgs[*]}"
  rc2=0
  (cd "$TMP" && bash -c "$RETEST_CMD \"\$@\"" _ "${pkgs[@]}") || rc2=$?
  [ "$rc2" -eq 0 ] || die "failed packages fail again in the retry: ${pkgs[*]}"
  echo "[sync] WARNING: flaky tests failed in the first run and passed in the retry:" >&2
  grep -E '^\s*--- FAIL:' "$log" | sed 's/^\s*/[sync]   /' >&2 || true
fi
say "step 8: tests pass"

if [ "$DRY" = 1 ]; then
  say "dry run: would fast-forward main and bridge to the tested commit $tested"
  say "dry run: would make tag $newtag and push main, bridge and $newtag"
  exit 0
fi

# Step 9: move the branches and make the tag.
git merge --ff-only "$tested" >/dev/null || die "cannot fast-forward bridge to $tested"
git fetch --no-tags . refs/remotes/upstream/main:refs/heads/main ||
  die "cannot fast-forward main to upstream/main"
git tag "$newtag" "$tested"
say "step 9: main and bridge moved, tag $newtag on $tested"

# Step 10: atomic push.
git push --atomic origin main bridge "refs/tags/$newtag" ||
  die "push failed. The local refs moved. Fix the cause, then run: git push --atomic origin main bridge refs/tags/$newtag"
say "step 10: pushed main, bridge and $newtag"
