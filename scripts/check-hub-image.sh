#!/usr/bin/env bash
# Build the hub image from a clean copy of the commit HEAD, then check that the hub serves the UI.
# Clean copy: only the files of the commit. No dist and no node_modules.
# Pass: GET / gives the HTML of the app, and one asset that the HTML names gives 200.
# Needs docker, git, tar and curl. Set PORT to change the host port (default 18090).
# Set TREE=working to build the working tree (for a check before the commit).
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
tag="beszel-hub-check:local"
port="${PORT:-18090}"
work=$(mktemp -d)
name="beszel-hub-check-$$"
cleanup() {
	docker rm -f "$name" >/dev/null 2>&1
	rm -rf "$work"
}
trap cleanup EXIT

mkdir "$work/src"
if [ "${TREE:-}" = "working" ]; then
	# Before a commit: the files that git tracks or would track (not ignored).
	# This can differ from the commit: an untracked file that you forget to add is in the copy.
	if ! git ls-files -z --cached --others --exclude-standard | tar --null --ignore-failed-read -T - -cf - | tar -xf - -C "$work/src"; then
		echo "FAIL: the copy of the working tree does not work" >&2
		exit 1
	fi
else
	# Default: the files of the commit HEAD, as a clean clone has them.
	if ! git archive HEAD | tar -xf - -C "$work/src"; then
		echo "FAIL: the copy of HEAD does not work" >&2
		exit 1
	fi
fi
if [ -e "$work/src/internal/site/dist" ] || [ -e "$work/src/internal/site/node_modules" ]; then
	echo "FAIL: the clone has dist or node_modules" >&2
	exit 1
fi

start=$SECONDS
if ! docker build -f "$work/src/internal/dockerfile_hub_ui" -t "$tag" "$work/src"; then
	echo "FAIL: the image does not build" >&2
	exit 1
fi
echo "build time: $((SECONDS - start)) s"

docker run -d --name "$name" -p "127.0.0.1:$port:8090" "$tag" >/dev/null || {
	echo "FAIL: the container does not start" >&2
	exit 1
}

html=""
for _ in $(seq 1 30); do
	html=$(curl -fsS "http://127.0.0.1:$port/" 2>/dev/null) && break
	html=""
	sleep 1
done
if [ -z "$html" ]; then
	echo "FAIL: GET / gives no answer" >&2
	docker logs "$name" >&2
	exit 1
fi
if ! grep -q '<div id="app"' <<<"$html"; then
	echo "FAIL: GET / is not the HTML of the app" >&2
	exit 1
fi
asset=$(grep -o '/assets/[^"]*\.js' <<<"$html" | head -n 1)
if [ -z "$asset" ]; then
	echo "FAIL: the HTML names no asset" >&2
	exit 1
fi
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port$asset")
if [ "$code" != "200" ]; then
	echo "FAIL: GET $asset gives $code" >&2
	exit 1
fi
echo "ok: the hub image builds from a clean clone, serves the HTML, and serves $asset (200)"
