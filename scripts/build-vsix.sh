#!/usr/bin/env bash
# Builds platform-specific .vsix packages for the k6-debug VS Code extension.
# Each vsix bundles the k6-debug binary for that platform.
#
# Usage:
#   ./scripts/build-vsix.sh              # build all platforms
#   ./scripts/build-vsix.sh darwin-arm64 # build one platform

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXT_DIR="$REPO_ROOT/vscode/k6-debug-extension"
BIN_DIR="$EXT_DIR/bin"
DIST_DIR="$REPO_ROOT/dist"

# platform entries: "<vscode-target>:<GOOS>:<GOARCH>"
ALL_PLATFORMS=(
  "darwin-arm64:darwin:arm64"
  "darwin-x64:darwin:amd64"
  "linux-x64:linux:amd64"
  "linux-arm64:linux:arm64"
  "win32-x64:windows:amd64"
  "win32-arm64:windows:arm64"
)

# Filter to requested platform(s) if argument given
if [[ $# -gt 0 ]]; then
  PLATFORMS=()
  for entry in "${ALL_PLATFORMS[@]}"; do
    target="${entry%%:*}"
    for arg in "$@"; do
      [[ "$target" == "$arg" ]] && PLATFORMS+=("$entry")
    done
  done
  if [[ ${#PLATFORMS[@]} -eq 0 ]]; then
    echo "Unknown platform(s): $*"
    echo "Available: darwin-arm64 darwin-x64 linux-x64 linux-arm64 win32-x64 win32-arm64"
    exit 1
  fi
else
  PLATFORMS=("${ALL_PLATFORMS[@]}")
fi

mkdir -p "$BIN_DIR" "$DIST_DIR"

# Install extension npm deps once
echo "==> Installing extension npm dependencies..."
(cd "$EXT_DIR" && npm ci --silent)

for entry in "${PLATFORMS[@]}"; do
  VSCODE_TARGET="${entry%%:*}"
  rest="${entry#*:}"
  GOOS="${rest%%:*}"
  GOARCH="${rest#*:}"

  BIN_NAME="k6-debug"
  [[ "$GOOS" == "windows" ]] && BIN_NAME="k6-debug.exe"

  echo ""
  # Clean bin/ so no stale binary from a previous platform leaks into this vsix
  rm -f "$BIN_DIR/k6-debug" "$BIN_DIR/k6-debug.exe"

  echo "==> [$VSCODE_TARGET] Building Go binary (GOOS=$GOOS GOARCH=$GOARCH)..."
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -o "$BIN_DIR/$BIN_NAME" -ldflags "-s -w" "$REPO_ROOT/cmd/k6debug/"

  # vsce looks for LICENSE in the extension directory
  cp "$REPO_ROOT/LICENSE" "$EXT_DIR/LICENSE"

  echo "==> [$VSCODE_TARGET] Packaging vsix..."
  (cd "$EXT_DIR" && npx vsce package \
    --target "$VSCODE_TARGET" \
    --no-git-tag-version \
    --no-update-package-json \
    --out "$DIST_DIR/")

  echo "==> [$VSCODE_TARGET] Done."
done

# Clean up bin dir (don't leave a stale binary from last platform)
rm -f "$BIN_DIR/k6-debug" "$BIN_DIR/k6-debug.exe"

echo ""
echo "==> Built vsix files:"
ls -lh "$DIST_DIR"/*.vsix
