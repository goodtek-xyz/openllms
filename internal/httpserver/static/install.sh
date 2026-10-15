#!/usr/bin/env bash
# Install llms CLI from the public gateway dist (no GitHub auth required).
#   curl -fsSL https://llms.goodtek.xyz/install.sh | bash
# Docs: https://llms.goodtek.xyz/install.md
set -euo pipefail

DIST_BASE="${LLMS_DIST_BASE:-https://llms.goodtek.xyz}"
DIST_BASE="${DIST_BASE%/}"
REPO="${LLMS_INSTALL_REPO:-goodtek-xyz/llms}"
VERSION="${LLMS_VERSION:-latest}"
BIN_DIR="${LLMS_BIN_DIR:-/usr/local/bin}"
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

download_dist() {
  local url="${DIST_BASE}/dist/${asset}"
  echo "Downloading ${url}" >&2
  if ! curl -fsSL "$url" -o "$tmp/llms"; then
    echo "Gateway dist download failed: $url" >&2
    return 1
  fi
  # Reject tiny HTML/error bodies
  if [[ ! -s "$tmp/llms" ]] || [[ "$(wc -c <"$tmp/llms")" -lt 100000 ]]; then
    echo "Unexpected dist payload (too small); falling back if possible" >&2
    return 1
  fi
}

download_github() {
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    echo "Downloading ${asset} via gh (${VERSION})" >&2
    if [[ "$VERSION" == "latest" ]]; then
      gh release download -R "$REPO" -p "$asset" -D "$tmp"
    else
      gh release download -R "$REPO" "$VERSION" -p "$asset" -D "$tmp"
    fi
    mv "$tmp/$asset" "$tmp/llms"
    return
  fi
  local token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  local url api
  if [[ "$VERSION" == "latest" ]]; then
    api="https://api.github.com/repos/${REPO}/releases/latest"
  else
    api="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
  fi
  if [[ -n "$token" ]]; then
    echo "Downloading ${asset} via GitHub API (${VERSION})" >&2
    url=$(curl -fsSL -H "Authorization: Bearer ${token}" -H "Accept: application/vnd.github+json" "$api" \
      | ASSET="$asset" python3 -c 'import os,sys,json; d=json.load(sys.stdin); name=os.environ["ASSET"]; print(next(a["url"] for a in d.get("assets",[]) if a["name"]==name))')
    curl -fsSL -H "Authorization: Bearer ${token}" -H "Accept: application/octet-stream" "$url" -o "$tmp/llms"
  else
    if [[ "$VERSION" == "latest" ]]; then
      url="https://github.com/${REPO}/releases/latest/download/${asset}"
    else
      url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
    fi
    echo "Downloading $url" >&2
    if ! curl -fsSL "$url" -o "$tmp/llms"; then
      echo "Download failed. Try: curl -fsSL ${DIST_BASE}/install.sh | bash" >&2
      echo "Or set GH_TOKEN / run: gh auth login" >&2
      exit 1
    fi
  fi
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
if command -v "$BIN_DIR/llms" >/dev/null 2>&1 || [[ -x "$BIN_DIR/llms" ]]; then
  "$BIN_DIR/llms" version 2>/dev/null || true
fi
echo "Default API base: ${DIST_BASE} (override with LLMS_API_BASE)"
echo "Next: llms login"
