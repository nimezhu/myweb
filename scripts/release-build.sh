#!/bin/sh
# Builds the release binaries for VERSION (e.g. 0.0.2) into OUT (default
# dist/release): PROGRAM_VERSION_OS_ARCH[.exe] for each platform, and
# checksums.txt. "myweb upgrade" downloads these; see selfupdate/.
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
	for pkg in . ./cmd/myweb-publish ./cmd/myweb-team; do
		prog=myweb
		[ "$pkg" != . ] && prog=${pkg##*/}
		echo "$prog $os/$arch"
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
			-o "$out/${prog}_${version}_${os}_${arch}${ext}" "$pkg"
	done
done
(cd "$out" && sha256sum -- *_"$version"_* > checksums.txt)
