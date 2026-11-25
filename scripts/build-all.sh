#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
GO_BIN="${GO_BIN:-$(command -v go || echo /opt/homebrew/bin/go)}"

mkdir -p "$DIST"

echo "==> Running Go tests..."
(cd "$ROOT" && "$GO_BIN" test -v ./...)

build_target() {
  local goos="$1"
  local goarch="$2"
  local out="$3"
  echo "==> Building $goos/$goarch -> $out"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$DIST/$out" ./cmd/csb)
}

build_target darwin  arm64 csb-darwin-arm64
build_target darwin  amd64 csb-darwin-amd64
build_target windows amd64 csb-windows-amd64.exe
build_target windows arm64 csb-windows-arm64.exe
build_target linux   amd64 csb-linux-amd64

# Also create convenient local runnable binary for current macOS arch
cp "$DIST/csb-darwin-$(uname -m | sed 's/x86_64/amd64/')" "$DIST/csb"
chmod +x "$DIST/csb"

# Package native macOS .app bundle
APP="$DIST/Claude Session Bridge.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$DIST/csb" "$APP/Contents/MacOS/csb"

cat > "$APP/Contents/MacOS/launcher" <<'LAUNCHER'
#!/usr/bin/env bash
DIR="$(cd "$(dirname "$0")" && pwd)"
exec "$DIR/csb" serve
LAUNCHER
chmod +x "$APP/Contents/MacOS/launcher" "$APP/Contents/MacOS/csb"

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>Claude Session Bridge</string>
  <key>CFBundleDisplayName</key>
  <string>Claude Session Bridge</string>
  <key>CFBundleIdentifier</key>
  <string>dev.csb.claude-session-bridge</string>
  <key>CFBundleVersion</key>
  <string>1.0.0</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0.0</string>
  <key>CFBundleExecutable</key>
  <string>launcher</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>LSMinimumSystemVersion</key>
  <string>12.0</string>
</dict>
</plist>
PLIST

echo ""
echo "✅ All builds succeeded! Output files in $DIST:"
ls -lh "$DIST"
