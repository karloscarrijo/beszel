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

check_main() {
  git merge-base --is-ancestor refs/heads/main refs/remotes/upstream/main ||
    die "main cannot fast-forward to upstream/main"
}
# The fetch refuses a main that is checked out in a different worktree.
ff_main() {
  git fetch --no-tags . refs/remotes/upstream/main:refs/heads/main ||
    die "cannot fast-forward main to upstream/main"
}

LOG=""
LOG2=""
cleanup() {
  [ -z "$LOG" ] || rm -f "$LOG"
  [ -z "$LOG2" ] || rm -f "$LOG2"
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
# why: origin/main with a commit that upstream/main does not have makes the push of main fail.
git merge-base --is-ancestor refs/remotes/origin/main refs/remotes/upstream/main ||
  die "origin/main has commits that are not in upstream/main"

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

# Step 5: is the release already in bridge, and does origin have bridge and the newest tag?
in_bridge=0
if git merge-base --is-ancestor "$commit" HEAD; then in_bridge=1; fi
headtag="$(git tag --points-at HEAD --list 'bridge-v*' | sort -V | tail -n 1)"
last="$(git tag --list 'bridge-v*' | grep -E '^bridge-v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"
# why: after a failed push the local refs and the tag moved, and origin did not.
# Check the newest tag, not only the tag on HEAD: a new upstream release must not hide it.
if [ -n "$last" ]; then
  remote_tag="$(git ls-remote --tags --refs origin "refs/tags/$last")" ||
    die "cannot read the tags of origin"
  remote_tag="${remote_tag%%[[:space:]]*}"
  if [ "$remote_tag" != "$(git rev-parse "refs/tags/$last")" ] ||
    { [ "$last" = "$headtag" ] && [ "$(git rev-parse refs/remotes/origin/bridge)" != "$(git rev-parse HEAD)" ]; }; then
    say "step 5: origin does not have $last or bridge (a push failed)"
    if [ "$DRY" = 1 ]; then
      say "dry run: would fast-forward main, then push main, bridge and $last again, with no new test run"
      exit 0
    fi
    # why: origin/main can have moved since the failed push. upstream/main holds it (step 2).
    ff_main
    # why: the tag marks the tested commit, so a new test run is not necessary.
    git push --atomic origin main bridge "refs/tags/$last" ||
      die "push failed. Fix the cause, then run the script again"
    say "step 5: pushed main, bridge and $last with no new test run"
    [ "$in_bridge" = 1 ] || say "run the script again to sync $release"
    exit 0
  fi
fi
if [ "$in_bridge" = 1 ] && [ -n "$headtag" ]; then
  if [ "$(git rev-parse refs/remotes/origin/main)" = "$(git rev-parse refs/remotes/upstream/main)" ]; then
    say "step 5: nothing to do ($release is in bridge, and origin has bridge, $headtag and main)"
    exit 0
  fi
  # why: main mirrors upstream, also when upstream has no new release.
  check_main
  if [ "$DRY" = 1 ]; then
    say "dry run: bridge is up to date, would fast-forward main to upstream/main and push main"
    exit 0
  fi
  ff_main
  git push origin main || die "push of main failed"
  say "step 5: bridge is up to date, pushed main"
  exit 0
fi
say "step 5: release in bridge: $([ "$in_bridge" = 1 ] && echo yes || echo no)"

# New tag name.
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
check_main
say "step 6: main can fast-forward to upstream/main"

# Step 7: merge in a temporary worktree.
TMP="$(mktemp -d)"
git worktree add --detach "$TMP" refs/heads/bridge >/dev/null
if [ "$in_bridge" = 1 ]; then
  say "step 7: release already in bridge, no merge"
else
  if ! mout="$(git -C "$TMP" merge --no-ff -m "chore(fork): merge upstream $release" "$commit" 2>&1)"; then
    files="$(git -C "$TMP" diff --name-only --diff-filter=U)"
    git -C "$TMP" merge --abort >/dev/null 2>&1 || true
    if [ -z "$files" ]; then
      echo "$mout" | sed 's/^/[sync]   /' >&2
      die "merge of $release failed with no conflict. Merge aborted"
    fi
    echo "[sync] conflict in:" >&2
    echo "$files" | sed 's/^/[sync]   /' >&2
    die "merge conflict with $release. Merge aborted"
  fi
  say "step 7: merged $release into a copy of bridge"
fi
tested="$(git -C "$TMP" rev-parse HEAD)"

# Step 7b: the Go pin of the fork image files must cover the Go version of go.mod.
# why: a merge of an upstream release can raise `go` in go.mod. The image would then
# fail to build, and `make test` does not see it.
[ -f "$TMP/go.mod" ] || die "go.mod is missing in the merged copy"
gomod_ver="$(awk '$1 == "go" {print $2; exit}' "$TMP/go.mod")"
[ -n "$gomod_ver" ] || die "go.mod of the merged copy has no go line"
gomod_mm="$(echo "$gomod_ver" | awk -F. '{print $1 "." $2}')"
for f in internal/dockerfile_hub_ui internal/dockerfile_snmpbridge; do
  pins="$(sed -n 's/^FROM .*golang:\([0-9][0-9]*\.[0-9][0-9]*\)[-.a-z0-9]*.*/\1/p' "$TMP/$f" 2>/dev/null || true)"
  [ -n "$pins" ] || die "$f has no golang image with a version tag"
  for pin in $pins; do
    if [ "$(printf '%s\n%s\n' "$gomod_mm" "$pin" | sort -V | tail -n 1)" != "$pin" ]; then
      die "go.mod needs Go $gomod_ver, but the golang image in $f is $pin. Raise the Go pin in internal/dockerfile_hub_ui and internal/dockerfile_snmpbridge"
    fi
  done
done
say "step 7b: the Go pin of the image files covers go $gomod_ver"

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
  LOG2="$(mktemp)"
  log2="$LOG2"
  # why: tee shows the retry output while it runs. The retry of a slow package takes minutes.
  (cd "$TMP" && bash -c "$RETEST_CMD \"\$@\"" _ "${pkgs[@]}") 2>&1 | tee "$log2" || rc2=${PIPESTATUS[0]}
  if [ "$rc2" -ne 0 ]; then
    if awk -F'\t' '$1 == "FAIL" && $2 ~ / \[/ {f=1} END {exit !f}' "$log2"; then
      die "the retry failed with a build or setup error"
    fi
    # why: name only the packages that fail in the retry, not those of the first run.
    mapfile -t pkgs2 < <(awk -F'\t' '$1 == "FAIL" && NF >= 2 {print $2}' "$log2" | sort -u)
    if [ "${#pkgs2[@]}" -gt 0 ]; then
      die "failed packages fail again in the retry: ${pkgs2[*]}"
    fi
    die "the retry failed and its output names no failed package"
  fi
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
# why: main first. The fetch refuses a main that is checked out in a different worktree,
# and a stop then must not leave a moved bridge with no tag.
ff_main
git merge --ff-only "$tested" >/dev/null || die "cannot fast-forward bridge to $tested"
git tag "$newtag" "$tested"
say "step 9: main and bridge moved, tag $newtag on $tested"

# Step 10: atomic push.
git push --atomic origin main bridge "refs/tags/$newtag" ||
  die "push failed. The local refs moved. Fix the cause, then run: git push --atomic origin main bridge refs/tags/$newtag"
say "step 10: pushed main, bridge and $newtag"
