#!/usr/bin/env bash
# Check the .snmprec files of this folder for private data before a commit.
#
# Usage: ./check.sh STRING [STRING...]
#
# Give each private string as an argument: the homelab domain, the host names
# and the community. Why: the repo is public, so no private string goes into a
# file of the repo, also not into this script.
#
# The script checks the value of each row of each *.snmprec file in this
# folder. It does not check the OIDs. It finds:
#   - an IPv4 address in 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16
#   - a MAC address: 6 hex pairs with ':' or '-', or a hex value (type 4x)
#     of 6 bytes that is not text
#   - each STRING, with no case match
# It decodes the hex values (type 4x) and checks the decoded text.
# Exit 0: nothing found. Exit 1: a finding. Exit 2: usage or setup error.
set -euo pipefail
shopt -s inherit_errexit nullglob

if [ "$#" -eq 0 ]; then
	echo "usage: $0 STRING [STRING...]" >&2
	echo "Give the homelab domain and the community as arguments." >&2
	exit 2
fi
for s in "$@"; do
	if [ -z "$s" ]; then
		# Why: an empty string matches each row. An unset variable gives one.
		echo "an argument is empty: set each private string" >&2
		exit 2
	fi
done
if ! command -v xxd >/dev/null; then
	echo "xxd is missing: install xxd (package xxd or vim-common)" >&2
	exit 2
fi

dir="$(cd "$(dirname "$0")" && pwd)"
files=("$dir"/*.snmprec)
if [ "${#files[@]}" -eq 0 ]; then
	echo "no .snmprec file in $dir" >&2
	exit 2
fi

private_ip='(^|[^0-9])(10\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])|192\.168)\.[0-9]{1,3}\.[0-9]{1,3}([^0-9]|$)'
mac='([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}'

# Print the value of each row, one line for each row, with no OID.
# Decode a hex value (type 4x). Print a 6-byte hex value that is not text as
# a MAC address (aa:bb:cc:dd:ee:ff), so that the MAC check finds it.
values() {
	local oid type value nontext
	while IFS='|' read -r oid type value; do
		if [ "$type" = "4x" ]; then
			nontext="$(printf '%s' "$value" | xxd -r -p | tr -d '[:print:]' | wc -c)"
			if [ "${#value}" -eq 12 ] && [ "$nontext" -gt 0 ]; then
				value="$(sed 's/../&:/g; s/:$//' <<<"$value")"
			else
				value="$(printf '%s' "$value" | xxd -r -p | tr -c '[:print:]' '.')"
			fi
		fi
		printf '%s\n' "$value"
	done <"$1"
}

found=0
for f in "${files[@]}"; do
	name="$(basename "$f")"
	text="$(values "$f")"
	# grep -n gives the row number in the file.
	if grep -n -E "$private_ip" <<<"$text" | sed "s/^/$name:/"; then
		echo "private address found in $name" >&2
		found=1
	fi
	if grep -n -E "$mac" <<<"$text" | sed "s/^/$name:/"; then
		echo "MAC address found in $name" >&2
		found=1
	fi
	i=0
	for s in "$@"; do
		i=$((i + 1))
		# Do not print the row or the string: both hold private data.
		if grep -q -i -F -- "$s" <<<"$text"; then
			echo "private string number $i found in $name" >&2
			found=1
		fi
	done
done

if [ "$found" -eq 0 ]; then
	echo "OK: no private data in ${#files[@]} files"
fi
exit "$found"
