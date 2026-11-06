#!/bin/sh
# Checks that install.sh refuses to install a binary it cannot verify.
#
# The installer is driven against a fake curl, so no network and no real
# release are needed:
#   1. no published checksum   -> the install must abort
#   2. a wrong checksum        -> the install must abort
#   3. no sha256 tool          -> the install must abort
#   4. a matching checksum     -> the install must succeed
#
# Usage: sh scripts/test-install.sh
set -e

ROOT=$(cd "$(dirname "$0")/.." && pwd)
INSTALLER="${ROOT}/install.sh"
WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT

FAILS=0

fail() {
  echo "FAIL: $1"
  FAILS=$((FAILS + 1))
}

# ── a curl stand-in that serves the release fixtures ─────────────────────────
FAKE_BIN="${WORK}/bin"
mkdir -p "${FAKE_BIN}"
cat > "${FAKE_BIN}/curl" <<'CURL'
#!/bin/sh
# Minimal curl stand-in for the installer test.
url=""
out=""
code=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) code="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "${url}" in
  *api.github.com*)
    printf '{"tag_name":"v9.9.9"}\n'
    ;;
  *checksums.txt*)
    if [ -z "${FAKE_CHECKSUMS}" ]; then
      exit 22
    fi
    cat "${FAKE_CHECKSUMS}"
    ;;
  *)
    printf 'fake muninn binary\n' > "${out}"
    if [ -n "${code}" ]; then
      printf '200'
    fi
    ;;
esac
CURL
chmod +x "${FAKE_BIN}/curl"

# The binary the fake curl serves; its hash is what the checksum file must hold.
PAYLOAD="fake muninn binary
"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "${ARCH}" in
  x86_64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
esac
PLATFORM="${OS}-${ARCH}"

if command -v sha256sum >/dev/null 2>&1; then
  HASH=$(printf '%s' "${PAYLOAD}" | sha256sum | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  HASH=$(printf '%s' "${PAYLOAD}" | shasum -a 256 | awk '{print $1}')
else
  echo "FAIL: neither sha256sum nor shasum is available on this machine" >&2
  exit 1
fi

SH=$(command -v sh)

# ── run the installer with the fake curl ─────────────────────────────────────
# usage: run_install <checksums-file> <path> [extra PATH entries...]
run_install() {
  sums="$1"
  path="$2"
  shift 2
  mkdir -p "${WORK}/install" "${WORK}/home"
  FAKE_CHECKSUMS="${sums}" \
  MUNINN_INSTALL_DIR="${WORK}/install" \
  HOME="${WORK}/home" \
  PATH="${path}" \
    "${SH}" "${INSTALLER}" > "${WORK}/out" 2>&1
}

# A PATH with the fake curl, the shell tools the installer needs, and no
# sha256 tool.
NOHASH_BIN="${WORK}/nohash-bin"
mkdir -p "${NOHASH_BIN}"
for tool in uname tr sed head grep awk cat mktemp chmod mv mkdir rm; do
  real=$(command -v "${tool}" || true)
  if [ -z "${real}" ]; then
    echo "FAIL: the test needs ${tool} on the PATH" >&2
    exit 1
  fi
  ln -s "${real}" "${NOHASH_BIN}/${tool}"
done
cp "${FAKE_BIN}/curl" "${NOHASH_BIN}/curl"

HASH_BIN="${WORK}/hash-bin"
mkdir -p "${HASH_BIN}"
for tool in uname tr sed head grep awk cat mktemp chmod mv mkdir rm; do
  ln -s "$(command -v "${tool}")" "${HASH_BIN}/${tool}"
done
cp "${FAKE_BIN}/curl" "${HASH_BIN}/curl"
if command -v sha256sum >/dev/null 2>&1; then
  ln -s "$(command -v sha256sum)" "${HASH_BIN}/sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  ln -s "$(command -v shasum)" "${HASH_BIN}/shasum"
fi

# 1. no published checksum -> abort
if run_install "" "${HASH_BIN}"; then
  fail "the installer accepted a release without a checksum"
elif grep -q "refusing to install" "${WORK}/out"; then
  echo "ok: a release without a checksum is refused"
else
  fail "the installer failed for the wrong reason: $(cat "${WORK}/out")"
fi

# 2. a wrong checksum -> abort
WRONG="${WORK}/wrong.txt"
printf '%s  muninn-%s\n' "0000000000000000000000000000000000000000000000000000000000000000" "${PLATFORM}" > "${WRONG}"
if run_install "${WRONG}" "${HASH_BIN}"; then
  fail "the installer accepted a binary with the wrong checksum"
elif grep -q "CHECKSUM VERIFICATION FAILED" "${WORK}/out"; then
  echo "ok: a wrong checksum is refused"
else
  fail "the installer failed for the wrong reason: $(cat "${WORK}/out")"
fi

# 3. no sha256 tool -> abort
if run_install "${WRONG}" "${NOHASH_BIN}"; then
  fail "the installer accepted a download it could not verify"
elif grep -q "sha256sum/shasum" "${WORK}/out"; then
  echo "ok: a missing sha256 tool is refused"
else
  fail "the installer failed for the wrong reason: $(cat "${WORK}/out")"
fi

# 4. a matching checksum -> install
GOOD="${WORK}/good.txt"
printf '%s  muninn-%s\n' "${HASH}" "${PLATFORM}" > "${GOOD}"
if run_install "${GOOD}" "${HASH_BIN}"; then
  if [ -x "${WORK}/install/muninn" ]; then
    echo "ok: a verified release is installed"
  else
    fail "the installer did not put the binary in place"
  fi
else
  fail "the installer refused a verified release: $(cat "${WORK}/out")"
fi

if [ "${FAILS}" -gt 0 ]; then
  echo "${FAILS} check(s) failed"
  exit 1
fi
echo "all install checks passed"
