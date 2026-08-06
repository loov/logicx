// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"howett.net/plist"
)

func TestOpenBundle_ParsesBinaryMetadataAndInstrument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo.logicx", "Alternatives", "000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	payload := make([]byte, 8)
	track := append([]byte{0x20}, []byte("Inst 1")...)
	track = append(track, make([]byte, 16-len(track))...)
	track = append(track, []byte{0x29, 0, 0xf7, 0xc5, 1, 0, 0, 0}...)
	payload = append(payload, track...)
	payload = append(payload, []byte("Pigments\x00utrAumua1taK")...)
	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	chunkHeader := make([]byte, 36)
	copy(chunkHeader, "tseT")
	binary.LittleEndian.PutUint64(chunkHeader[28:], uint64(len(payload)))
	data = append(data, chunkHeader...)
	data = append(data, payload...)
	if err := os.WriteFile(filepath.Join(dir, "ProjectData"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	metadata, err := plist.Marshal(map[string]any{
		"SongKey": "C", "BeatsPerMinute": int64(120), "NumberOfTracks": int64(-1),
		"SongSignatureNumerator": int64(-1), "AudioFiles": []string{"take.wav"},
	}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MetaData.plist"), metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := plist.Marshal(map[string]any{"Window": "Mixer"}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "DisplayStateArchive"), archive, 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := OpenBundle(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	alt := bundle.Alternatives[0]
	if len(bundle.PropertyLists) != 2 || bundle.PropertyLists["Alternatives/000/MetaData.plist"] == nil || bundle.PropertyLists["Alternatives/000/DisplayStateArchive"] == nil {
		t.Fatalf("property lists = %+v", bundle.PropertyLists)
	}
	if alt.Metadata.Key != "C" || math.Abs(alt.Metadata.BPM-120) > 1e-9 || alt.Metadata.AudioFileCount != 1 || alt.Metadata.TrackCount != 0 || alt.Metadata.TimeSignature != [2]uint64{4, 4} {
		t.Fatalf("metadata = %+v", alt.Metadata)
	}
	if len(alt.Project.Tracks) != 1 || alt.Project.Tracks[0].Kind != TrackKindInstrument {
		t.Fatalf("tracks = %+v", alt.Project.Tracks)
	}
	if len(alt.Project.Chunks) == 0 {
		t.Fatal("no raw chunks")
	}
	instrument := alt.Project.Tracks[0].Instrument
	if instrument == nil || instrument.Fingerprint() != "aumu/Kat1/Artu" || instrument.Name != "Pigments" {
		t.Fatalf("instrument = %+v", instrument)
	}
}

func TestParseProjectData_ParsesMIDINotes(t *testing.T) {
	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	data = appendChunk(data, "qeSM", []byte("header\x00Lead Trumpet"))
	data = appendChunk(data, "karT", nil)

	events := make([]byte, 48)
	note := events[16:48]
	note[0] = 0x90
	binary.LittleEndian.PutUint16(note[2:4], 3)
	binary.LittleEndian.PutUint32(note[4:8], 38_400)
	copy(note[10:12], "AP")
	note[12] = 74
	binary.LittleEndian.PutUint32(note[28:32], 720)
	data = appendChunk(data, "qSvE", events)

	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Sequences) != 1 || project.Sequences[0].Name != "Lead Trumpet" {
		t.Fatalf("sequences = %+v", project.Sequences)
	}
	got := project.Sequences[0].Notes[0]
	if got.Position != 38_400 || got.PositionFraction != 3 || got.Pitch != 74 || got.Duration != 720 || got.Raw[10] != 'A' {
		t.Fatalf("note = %+v", got)
	}
}

func TestParseProjectData_ParsesMarkers(t *testing.T) {
	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	events := make([]byte, 64)
	binary.LittleEndian.PutUint32(events[0:4], 0x12)
	binary.LittleEndian.PutUint32(events[4:8], 38_400)
	binary.LittleEndian.PutUint32(events[16:20], 4)
	binary.LittleEndian.PutUint32(events[20:24], 0x88000000)
	binary.LittleEndian.PutUint32(events[28:32], 7_680)
	data = appendChunk(data, "qSvE", events)
	data = appendChunkID(data, "qSxT", 4, []byte("prefix{\\rtf1\\ansi \\f0\\fs24 Chorus}"))

	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Markers) != 1 {
		t.Fatalf("markers = %+v", project.Markers)
	}
	marker := project.Markers[0]
	if marker.Position != 38_400 || marker.Length != 7_680 || marker.TextID != 4 || marker.Name != "Chorus" || marker.RTF == "" {
		t.Fatalf("marker = %+v", marker)
	}
}

func TestParseProjectData_ProjectChordsMatchMarkerOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/chords.logicx/Alternatives/000/ProjectData")
	if err != nil {
		t.Fatal(err)
	}
	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.ProjectChords) != 48 {
		t.Fatalf("project chords = %d, want 48", len(project.ProjectChords))
	}
	markers := make(map[uint32]string, len(project.Markers))
	for _, marker := range project.Markers {
		markers[marker.Position] = marker.Name
	}
	for _, chord := range project.ProjectChords {
		got := markers[chord.Position]
		if got != chord.Name {
			t.Errorf("chord at %d = %q, marker = %q", chord.Position, chord.Name, got)
		}
	}
	first := project.ProjectChords[0]
	if first.Position != 38_400 || first.Duration != 3_840 || first.SequenceID != 28 ||
		first.IntervalMask != 0x091 || first.RootPitchClass != 0 || first.RootSpelling != 2 ||
		!slices.Equal(first.Pitches, []uint8{60, 64, 67}) || len(first.Raw) != 32 {
		t.Fatalf("first chord = %+v", first)
	}
	slash := project.ProjectChords[26]
	if slash.Name != "C/E" || !slash.HasBass || slash.BassPitchClass != 4 || slash.BassSpelling != 2 ||
		!slices.Equal(slash.Pitches, []uint8{52, 60, 64, 67}) {
		t.Fatalf("slash chord = %+v", slash)
	}
	noChord := project.ProjectChords[37]
	if noChord.Name != "no chord" || !noChord.NoChord || noChord.IntervalMask != 0 || len(noChord.Pitches) != 0 {
		t.Fatalf("no chord = %+v", noChord)
	}
	last := project.ProjectChords[len(project.ProjectChords)-1]
	if last.Name != "Fbb" || last.Scale || last.IntervalMask != 0x091 ||
		last.RootPitchClass != 3 || last.RootSpelling != 0 || last.Attributes != 0x0ad50380 ||
		!slices.Equal(last.Pitches, []uint8{63, 67, 70}) {
		t.Fatalf("last chord = %+v", last)
	}
}

func TestParseProjectData_ChordSpellingIncludesDoubleSharpsAndBass(t *testing.T) {
	project := parseFixtureProject(t, "chord-spelling.logicx")
	if len(project.ProjectChords) != 30 {
		t.Fatalf("project chords = %d, want 30", len(project.ProjectChords))
	}
	root := project.ProjectChords[0]
	if root.Name != "F##" || root.RootPitchClass != 7 || root.RootSpelling != 4 {
		t.Fatalf("double-sharp root = %+v", root)
	}
	bass := project.ProjectChords[1]
	if bass.Name != "C/F##" || !bass.HasBass || bass.BassPitchClass != 7 || bass.BassSpelling != 4 {
		t.Fatalf("double-sharp bass = %+v", bass)
	}
	minorSeventh := project.ProjectChords[5]
	if minorSeventh.Name != "G##m7" || minorSeventh.IntervalMask != 0x489 || minorSeventh.ScaleMask != 0x5ad {
		t.Fatalf("minor seventh = %+v", minorSeventh)
	}
}

func TestParseProjectData_ScaleMask9B5IsHarmonicMajor(t *testing.T) {
	project := parseFixtureProject(t, "chord-scales.logicx")
	if len(project.ProjectChords) != 13 {
		t.Fatalf("project chords = %d, want 13", len(project.ProjectChords))
	}
	chord := project.ProjectChords[3]
	if chord.Name != "C harmonic major" || !chord.Scale || chord.ScaleMask != 0x9b5 {
		t.Fatalf("harmonic major = %+v", chord)
	}
}

func TestParseProjectData_ProjectChordBiasIgnoresTimeSignature(t *testing.T) {
	tests := []struct {
		name           string
		secondPosition uint32
	}{
		{"chord-position-3-4.logicx", 41_280},
		{"chord-position-5-8.logicx", 40_800},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := parseFixtureProject(t, test.name)
			if len(project.ProjectChords) != 2 || project.ProjectChords[0].Position != 38_400 || project.ProjectChords[1].Position != test.secondPosition {
				t.Fatalf("project chords = %+v", project.ProjectChords)
			}
		})
	}
}

func TestParseProjectData_ProjectChordDurationComesFromChildSequence(t *testing.T) {
	project := parseFixtureProject(t, "chord-duration.logicx")
	want := []uint32{960, 960, 960, 960, 7_680, 5_280}
	got := make([]uint32, len(project.ProjectChords))
	for i, chord := range project.ProjectChords {
		got[i] = chord.Duration
	}
	if !slices.Equal(got, want) {
		t.Fatalf("chord durations = %v, want %v", got, want)
	}
}

func TestParseProjectData_RegionChordsStayWithMIDISequence(t *testing.T) {
	project := parseFixtureProject(t, "chord-regions.logicx")
	if len(project.Sequences) != 2 {
		t.Fatalf("sequences = %+v", project.Sequences)
	}
	want := []string{"Aaug", "Ab/C", "Gm7", "Db7", "C7"}
	for _, sequence := range project.Sequences {
		got := make([]string, len(sequence.Chords))
		for i, chord := range sequence.Chords {
			got[i] = chord.Name
		}
		if !slices.Equal(got, want) {
			t.Errorf("sequence %q chords = %q, want %q", sequence.Name, got, want)
		}
	}
}

func parseFixtureProject(t *testing.T, name string) ProjectData {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name, "Alternatives", "000", "ProjectData"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func appendChunk(data []byte, descriptor string, payload []byte) []byte {
	return appendChunkID(data, descriptor, 0, payload)
}

func appendChunkID(data []byte, descriptor string, id uint32, payload []byte) []byte {
	header := make([]byte, 36)
	copy(header, descriptor)
	binary.LittleEndian.PutUint32(header[10:14], id)
	binary.LittleEndian.PutUint64(header[28:], uint64(len(payload)))
	return append(append(data, header...), payload...)
}
