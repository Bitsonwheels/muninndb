#!/bin/sh
# Check scripts/verify-sha256.sh: a matching hash passes, a wrong hash and a
# missing tool fail. No network, no build.
set -eu

cd "$(dirname "$0")/.."

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf 'hello' >"$tmp/file"
expected=$(sha256sum "$tmp/file" | cut -d' ' -f1)

# 1. the matching hash is accepted
sh scripts/verify-sha256.sh "$tmp/file" "$expected" >/dev/null
echo "ok: a matching checksum is accepted"

# 2. a wrong hash is refused
if sh scripts/verify-sha256.sh "$tmp/file" 0000000000000000000000000000000000000000000000000000000000000000 >/dev/null 2>&1; then
	echo "FAIL: a wrong checksum was accepted" >&2
	exit 1
fi
echo "ok: a wrong checksum is refused"

# 3. without a sha256 tool nothing is accepted
mkdir -p "$tmp/empty-bin"
if PATH="$tmp/empty-bin" sh scripts/verify-sha256.sh "$tmp/file" "$expected" >/dev/null 2>&1; then
	echo "FAIL: a file was accepted without a checksum tool" >&2
	exit 1
fi
echo "ok: a missing checksum tool is refused"

echo "all checksum checks passed"
