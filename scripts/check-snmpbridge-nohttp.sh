#!/usr/bin/env bash
# Check that the SNMP bridge builds and passes its tests without the HTTP source.
# Pass: go vet, go build and go test pass on a copy of internal/snmpbridge with no http*.go file.
# Run it by hand. It is not part of make test.
set -euo pipefail
cd "$(dirname "$0")/.."
# why: the copy must be inside the module, so that its imports resolve.
copy=$(mktemp -d internal/snmpbridge-nohttp.XXXXXX)
trap 'rm -rf "$copy"' EXIT
tar -C internal/snmpbridge --exclude='http*.go' --exclude='http*.json' -cf - . | tar -C "$copy" -xf -
if compgen -G "$copy/http*" >/dev/null; then
	echo "FAIL: the copy still holds an http file" >&2
	exit 1
fi
# why: a test file that needs a hook that the http files set would fail only here.
go vet "./$copy/"
go build "./$copy/"
go test "./$copy/"
echo "ok: the package passes vet, build and test with no http*.go file"
