#!/usr/bin/env bash
# Test for scripts/sync-upstream.sh. Run by hand: scripts/sync-upstream-test.sh
# It makes temporary git repos (an "upstream", a bare "origin" and a clone).
# It needs no network and no real test suite: SYNC_TEST_CMD and SYNC_RETEST_CMD are stubs.
set -uo pipefail

SCRIPT="$(cd "$(dirname "$0")" && pwd)/sync-upstream.sh"
ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com
export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

PASS=0
FAILED=0
check() { # check <name> <condition exit code>
  if [ "$2" -eq 0 ]; then
    PASS=$((PASS + 1))
    echo "ok   - $1"
  else
    FAILED=$((FAILED + 1))
    echo "FAIL - $1"
  fi
}
t() { "$@" >/dev/null 2>&1; }

UP="" ORIGIN="" CLONE="" OUT="" CODE=0

commit() { # commit <repo> <file> <content> <message>
  echo "$3" >"$1/$2"
  git -C "$1" add "$2"
  git -C "$1" commit -q -m "$4"
}

# setup [conflict]: upstream at v0.9.0, a clone with bridge = main + 1 own commit.
setup() {
  local d
  d="$(mktemp -d -p "$ROOT")"
  UP="$d/upstream"
  ORIGIN="$d/origin.git"
  CLONE="$d/clone"
  git init -q -b main "$UP"
  commit "$UP" a.txt base "c1"
  git -C "$UP" tag v0.9.0
  git clone -q --bare "$UP" "$ORIGIN"
  git clone -q "$ORIGIN" "$CLONE"
  git -C "$CLONE" remote add upstream "$UP"
  git -C "$CLONE" checkout -q -b bridge
  if [ "${1:-}" = conflict ]; then
    commit "$CLONE" a.txt bridge-change "own"
  else
    commit "$CLONE" own.txt own "own"
  fi
  git -C "$CLONE" push -q origin bridge
  # Upstream moves on: v0.10.0 (annotated), then an rc tag on a later commit.
  if [ "${1:-}" = conflict ]; then
    commit "$UP" a.txt upstream-change "c2"
  else
    commit "$UP" b.txt b "c2"
  fi
  git -C "$UP" tag -a -m v0.10.0 v0.10.0
  commit "$UP" c.txt c "c3"
  git -C "$UP" tag v9.9.9-rc1
}

run() { # run [args]: run the script in the clone, set OUT and CODE
  OUT="$(cd "$CLONE" && bash "$SCRIPT" "$@" 2>&1)"
  CODE=$?
}

snap() { # all refs that the script may change
  git -C "$CLONE" for-each-ref refs/heads refs/tags
  git -C "$ORIGIN" for-each-ref
  git -C "$CLONE" worktree list
}

# Stubs for the test command.
STUB="$ROOT/stub"
mkdir -p "$STUB"
printf '#!/bin/bash\nexit 0\n' >"$STUB/pass.sh"
printf '#!/bin/bash\nprintf "%%s\\n" "--- FAIL: TestSlow (0.10s)" "FAIL"; printf "FAIL\\tpkg/a\\t0.1s\\n"\nexit 1\n' >"$STUB/fail.sh"
printf '#!/bin/bash\necho "build error"\nexit 1\n' >"$STUB/builderr.sh"
printf '#!/bin/bash\necho "$@" >"%s/retest-args"\nexit 0\n' "$STUB" >"$STUB/retest-pass.sh"
printf '#!/bin/bash\nexit 1\n' >"$STUB/retest-fail.sh"
chmod +x "$STUB"/*.sh
export SYNC_TEST_CMD="$STUB/pass.sh"
export SYNC_RETEST_CMD="$STUB/retest-pass.sh"

echo "== success, tag choice, tag names =="
setup
before_tags="$(git -C "$CLONE" tag --list)"
run
check "exit 0" "$CODE"
check "no upstream tag in local tags" "$([ "$(git -C "$CLONE" tag --list | sort)" = "$(printf '%s\nbridge-v0.1.0\n' "$before_tags" | sort)" ] && echo 0 || echo 1)"
check "bridge holds the commit of v0.10.0" "$(t git -C "$CLONE" merge-base --is-ancestor "$(git -C "$UP" rev-parse 'v0.10.0^{commit}')" bridge && echo 0 || echo 1)"
check "bridge does not hold the rc commit" "$(t git -C "$CLONE" merge-base --is-ancestor "$(git -C "$UP" rev-parse v9.9.9-rc1)" bridge && echo 1 || echo 0)"
check "merge message" "$([ "$(git -C "$CLONE" log -1 --format=%s bridge)" = "chore(fork): merge upstream v0.10.0" ] && echo 0 || echo 1)"
check "tag points to the merge commit" "$([ "$(git -C "$CLONE" rev-parse bridge-v0.1.0)" = "$(git -C "$CLONE" rev-parse bridge)" ] && echo 0 || echo 1)"
check "main equals upstream main" "$([ "$(git -C "$CLONE" rev-parse main)" = "$(git -C "$UP" rev-parse main)" ] && echo 0 || echo 1)"
check "origin has main, bridge and the tag" "$([ "$(git -C "$ORIGIN" rev-parse main bridge bridge-v0.1.0 | tr '\n' ' ')" = "$(git -C "$CLONE" rev-parse main bridge bridge-v0.1.0 | tr '\n' ' ')" ] && echo 0 || echo 1)"
check "temporary worktree removed" "$([ "$(git -C "$CLONE" worktree list | wc -l)" -eq 1 ] && echo 0 || echo 1)"

echo "== nothing to do =="
s1="$(snap)"
run
check "exit 0" "$CODE"
check "prints nothing to do" "$(grep -q 'nothing to do' <<<"$OUT" && echo 0 || echo 1)"
check "nothing changed" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"

echo "== next patch tag, then --minor =="
commit "$UP" d.txt d "c4"
git -C "$UP" tag v0.10.1
run
check "patch: exit 0" "$CODE"
check "patch: bridge-v0.1.1" "$(t git -C "$CLONE" rev-parse -q --verify refs/tags/bridge-v0.1.1 && echo 0 || echo 1)"
commit "$UP" e.txt e "c5"
git -C "$UP" tag v0.10.2
old_bridge="$(git -C "$CLONE" rev-parse bridge)"
run --minor
check "minor: exit 0" "$CODE"
check "minor: bridge-v0.2.0" "$(t git -C "$CLONE" rev-parse -q --verify refs/tags/bridge-v0.2.0 && echo 0 || echo 1)"
check "bridge moved by fast-forward only" "$(t git -C "$CLONE" merge-base --is-ancestor "$old_bridge" bridge && [ "$(git -C "$CLONE" rev-parse bridge)" = "$(git -C "$CLONE" rev-parse bridge-v0.2.0)" ] && echo 0 || echo 1)"

echo "== release not on upstream/main =="
setup
git -C "$UP" checkout -q -b side v0.9.0
commit "$UP" side.txt s "side"
git -C "$UP" tag v1.0.0
git -C "$UP" checkout -q main
s1="$(snap)"
run
check "exit 1" "$([ "$CODE" -eq 1 ] && echo 0 || echo 1)"
check "names the ancestor problem" "$(grep -q 'not an ancestor' <<<"$OUT" && echo 0 || echo 1)"
check "nothing changed" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"

echo "== merge conflict =="
setup conflict
s1="$(snap)"
run
check "exit 1" "$([ "$CODE" -eq 1 ] && echo 0 || echo 1)"
check "prints the conflict file" "$(grep -q 'a.txt' <<<"$OUT" && echo 0 || echo 1)"
check "bridge, main, origin as before" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"
check "work tree clean" "$([ -z "$(git -C "$CLONE" status --porcelain)" ] && echo 0 || echo 1)"

echo "== tests fail and the retry fails =="
setup
s1="$(snap)"
SYNC_TEST_CMD="$STUB/fail.sh" SYNC_RETEST_CMD="$STUB/retest-fail.sh" run
check "exit 1" "$([ "$CODE" -eq 1 ] && echo 0 || echo 1)"
check "nothing changed" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"

echo "== tests fail with a build error (no package) =="
SYNC_TEST_CMD="$STUB/builderr.sh" run
check "exit 1" "$([ "$CODE" -eq 1 ] && echo 0 || echo 1)"
check "nothing changed" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"

echo "== tests fail and the retry passes =="
SYNC_TEST_CMD="$STUB/fail.sh" SYNC_RETEST_CMD="$STUB/retest-pass.sh" run
check "exit 0" "$CODE"
check "warning names the failed test" "$(grep -q 'TestSlow' <<<"$OUT" && echo 0 || echo 1)"
check "retry got the failed package" "$(grep -q 'pkg/a' "$STUB/retest-args" && echo 0 || echo 1)"
check "tag made" "$(t git -C "$CLONE" rev-parse -q --verify refs/tags/bridge-v0.1.0 && echo 0 || echo 1)"

echo "== --dry-run =="
setup
s1="$(snap)"
run --dry-run
check "exit 0" "$CODE"
check "prints the steps" "$(grep -q 'step 8: tests pass' <<<"$OUT" && grep -q 'step 3' <<<"$OUT" && echo 0 || echo 1)"
check "no local branch, tag or origin change" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"

echo "== start state =="
echo dirty >>"$CLONE/own.txt"
run
check "dirty tree: exit 1 with reason" "$([ "$CODE" -eq 1 ] && grep -q 'not clean' <<<"$OUT" && echo 0 || echo 1)"
git -C "$CLONE" checkout -q -- own.txt
git -C "$CLONE" checkout -q main
run
check "not on bridge: exit 1 with reason" "$([ "$CODE" -eq 1 ] && grep -q "'bridge' is not checked out" <<<"$OUT" && echo 0 || echo 1)"
git -C "$CLONE" checkout -q bridge
commit "$CLONE" extra.txt x "extra on main"
git -C "$CLONE" branch -f main HEAD
run
check "main cannot fast-forward: exit 1 with reason" "$([ "$CODE" -eq 1 ] && grep -q 'main cannot fast-forward' <<<"$OUT" && echo 0 || echo 1)"

echo "== stale local bridge =="
setup
commit "$CLONE" more.txt m "more"
git -C "$CLONE" push -q origin bridge
git -C "$CLONE" reset -q --hard HEAD~1
s1="$(snap)"
run
check "exit 1 with reason" "$([ "$CODE" -eq 1 ] && grep -q 'does not hold origin/bridge' <<<"$OUT" && echo 0 || echo 1)"
check "nothing changed" "$([ "$s1" = "$(snap)" ] && echo 0 || echo 1)"

echo "== push fails =="
setup
printf '#!/bin/sh\nexit 1\n' >"$ORIGIN/hooks/pre-receive"
chmod +x "$ORIGIN/hooks/pre-receive"
run
check "exit 1 with push command" "$([ "$CODE" -eq 1 ] && grep -q 'push failed' <<<"$OUT" && echo 0 || echo 1)"

echo
echo "passed: $PASS, failed: $FAILED"
[ "$FAILED" -eq 0 ]
