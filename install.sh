#!/bin/sh
# Install a checksum-verified release from RedwindA/ccusage_go.
set -eu

fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }

case "${1:-}" in
  -h|--help)
    printf '%s\n' 'Usage: sh install.sh [vVERSION]' \
      'Defaults to the latest stable release.' \
      'Set INSTALL_DIR to override ~/.local/bin; VERSION may also select a release.'
    exit 0 ;;
esac
[ "$#" -le 1 ] || fail 'Expected at most one version argument.'
version=${1:-${VERSION:-latest}}
case "$version" in
  latest) ;;
  v[0-9]*)
    case "$version" in *[!a-zA-Z0-9._-]*) fail 'Invalid version tag.' ;; esac ;;
  *) fail 'Use a version tag such as v0.15.0, or latest.' ;;
esac

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail 'Supported systems: Linux and macOS. Download Windows ZIPs from GitHub Releases.' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail 'Supported architectures: x86_64 and arm64.' ;;
esac

for cmd in curl tar mktemp awk chmod mv mkdir; do
  command -v "$cmd" >/dev/null 2>&1 || fail "Required command not found: $cmd"
done
if command -v sha256sum >/dev/null 2>&1; then
  checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  checksum_tool=shasum
else
  fail 'Install sha256sum or shasum to verify the download.'
fi

install_dir=${INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$install_dir"
[ -w "$install_dir" ] || fail "Directory is not writable: $install_dir (set INSTALL_DIR)."
tmp=$(mktemp -d)
staged=
cleanup() {
  rm -rf "$tmp"
  if [ -n "$staged" ]; then rm -f "$staged"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

repo_url=https://github.com/RedwindA/ccusage_go
if [ "$version" = latest ]; then
  # Resolve once so the archive and checksum always come from the same release.
  resolved=$(curl --proto '=https' --tlsv1.2 -fsSL --retry 3 \
    -o /dev/null -w '%{url_effective}' "$repo_url/releases/latest")
  case "$resolved" in
    "$repo_url"/releases/tag/v*) version=${resolved##*/} ;;
    *) fail 'Could not resolve the latest release.' ;;
  esac
  case "$version" in *[!a-zA-Z0-9._-]*) fail 'Invalid release tag.' ;; esac
fi
binary=ccusage_go-$os-$arch
archive=$binary.tar.gz
base_url=$repo_url/releases/download/$version
printf 'Downloading ccusage_go %s (%s/%s)...\n' "$version" "$os" "$arch"
curl --proto '=https' --tlsv1.2 -fsSL --retry 3 "$base_url/$archive" -o "$tmp/$archive"
curl --proto '=https' --tlsv1.2 -fsSL --retry 3 "$base_url/checksums.txt" -o "$tmp/checksums.txt"
expected=$(awk -v name="$archive" '$2 == name {print $1}' "$tmp/checksums.txt")
[ "${#expected}" -eq 64 ] || fail "Missing or invalid checksum for $archive."
case "$expected" in *[!0-9a-fA-F]*) fail 'Invalid SHA-256 checksum.' ;; esac
if [ "$checksum_tool" = sha256sum ]; then
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || fail 'Checksum mismatch; installation aborted.'

# Extract only the expected binary; replace an existing install after verification.
staged=$(mktemp "$install_dir/.ccusage_go.XXXXXX")
tar -xzOf "$tmp/$archive" "$binary" > "$staged"
[ -s "$staged" ] || fail 'Archive contained an empty binary.'
chmod 755 "$staged"
[ ! -d "$install_dir/ccusage_go" ] || fail 'Install destination is a directory.'
mv -f "$staged" "$install_dir/ccusage_go"
staged=
printf 'Installed %s/ccusage_go (%s)\n' "$install_dir" "$version"
case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *) printf 'Add this directory to your PATH: %s\n' "$install_dir" ;;
esac
