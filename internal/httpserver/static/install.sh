#!/usr/bin/env bash
# Install llms CLI from the public gateway dist (no GitHub auth required).
#   curl -fsSL https://llms.goodtek.xyz/install.sh | bash
# Docs: https://llms.goodtek.xyz/install.md
set -euo pipefail

DIST_BASE="${LLMS_DIST_BASE:-https://llms.goodtek.xyz}"
DIST_BASE="${DIST_BASE%/}"
REPO="${LLMS_INSTALL_REPO:-goodtek-xyz/openllms}"
VERSION="${LLMS_VERSION:-latest}"
# Install where the llms on PATH already lives, so `llms` runs the new build; else /usr/local/bin.
if [[ -n "${LLMS_BIN_DIR:-}" ]]; then
  BIN_DIR="$LLMS_BIN_DIR"
elif existing="$(command -v llms 2>/dev/null)" && [[ -f "$existing" ]]; then
  BIN_DIR="$(cd "$(dirname "$existing")" && pwd)"
else
  BIN_DIR=/usr/local/bin
fi
# Prefer gateway /dist unless LLMS_INSTALL_SOURCE=github
SOURCE="${LLMS_INSTALL_SOURCE:-dist}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "unsupported os: $os" >&2; exit 1 ;;
esac

asset="llms_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# fetch <binary url> <SHA256SUMS url>: download, then install only if SHA256SUMS lists this asset with the same hash.
fetch() {
  echo "Downloading $1" >&2
  curl -fsSL "$1" -o "$tmp/llms" || { echo "Download failed: $1" >&2; return 1; }
  curl -fsSL "$2" -o "$tmp/SHA256SUMS" || { echo "No checksums at $2 — not installing" >&2; return 1; }
  local want got
  want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/SHA256SUMS")"
  got="$(sha256_of "$tmp/llms")"
  if [[ -z "$want" || "$want" != "$got" ]]; then
    echo "Checksum mismatch for $asset (SHA256SUMS: ${want:-none}, file: $got) — not installing" >&2
    return 1
  fi
}

download_dist() {
  fetch "${DIST_BASE}/dist/${asset}" "${DIST_BASE}/dist/SHA256SUMS"
}

download_github() {
  local rel="https://github.com/${REPO}/releases"
  if [[ "$VERSION" == "latest" ]]; then rel="$rel/latest/download"; else rel="$rel/download/$VERSION"; fi
  fetch "$rel/$asset" "$rel/SHA256SUMS" || exit 1
}

if [[ "$SOURCE" == "github" ]]; then
  download_github
elif ! download_dist; then
  echo "Falling back to GitHub Releases…" >&2
  download_github
fi

chmod +x "$tmp/llms"
if [[ -w "$BIN_DIR" ]] || [[ "$BIN_DIR" == "$HOME"* && -w "$(dirname "$BIN_DIR")" ]]; then
  mkdir -p "$BIN_DIR"
  install -m 0755 "$tmp/llms" "$BIN_DIR/llms"
else
  sudo install -m 0755 "$tmp/llms" "$BIN_DIR/llms"
fi
echo "Installed $BIN_DIR/llms"
"$BIN_DIR/llms" version 2>/dev/null || true
hash -r 2>/dev/null || true
onpath="$(command -v llms 2>/dev/null || true)"
if [[ -n "$onpath" && "$(cd "$(dirname "$onpath")" && pwd)/llms" != "$BIN_DIR/llms" ]]; then
  echo "Note: \`llms\` on your PATH is $onpath, not $BIN_DIR/llms — remove the other one or put $BIN_DIR earlier in PATH." >&2
elif [[ -z "$onpath" ]]; then
  echo "Note: $BIN_DIR is not on your PATH." >&2
fi
echo "Default API base: ${DIST_BASE} (override with LLMS_API_BASE)"
echo "Next: llms login"
