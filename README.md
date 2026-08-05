# logicx

Go library for read-only inspection of Logic Pro `.logicx` project bundles.
It exposes every plist value (including extensionless binary plists) and every
raw `ProjectData` chunk, plus decoded project metadata, channel strips, Audio
Units, MIDI note sequences, and markers.

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

Sequences with the same Logic name are combined into one MusicXML part.
Global tempo, key, mode, and time signature come from `MetaData.plist`; markers
are emitted as rehearsal marks. Region chords stay on their owning sequence;
decoded project chords get a `Project Chords` staff unless an existing staff
already contains them, and chords use semantic MusicXML harmony elements.
Tempo maps, region chord records, active-region
references, and articulation assignments remain available in raw chunks but
are not decoded yet.

The binary format is undocumented and may change between Logic versions.
Keep backups of irreplaceable projects. This package never writes to a bundle.

Format knowledge is based on
[`lpx-toolkit`](https://github.com/rhydlewis/lpx-toolkit) and
[`lpx-explorer`](https://github.com/rhydlewis/lpx-explorer).

License: GPL-3.0-or-later.
