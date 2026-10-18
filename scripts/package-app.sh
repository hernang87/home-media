#!/usr/bin/env bash
# Builds bin/HomeMedia.app from scratch: binary, Info.plist, icon and ad-hoc signature.
#
#   bash scripts/package-app.sh            local build; remembers this repo as mediad's working dir
#   bash scripts/package-app.sh --release  portable build; mediad keeps .env and data/ in
#                                          ~/Library/Application Support/HomeMedia
set -euo pipefail

RELEASE=0
[ "${1:-}" = "--release" ] && RELEASE=1

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP="$ROOT/bin/HomeMedia.app"
CONTENTS="$APP/Contents"
ICON_SRC="$ROOT/cmd/homemedia-app/icon.png"

rm -rf "$APP"
mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Resources"

echo "Building homemedia-app..."
# The extldflags hide a harmless "duplicate -lobjc" warning from Fyne's cgo flags.
go build -ldflags="-extldflags=-Wl,-no_warn_duplicate_libraries" -o "$CONTENTS/MacOS/homemedia-app" ./cmd/homemedia-app
echo "Building mediad (the app starts it and stops it on quit)..."
go build -o "$CONTENTS/MacOS/mediad" ./cmd/mediad
# Where mediad reads .env and keeps data/ (see internal/daemon).
if [ "$RELEASE" = 0 ]; then
	printf '%s\n' "$ROOT" > "$CONTENTS/Resources/workdir"
fi

cat > "$CONTENTS/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Home Media</string>
	<key>CFBundleDisplayName</key>
	<string>Home Media</string>
	<key>CFBundleIdentifier</key>
	<string>com.homemedia.app</string>
	<key>CFBundleExecutable</key>
	<string>homemedia-app</string>
	<key>CFBundleIconFile</key>
	<string>icon</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>0.1.0</string>
	<key>CFBundleVersion</key>
	<string>0.1.0</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSLocalNetworkUsageDescription</key>
	<string>Home Media looks for your media server and TVs on the local network.</string>
	<key>NSBonjourServices</key>
	<array>
		<string>_homemedia._tcp</string>
	</array>
</dict>
</plist>
PLIST

echo "Building icon.icns..."
ICONSET_DIR="$(mktemp -d)"
trap 'rm -rf "$ICONSET_DIR"' EXIT
ICONSET="$ICONSET_DIR/icon.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
	d=$((s * 2))
	sips -z "$s" "$s" "$ICON_SRC" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
	sips -z "$d" "$d" "$ICON_SRC" --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$CONTENTS/Resources/icon.icns"

echo "Signing (ad-hoc)..."
codesign --force --deep -s - "$APP"

echo "App bundle: $APP"
