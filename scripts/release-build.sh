#!/bin/sh
# Builds the release binaries for VERSION (e.g. 0.0.2) into OUT (default
# dist/release): PROGRAM_VERSION_OS_ARCH[.exe] for each platform, and
# checksums.txt. "myweb upgrade" downloads these; see selfupdate/.
# The tray app (myweb-desktop) is built here for Linux and Windows; on macOS it
# needs cgo, so scripts/desktop-mac.sh builds it on a Mac, and files in
# $RELEASE_EXTRA (a folder) are added to the release before the checksums.
set -eu
version=${1:?usage: scripts/release-build.sh VERSION [OUT]}
version=${version#v}
out=${2:-dist/release}
rm -rf "$out"
mkdir -p "$out"
ldflags="-s -w -X github.com/nimezhu/myweb/myserver.VERSION=$version"
for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os=${platform%/*}
	arch=${platform#*/}
	ext=
	[ "$os" = windows ] && ext=.exe
	pkgs=". ./cmd/myweb-publish ./cmd/myweb-team"
	[ "$os" != darwin ] && pkgs="$pkgs ./cmd/myweb-desktop"
	for pkg in $pkgs; do
		prog=myweb
		[ "$pkg" != . ] && prog=${pkg##*/}
		flags=$ldflags
		# the tray app on Windows: no console window
		[ "$prog" = myweb-desktop ] && [ "$os" = windows ] && flags="$flags -H windowsgui"
		echo "$prog $os/$arch"
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$flags" \
			-o "$out/${prog}_${version}_${os}_${arch}${ext}" "$pkg"
	done
done
if [ -n "${RELEASE_EXTRA:-}" ]; then
	cp -- "$RELEASE_EXTRA"/* "$out"/
fi
(cd "$out" && sha256sum -- *_"$version"_* > checksums.txt)
