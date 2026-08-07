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
them, and chords use semantic MusicXML harmony elements. A staff with no
recorded notes is written as a chord chart, one rhythm slash per beat; pass
`-realize-chords` to voice the chord tones as pitches instead.
Timing is snapped to a notation grid — `-quantize`, a 1/16 note by default,
refined automatically where notes crowd closer than that — and durations are
split into tied notatable values, so raw performance data (a Melodyne
transcription, say) still produces a readable score. A beat whose notes fit
thirds of a beat better than the straight grid is written as a triplet. Notes that overlap within a part
are spread across voices, and silence in the first voice is written out as
rests.
Active MIDI-region placement, right-edge cropping, and loops are reconstructed;
looped notes, region chords, tempo curves, lyrics, and score articulations are
expanded in the exported score. Performance articulation-ID assignments remain
available in raw chunks but are not decoded yet.

## Run the exporter from Logic Pro

Logic Pro's Scripter plug-in cannot launch external programs, but a macOS
Automator Quick Action can run the exporter from Logic's Services menu or a
keyboard shortcut.

First, build the command from this repository:

```sh
mkdir -p "$HOME/bin"
go build -o "$HOME/bin/logicx-to-musicxml" ./cmd/logicx-to-musicxml
```

Then create the Quick Action:

1. Open Automator and choose **Quick Action**.
2. Set **Workflow receives current** to **no input** in **any application**.
3. Add **Run Shell Script**, choose `/bin/zsh`, and paste:

   ```sh
   project=$(/usr/bin/osascript -e 'POSIX path of (choose file with prompt "Choose a saved Logic Pro project")') || exit 0
   project=${project%/}
   output=${project%.logicx}.musicxml
   "$HOME/bin/logicx-to-musicxml" -o "$output" "$project" &&
       /usr/bin/open -R "$output"
   ```

4. Save the action as **Export Logic Project to MusicXML**.
5. In **System Settings > Keyboard > Keyboard Shortcuts > Services**, enable
   the action and optionally assign it a shortcut.

Save the Logic project before running the action. Invoke it from **Logic Pro >
Services** or with the assigned shortcut, choose the `.logicx` project, and the
exporter writes a `.musicxml` file next to it and reveals it in Finder.

The binary format is undocumented and may change between Logic versions.
Keep backups of irreplaceable projects. This package never writes to a bundle.

Format knowledge is based on
[`lpx-toolkit`](https://github.com/rhydlewis/lpx-toolkit) and
[`lpx-explorer`](https://github.com/rhydlewis/lpx-explorer).

License: GPL-3.0-or-later.
