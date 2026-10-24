#!/bin/sh
# Prints VERSION's section of CHANGELOG.md (for v0.0.2-rc.1, that of 0.0.2),
# without its heading; nothing when there is none.
set -eu
version=${1:?usage: scripts/release-notes.sh VERSION}
version=${version#v}
version=${version%%-*}
awk -v v="$version" '
	/^## / { on = ($2 == v); next }
	on
' "$(dirname "$0")/../CHANGELOG.md" | sed -e '/./,$!d'
