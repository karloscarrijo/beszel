#!/usr/bin/env bash
# Exit 1 when the hub command or the agent command imports gosnmp.
# why: gosnmp belongs to the SNMP bridge only. The stock binaries stay small.
set -euo pipefail
cd "$(dirname "$0")/.."
status=0
for pkg in ./internal/cmd/hub ./internal/cmd/agent; do
	# why: -e lists the deps when the web build (internal/site/dist) is absent.
	# The check of the package itself stops the script when go list gives no list.
	deps=$(go list -e -deps -f '{{.ImportPath}}' "$pkg")
	if ! grep -q "^github.com/henrygd/beszel/${pkg#./}\$" <<<"$deps"; then
		echo "FAIL: go list gave no deps for $pkg" >&2
		exit 1
	fi
	if grep -q '^github.com/gosnmp/gosnmp' <<<"$deps"; then
		echo "FAIL: $pkg imports gosnmp" >&2
		status=1
	fi
done
if [ "$status" -eq 0 ]; then
	echo "ok: the hub and agent commands do not import gosnmp"
fi
exit "$status"
