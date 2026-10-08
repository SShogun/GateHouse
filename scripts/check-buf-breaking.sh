#!/usr/bin/env bash
set -euo pipefail

fixture_root="tests/fixtures/proto-compat"
diagnostic_file="$(mktemp)"
trap 'rm -f "$diagnostic_file"' EXIT

if buf breaking "$fixture_root/breaking" --against "$fixture_root/baseline" >"$diagnostic_file" 2>&1; then
	cat "$diagnostic_file"
	echo "expected Buf to reject the fixture with a deleted field" >&2
	exit 1
else
	breaking_status=$?
fi

if ! grep -Fq 'Previously present field "2" with name "removed" on message "Contract" was deleted.' "$diagnostic_file"; then
	cat "$diagnostic_file" >&2
	echo "Buf failed for a reason other than the expected field deletion" >&2
	exit 1
fi

cat "$diagnostic_file"
echo "Buf rejected the intentionally breaking fixture with exit code ${breaking_status}."

buf breaking "$fixture_root/compatible" --against "$fixture_root/baseline"
echo "Buf accepted the compatible fixture."
