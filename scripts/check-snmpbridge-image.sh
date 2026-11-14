#!/usr/bin/env bash
# Build the SNMP bridge image and run it with no SNMPBRIDGE_CONFIG.
# Pass: the exit code is not 0 and the output holds SNMPBRIDGE_CONFIG.
# Needs docker.
set -uo pipefail
cd "$(dirname "$0")/.."
tag="snmpbridge-check:local"
if ! docker build -f internal/dockerfile_snmpbridge -t "$tag" .; then
	echo "FAIL: the image does not build" >&2
	exit 1
fi
out=$(docker run --rm "$tag" 2>&1)
code=$?
if [ "$code" -eq 0 ]; then
	echo "FAIL: the bridge exited with code 0 and no config" >&2
	exit 1
fi
if ! grep -q 'SNMPBRIDGE_CONFIG' <<<"$out"; then
	echo "FAIL: the output has no SNMPBRIDGE_CONFIG: $out" >&2
	exit 1
fi
echo "ok: the image builds, and the bridge stops with the config error (exit code $code)"
