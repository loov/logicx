#!/bin/zsh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Builds a universal, signed, notarized disk image to hand to other people.
#
#   VERSION=1.0 ./release.sh
#
# It needs a Developer ID Application certificate in the keychain (from the
# paid Apple Developer Program; Xcode > Settings > Accounts > Manage
# Certificates > + > Developer ID Application), and notarization credentials
# stored once with:
#
#   xcrun notarytool store-credentials logicx-notary \
#       --apple-id you@example.com --team-id TEAMID --password <app-specific>
#
# On a build machine, where no one can type a keychain password, set
# NOTARY_KEY to an App Store Connect API key file instead, with NOTARY_KEY_ID
# and NOTARY_ISSUER_ID beside it.
#
# SIGN_IDENTITY, NOTARY_PROFILE, VERSION and OUT_DIR override the defaults.
# Without a notary profile it still signs and builds the image, which is fine
# for a local check but not for anyone else's Mac.
set -eu

cd "$(dirname "$0")"

version=${VERSION:-1.0}
identity=${SIGN_IDENTITY:-Developer ID Application}
profile=${NOTARY_PROFILE:-logicx-notary}
out=${OUT_DIR:-build}
app="$out/Export to MusicXML.app"
dmg="$out/ExportToMusicXML-$version.dmg"

if [[ $identity != "-" ]] && ! security find-identity -v -p codesigning | grep -q "$identity"; then
	echo "no \"$identity\" certificate in the keychain; see the comment at the top of $0" >&2
	exit 1
fi

mkdir -p "$out"
VERSION=$version ARCHS="arm64 x86_64" ./build-app.sh "$app"

# The hardened runtime is what notarization requires; the timestamp is what
# keeps the signature valid after the certificate expires.
codesign --force --timestamp --options runtime --sign "$identity" "$app"
codesign --verify --strict --verbose=2 "$app"

staging=$(mktemp -d)
cp -R "$app" "$staging/"
ln -s /Applications "$staging/Applications"
rm -f "$dmg"
hdiutil create -volname "Export to MusicXML" -srcfolder "$staging" -ov -format UDZO "$dmg" >/dev/null
rm -rf "$staging"
codesign --force --timestamp --sign "$identity" "$dmg"

if [[ $identity == "-" ]]; then
	echo "$dmg (ad-hoc signed, for local checks only)"
	exit 0
fi
# A stored profile is the convenient way by hand; an App Store Connect API key
# is the one a build machine can hold, so NOTARY_KEY wins where it is set.
credentials=(--keychain-profile "$profile")
if [[ -n ${NOTARY_KEY:-} ]]; then
	credentials=(--key "$NOTARY_KEY" --key-id "${NOTARY_KEY_ID:?NOTARY_KEY needs NOTARY_KEY_ID}"
		--issuer "${NOTARY_ISSUER_ID:?NOTARY_KEY needs NOTARY_ISSUER_ID}")
elif ! xcrun notarytool history --keychain-profile "$profile" >/dev/null 2>&1; then
	echo "$dmg (signed but NOT notarized: no \"$profile\" credentials)" >&2
	echo "Gatekeeper will refuse it on another Mac; see the comment at the top of $0" >&2
	exit 1
fi

# Notarizing the image covers the app inside it, so one staple is enough.
xcrun notarytool submit "$dmg" "${credentials[@]}" --wait
xcrun stapler staple "$dmg"
spctl --assess --type open --context context:primary-signature -v "$dmg"

echo "$dmg"
