# logicx

Go library for read-only inspection of Logic Pro `.logicx` project bundles.
It exposes every plist value (including extensionless binary plists) and every
raw `ProjectData` chunk, plus decoded project metadata, channel strips, Audio
Units, MIDI note sequences, score articulations, markers, tempo maps, and
key/time signatures.

```go
bundle, err := logicx.OpenBundle("song.logicx")
if err != nil {
	log.Fatal(err)
}
fmt.Println(bundle.Alternatives[0].Metadata.BPM)
fmt.Println(bundle.PropertyLists["Resources/ProjectInformation.plist"])
fmt.Println(bundle.Alternatives[0].Project.Sequences[0].Notes[0].Pitch)
```

For a bytes-only safety boundary, use `ParseProjectData` and `ParseMetadata`.
Both XML and binary property lists are supported.

Export the discovered MIDI sequences as MusicXML:

```sh
go run ./cmd/logicx-to-musicxml -o score.musicxml song.logicx
```

Pass `-midi` to write the note data beside the score as a Standard MIDI File,
with Logic's own timing left unquantized. Notation programs quantize and detect
tuplets when they import MIDI, which they do better than a grid snap here can;
the MusicXML alongside carries the chord symbols, sections and layout that MIDI
has no way to express.

Sequences with the same Logic name are combined into one MusicXML part.
Tempo, key, and time-signature maps are reconstructed, including asymmetric
beat grouping; markers become bold system text and end the preceding bar with a
double barline. They are deliberately not rehearsal marks, which notation
programs renumber into their own A, B, C sequence and so lose the section name. Region chords stay on their owning sequence; decoded project
chords get a `Project Chords` staff unless an existing staff already contains
them, and chords use semantic MusicXML harmony elements. Staff notes are
spelled from the key signature, or from the chord track where the key signature
is the C major Logic starts out with. Chord symbols land on
their own grid, `-quantize-chords`, a quarter note by default, since a chord
change belongs on the beat however loosely it was played. A staff with no
recorded notes is written as a chord chart, one rhythm slash per beat; pass
`-realize-chords` to voice the chord tones as pitches instead.
Timing is snapped to a notation grid — `-quantize`, a 1/16 note by default,
refined automatically where notes crowd closer than that — and durations are
split into tied notatable values, so raw performance data (a Melodyne
transcription, say) still produces a readable score. A beat whose notes fit
thirds of a beat better than the straight grid is written as a triplet, unless
`-triplets=false` says to keep the straight grid. Either grid can be turned
off with `-quantize off` or `-quantize-chords off`, which writes what Logic
recorded, tick for tick; triplet detection goes with it, since it measures how
far the onsets sit off the grid there is no longer. Notes that overlap within a part
are spread across voices, and silence in the first voice is written out as
rests.
Active MIDI-region placement, right-edge cropping, and loops are reconstructed;
looped notes, region chords, tempo curves, lyrics, and score articulations are
expanded in the exported score. Performance articulation-ID assignments remain
available in raw chunks but are not decoded yet.

## The Export to MusicXML app

```sh
./install.sh
```

builds **Export to MusicXML.app** into `~/Applications` (override with
`APP_DIR`) and links the command into `$HOME/bin` (`BIN_DIR`). Uninstall by
deleting those two paths.

The app declares itself as a **Services** menu item, which is how Logic Pro
gets one: Logic's Scripter plug-in cannot launch programs, but every app's
Services menu can.

Save the Logic project first, then either drop it on the app, open it with the
app, or choose **Logic Pro > Services > Export to MusicXML** and pick it. A
dialog offers the two quantization grids (**off** included), triplet
detection, the chord and MIDI options, and the project alternative when there
is more than one; **Export…** asks where to
save, writes the files, and reveals them in Finder. Give the Quick Action a
keyboard shortcut in **System Settings > Keyboard > Keyboard Shortcuts >
Services** if you want one.

The app is the same binary as the command — with arguments it is a command,
without them it is the app — and the dialog is AppKit called directly through
cgo.

## Release a disk image

`install.sh` builds for this machine and does not sign, which is all a local
build needs. To hand the app to someone else:

```sh
VERSION=1.0 ./release.sh
```

builds a universal (arm64 and x86_64) app, signs it with the hardened runtime,
packs it into a drag-to-Applications disk image, notarizes it, and staples the
ticket, leaving `build/ExportToMusicXML-1.0.dmg`. It needs a **Developer ID
Application** certificate from the paid Apple Developer Program, and
notarization credentials stored once:

```sh
xcrun notarytool store-credentials logicx-notary \
    --apple-id you@example.com --team-id TEAMID --password <app-specific-password>
```

Pushing a `v*` tag runs the same script on GitHub and attaches the image to
the release. That needs five repository secrets — `SIGNING_CERTIFICATE` (the
base64 of a `.p12` export of the Developer ID certificate *and its private
key*), `SIGNING_PASSWORD`, and `NOTARY_KEY`, `NOTARY_KEY_ID`,
`NOTARY_ISSUER_ID` from an App Store Connect API key, which is the
notarization credential a build machine can hold. The workflow comments say
where each comes from.

`SIGN_IDENTITY`, `NOTARY_PROFILE` and `OUT_DIR` override the defaults;
`SIGN_IDENTITY=-` signs ad-hoc and skips notarization, for checking the image
builds without a certificate. A disk image that is signed but not notarized is
refused by Gatekeeper on any Mac but the one that built it.

The binary format is undocumented and may change between Logic versions.
Keep backups of irreplaceable projects. This package never writes to a bundle.

Format knowledge is based on
[`lpx-toolkit`](https://github.com/rhydlewis/lpx-toolkit) and
[`lpx-explorer`](https://github.com/rhydlewis/lpx-explorer).

License: GPL-3.0-or-later.
