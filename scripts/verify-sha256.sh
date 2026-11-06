#!/bin/sh
# Verify the SHA-256 of a downloaded file.
#
# Usage: verify-sha256.sh <file> <expected-sha256>
#
# Exits non-zero when the hash does not match or when no sha256 tool is
# available. A download that cannot be checked is not used: a build asset
# from an unverified source is just an arbitrary file from the internet.
set -eu

file=$1
expected=$2

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$file" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$file" | cut -d' ' -f1)
else
	echo "no sha256sum/shasum found - cannot verify $file" >&2
	exit 1
fi

if [ "$actual" != "$expected" ]; then
	echo "checksum mismatch for $file" >&2
	echo "  expected $expected" >&2
	echo "  actual   $actual" >&2
	exit 1
fi

echo "    verified $file ($actual)"
