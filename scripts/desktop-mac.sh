#!/bin/sh
# Builds myweb.app (the tray app, for Apple silicon and Intel Macs) for
# VERSION into OUT (default dist/desktop) as myweb-desktop_VERSION_darwin_universal.zip.
# Runs on a Mac: the menu bar needs cgo. Signed ad hoc only: on first open,
# macOS asks to confirm it (right-click > Open, or System Settings > Privacy
# & Security).
set -eu
version=${1:?usage: scripts/desktop-mac.sh VERSION [OUT]}
version=${version#v}
out=${2:-dist/desktop}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"
ldflags="-s -w -X github.com/nimezhu/myweb/myserver.VERSION=$version"
for arch in arm64 amd64; do
	CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
		-o "$work/myweb-desktop-$arch" ./cmd/myweb-desktop
done

app=$work/myweb.app
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/myweb-desktop" "$work/myweb-desktop-arm64" "$work/myweb-desktop-amd64"

# the icon, from the file manager's
set_=$work/myweb.iconset
mkdir -p "$set_"
for n in 16 32 128 256 512; do
	sips -z $n $n manager/web/icon-512.png --out "$set_/icon_${n}x${n}.png" >/dev/null
	[ $n -le 256 ] && sips -z $((n * 2)) $((n * 2)) manager/web/icon-512.png --out "$set_/icon_${n}x${n}@2x.png" >/dev/null
done
iconutil -c icns "$set_" -o "$app/Contents/Resources/myweb.icns"

# LSUIElement: a menu bar app, without a Dock icon
cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>myweb</string>
	<key>CFBundleDisplayName</key><string>myweb</string>
	<key>CFBundleIdentifier</key><string>com.github.nimezhu.myweb-desktop</string>
	<key>CFBundleExecutable</key><string>myweb-desktop</string>
	<key>CFBundleIconFile</key><string>myweb</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF
codesign --force --deep --sign - "$app"
ditto -c -k --keepParent "$app" "$out/myweb-desktop_${version}_darwin_universal.zip"
echo "$out/myweb-desktop_${version}_darwin_universal.zip"
