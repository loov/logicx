# logicx

Read Logic Pro projects from Go, and export them as MusicXML.

This is unofficial and reverse-engineered, not affiliated with or endorsed by
Apple. Apple documents none of the format, so everything here is best effort,
worked out from real projects: it decodes what has been figured out, skips
what it does not recognise, and can be wrong or incomplete for projects,
plug-ins or Logic versions it has not met. Read an exported score against the
project before trusting it, and expect a Logic update to be able to break the
parsing. Nothing here ever writes to a bundle, but keep backups of
irreplaceable projects anyway.

## MusicXML Bridge for Logic Pro

```sh
./install.sh
```

builds **MusicXML Bridge for Logic Pro.app** into `~/Applications` and links
the command, `logicx-to-musicxml`, into `$HOME/bin`. `APP_DIR` and `BIN_DIR`
override both; uninstalling is deleting them.

Save the Logic project, then drop it on the app, open it with the app, or
choose **Logic Pro > Services > Export to MusicXML** and pick it. The app
provides that menu item itself: Logic's Scripter plug-in cannot launch
programs, but every app's Services menu can. Opening the app by hand offers an
**Add to Logic Pro Menu** button that registers it, and **System Settings >
Keyboard > Keyboard Shortcuts > Services** will give it a shortcut.

A dialog offers the quantization grids, triplet detection, the chord and MIDI
options, and the project alternative when there is more than one. **Export…**
asks where to save, writes the files, and reveals them in Finder.

The app is the same binary as the command: with arguments it is a command,
without them it is the app.

## Command line

```sh
logicx-to-musicxml -o score.musicxml song.logicx
```

| flag | default | |
|---|---|---|
| `-o` | stdout | where the score is written |
| `-alternative` | first | which project alternative to export |
| `-quantize` | `1/16` | coarsest grid notes snap to; `off` keeps Logic's timing |
| `-quantize-chords` | `1/4` | grid chord symbols snap to; `off` keeps theirs |
| `-triplets` | true | notate beats played in thirds as triplets |
| `-realize-chords` | false | voice chord staves as pitches, not rhythm slashes |
| `-midi` | false | also write the raw notes beside `-o` as a MIDI file |
| `-ui` | false | ask for the settings in the dialog |

`-midi` is worth knowing about: notation programs quantize and detect tuplets
on import better than a grid snap here can, and the MusicXML alongside carries
the chord symbols, sections and layout that MIDI cannot express.

## What the export carries

**Parts.** Sequences sharing a Logic name merge into one part. Overlapping
notes are spread across voices, and silence in the first voice becomes rests.

**Maps.** Tempo, key and time signatures are reconstructed, asymmetric beat
grouping included. Markers become bold system text and close the preceding bar
with a double barline — deliberately not rehearsal marks, which notation
programs renumber into their own A, B, C and so lose the section name.

**Chords.** Region chords stay on their own sequence; project chords join a
staff that already plays them, or get a `Project Chords` staff. They are
written as semantic MusicXML harmony elements, and land on their own grid,
since a chord change belongs on the beat however loosely it was played. A
staff with no recorded notes becomes a chord chart, one rhythm slash per beat.

**Timing.** Notes snap to a notation grid, refined automatically where they
crowd closer than it, and durations split into tied notatable values, so raw
performance data — a Melodyne transcription, say — still reads. A beat whose
notes fit thirds better than the straight grid is written as a triplet.
Turning quantization off turns detection off with it, since it works by
measuring how far onsets sit off a grid there no longer is.

**Notation.** Staff notes are spelled from the key signature, or from the
chord track where the key is the C major Logic starts out with. Region
placement, right-edge cropping and loops are reconstructed, with looped notes,
region chords, tempo curves, lyrics and score articulations expanded.
Performance articulation-ID assignments stay in the raw chunks, undecoded.

## Library

```go
bundle, err := logicx.OpenBundle("song.logicx")
if err != nil {
	log.Fatal(err)
}
fmt.Println(bundle.Alternatives[0].Metadata.BPM)
fmt.Println(bundle.PropertyLists["Resources/ProjectInformation.plist"])
fmt.Println(bundle.Alternatives[0].Project.Sequences[0].Notes[0].Pitch)
```

Every plist value (binary ones without an extension included) and every raw
`ProjectData` chunk is exposed, alongside decoded metadata, channel strips,
Audio Units, MIDI sequences, score articulations, markers, tempo maps and
key/time signatures. `ParseProjectData` and `ParseMetadata` take bytes, for a
boundary that touches no files.

---

Building, releasing and how the app bundle is put together:
[DEVELOPMENT.md](DEVELOPMENT.md). The vocabulary the score reconstruction uses:
[CONTEXT.md](CONTEXT.md).

Format knowledge is based on
[`lpx-toolkit`](https://github.com/rhydlewis/lpx-toolkit) and
[`lpx-explorer`](https://github.com/rhydlewis/lpx-explorer). Logic Pro is a
trademark of Apple Inc.

License: GPL-3.0-or-later.
