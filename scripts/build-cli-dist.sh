#!/usr/bin/env bash
# Cross-build llms CLI into a dist directory for gateway /dist serving.
# Usage: scripts/build-cli-dist.sh <out-dir> [git-sha]
set -euo pipefail

OUT="${1:-}"
SHA="${2:-${GITHUB_SHA:-}}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ -z "$OUT" ]]; then
  echo "usage: $0 <out-dir> [git-sha]" >&2
  exit 2
fi

cd "$REPO_ROOT"

if [[ -z "$SHA" ]]; then
  SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
fi
SHORT="$(printf '%s' "$SHA" | cut -c1-12)"
VERSION="${LLMS_CLI_VERSION:-}"
if [[ -z "$VERSION" ]]; then
  VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "dev-$SHORT")"
fi

mkdir -p "$OUT"
export CGO_ENABLED=0

targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
)

build_one() {
  local goos="$1" goarch="$2"
  local name="llms_${goos}_${goarch}"
  echo "  building $name" >&2
  GOOS="$goos" GOARCH="$goarch" go build \
    -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${SHORT}" \
    -o "$OUT/$name" \
    ./cmd/llms
}

if command -v go >/dev/null 2>&1; then
  echo "==> building CLI dist with host go ($(go version))"
  for t in "${targets[@]}"; do
    # shellcheck disable=SC2086
    build_one $t
  done
else
  ctr="$(command -v docker || command -v podman || true)"
  if [[ -z "$ctr" ]]; then
    echo "host go missing and no docker/podman to build in a golang container" >&2
    exit 1
  fi
  echo "==> host go missing; building CLI dist via golang container ($(basename "$ctr"))"
  "$ctr" run --rm \
    -v "$REPO_ROOT:/src:Z" \
    -v "$OUT:/out:Z" \
    -w /src \
    -e CGO_ENABLED=0 \
    docker.io/library/golang:1.24 \
    bash -c '
      set -euo pipefail
      VERSION="'"$VERSION"'"
      SHORT="'"$SHORT"'"
      for pair in "linux amd64" "linux arm64" "darwin amd64" "darwin arm64"; do
        set -- $pair
        goos=$1; goarch=$2
        name="llms_${goos}_${goarch}"
        echo "  building $name"
        GOOS=$goos GOARCH=$goarch go build -trimpath \
          -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${SHORT}" \
          -o "/out/$name" ./cmd/llms
      done
    '
fi

# Checksums in `sha256sum` format; install.sh and `llms update` install a binary only when it matches.
(
  cd "$OUT"
  if command -v sha256sum >/dev/null 2>&1; then sha256sum llms_*_*; else shasum -a 256 llms_*_*; fi
) >"$OUT/SHA256SUMS.tmp"
mv "$OUT/SHA256SUMS.tmp" "$OUT/SHA256SUMS"
printf '%s\n' "$VERSION" >"$OUT/VERSION"
printf '%s\n' "$SHA" >"$OUT/COMMIT"
# Keep install scripts alongside binaries for operators copying the tree.
cp -f "$REPO_ROOT/install.sh" "$OUT/install.sh" 2>/dev/null || true

echo "==> dist ready in $OUT"
ls -la "$OUT"
