#!/bin/zsh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Assembles "Export to MusicXML.app" at the given path. ARCHS picks what goes
# in the binary, this machine's architecture by default. VERSION overrides the
# version in Info.plist.
#
# usage: ./build-app.sh "/path/to/Export to MusicXML.app"
set -eu

app=${1:?usage: build-app.sh <path to .app>}
source=$(cd "$(dirname "$0")" && pwd)/cmd/logicx-to-musicxml
archs=${ARCHS:-$(uname -m)}

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

# cgo builds one architecture at a time; lipo joins them for a universal app.
slices=()
for arch in ${=archs}; do
	case $arch in
		arm64) goarch=arm64 clangarch=arm64 ;;
		x86_64) goarch=amd64 clangarch=x86_64 ;;
		*) echo "unknown architecture $arch" >&2; exit 1 ;;
	esac
	slice="$app/Contents/MacOS/logicx-to-musicxml.$arch"
	CGO_ENABLED=1 GOARCH=$goarch CC="clang -arch $clangarch" \
		go build -trimpath -o "$slice" ./cmd/logicx-to-musicxml
	slices+=("$slice")
done
lipo -create -output "$app/Contents/MacOS/logicx-to-musicxml" "${slices[@]}"
rm -f "${slices[@]}"

cp "$source/Info.plist" "$app/Contents/Info.plist"
cp "$source/icon.icns" "$app/Contents/Resources/icon.icns"
printf 'APPL????' > "$app/Contents/PkgInfo"
if [[ -n ${VERSION:-} ]]; then
	plutil -replace CFBundleShortVersionString -string "$VERSION" "$app/Contents/Info.plist"
	plutil -replace CFBundleVersion -string "$VERSION" "$app/Contents/Info.plist"
fi
