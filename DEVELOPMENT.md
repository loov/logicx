# Development

## Build and test

```sh
go test ./...
GOOS=linux CGO_ENABLED=0 go build ./...
```

The library stays pure Go and portable, which the second line checks. The app
does not: its dialog is AppKit through cgo, in `dialog.m` and `ui_darwin.go`,
behind a `darwin` build tag with a stub beside it. Both run on every push in
`.github/workflows/test.yml`.

`./install.sh` builds the app for this machine, unsigned, into `~/Applications`
and links the command into `$HOME/bin`. `build-app.sh` assembles the bundle
and is what `install.sh` and `release.sh` share; `ARCHS` picks the
architectures and `VERSION` the version in `Info.plist`.

## Release a disk image

```sh
VERSION=1.0 ./release.sh
```

builds a universal app, signs it with the hardened runtime, packs it into a
drag-to-Applications image, notarizes it and staples the ticket, leaving
`build/MusicXMLBridge-1.0.dmg`. It needs a **Developer ID Application**
certificate from the paid Apple Developer Program (Xcode > Settings > Accounts
> Manage Certificates > + > Developer ID Application), and notarization
credentials stored once:

```sh
xcrun notarytool store-credentials logicx-notary \
    --apple-id you@example.com --team-id TEAMID --password <app-specific-password>
```

`SIGN_IDENTITY=-` signs ad-hoc and skips notarization, to check the image
builds without a certificate; `SIGN_IDENTITY`, `NOTARY_PROFILE` and `OUT_DIR`
override the rest.

Notarization is not optional for anyone else's Mac: Gatekeeper refuses a
signed but unnotarized image everywhere but the machine that built it.

## Release from GitHub

Pushing a `v*` tag runs the same script and attaches the image to the release.
That takes these repository secrets:

| secret | |
|---|---|
| `SIGNING_CERTIFICATE` | base64 of a `.p12` export of the certificate *and its private key* |
| `SIGNING_PASSWORD` | the password that `.p12` was exported with |

plus notarization credentials, either an App Store Connect API key:

| secret | |
|---|---|
| `NOTARY_KEY` | base64 of the API key `.p8` |
| `NOTARY_KEY_ID` | the key ID beside it |
| `NOTARY_ISSUER_ID` | the issuer ID from App Store Connect > Integrations |

or, while a team waits on App Store Connect API access, an Apple ID:

| secret | |
|---|---|
| `NOTARY_APPLE_ID` | the Apple ID of a team member |
| `NOTARY_TEAM_ID` | the team ID from developer.apple.com > Membership |
| `NOTARY_PASSWORD` | an app-specific password from account.apple.com |

The API key wins where both are set, being scoped and revocable on its own.

## The app bundle

The app and the command are one binary: with arguments it is
`logicx-to-musicxml`, without them, and running from inside a `.app`, it opens
the dialog. `Info.plist` is what makes it an app — the icon, the `.logicx`
document type that lets projects be dropped on it, and the `NSServices` entry
that puts **Export to MusicXML** in every app's Services menu, answered by the
provider in `dialog.m`.

The menu item depends on the system having read that entry, which is what
`install.sh` asks for with `lsregister` and `pbs`. Someone who dragged the app
out of a disk image ran neither, so the dialog offers **Add to Logic Pro
Menu** — `NSUpdateDynamicServices()` — whenever the app was opened by hand
rather than from the menu or by a drop.

Changing `CFBundleIdentifier` after a release makes it a different app to
LaunchServices, Gatekeeper and notarization. Change it before, or not at all.
