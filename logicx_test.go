// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
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

	// A channel strip is one AuCO chunk with its record at a fixed offset, and
	// each of its plug-ins is an AuCU chunk naming the same strip.
	const strip = 7
	stripPayload := make([]byte, channelStripRecord)
	track := append([]byte{0x20}, []byte("Inst 1")...)
	track = append(track, make([]byte, 16-len(track))...)
	track = append(track, []byte{0x29, 0, 0xf7, 0xc5, 1, 0, 0, 0}...)
	stripPayload = append(stripPayload, track...)

	pluginPayload := make([]byte, pluginRecordSize)
	binary.LittleEndian.PutUint16(pluginPayload[pluginChainOffset:], chainAudio)
	binary.LittleEndian.PutUint16(pluginPayload[pluginSlotOffset:], 0)
	copy(pluginPayload[pluginSetting:], "#default.pst")
	copy(pluginPayload[pluginName:], "Pigments")
	copy(pluginPayload[pluginManufacturer:], "utrAumua1taK")

	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	data = appendAudioChunk(data, "OCuA", channelStripVariant, strip, stripPayload)
	data = appendAudioChunk(data, "UCuA", pluginVariant, strip, pluginPayload)
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
	note[16] = 0x40
	note[23] = 0x89
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

func TestParseProjectData_ScoreArticulationsPreserveKindAndDirection(t *testing.T) {
	sequences := parseFixtureProject(t, "articulations.logicx").Sequences
	if len(sequences) != 1 || len(sequences[0].Notes) != 7 {
		t.Fatalf("sequences = %+v", sequences)
	}
	want := []ScoreArticulationKind{
		ScoreArticulationUnknown, ScoreArticulationStaccato, ScoreArticulationTenuto,
		ScoreArticulationAccent, ScoreArticulationMarcato, ScoreArticulationMarcato,
		ScoreArticulationStaccatissimo,
	}
	for i, note := range sequences[0].Notes {
		if i == 0 {
			if len(note.ScoreArticulations) != 0 {
				t.Errorf("normal note articulations = %+v", note.ScoreArticulations)
			}
			continue
		}
		if len(note.ScoreArticulations) != 1 || note.ScoreArticulations[0].Kind != want[i] || note.ScoreArticulations[0].Flipped != (i == 5) {
			t.Errorf("note %d articulations = %+v", i, note.ScoreArticulations)
		}
	}
}

func TestParseProjectData_MusicXMLImportPreservesNotesLyricsAndArticulations(t *testing.T) {
	sequences := parseFixtureProject(t, "musicxml-roundtrip.logicx").Sequences
	if len(sequences) != 1 || len(sequences[0].Notes) != 43 {
		t.Fatalf("sequences = %+v", sequences)
	}
	notes := sequences[0].Notes
	if notes[0].Raw[10] != 0x69 || len(notes[0].Lyrics) != 1 || notes[0].Lyrics[0].Text != "normal" {
		t.Fatalf("first note = %+v", notes[0])
	}
	if len(notes[8].Lyrics) != 2 || notes[8].Lyrics[0].Text != "mul-" || notes[8].Lyrics[0].Verse != 1 || notes[8].Lyrics[1].Text != "verse" || notes[8].Lyrics[1].Verse != 2 {
		t.Fatalf("multi-verse note = %+v", notes[8])
	}
	if got := notes[6].ScoreArticulations; len(got) != 1 || got[0].Kind != ScoreArticulationStaccatissimo || got[0].Code != 4 {
		t.Fatalf("imported staccatissimo = %+v", got)
	}
}

func TestParseProjectData_NativeScoreFixturePreservesNotation(t *testing.T) {
	sequences := parseFixtureProject(t, "score-notation-native-logic.logicx").Sequences
	if len(sequences) != 1 || len(sequences[0].Notes) != 36 {
		t.Fatalf("sequences = %+v", sequences)
	}
	notes := sequences[0].Notes
	if got := notes[11].Lyrics[0].Text; got != "pedal stop" {
		t.Fatalf("pedal stop lyric = %q", got)
	}
	if len(notes[6].ScoreFermatas) != 1 || notes[6].ScoreFermatas[0].Inverted ||
		len(notes[7].ScoreFermatas) != 1 || !notes[7].ScoreFermatas[0].Inverted {
		t.Fatalf("fermatas = %+v, %+v", notes[6].ScoreFermatas, notes[7].ScoreFermatas)
	}
	wantOrnaments := []ScoreOrnamentKind{
		ScoreOrnamentTrill, ScoreOrnamentTurn, ScoreOrnamentMordent,
		ScoreOrnamentInvertedTurn, ScoreOrnamentInvertedMordent,
		ScoreOrnamentInvertedTurnWithLine, ScoreOrnamentTremolo,
	}
	for i, want := range wantOrnaments {
		got := notes[13+i].ScoreOrnaments
		if len(got) != 1 || got[0].Kind != want {
			t.Errorf("note %d ornaments = %+v, want %q", 13+i, got, want)
		}
	}
	wantDirections := []ScoreArpeggioDirection{ScoreArpeggioDirectionNone, ScoreArpeggioDirectionUp, ScoreArpeggioDirectionDown}
	for i, noteIndex := range []int{27, 30, 33} {
		got := notes[noteIndex].ScoreArpeggios
		if len(got) != 1 || got[0].Direction != wantDirections[i] {
			t.Errorf("note %d arpeggios = %+v, want direction %q", noteIndex, got, wantDirections[i])
		}
	}
}

func TestParseProjectData_SlurSegmentsBecomeSemanticEndpoints(t *testing.T) {
	sequences := parseFixtureProject(t, "score-slurs.logicx").Sequences
	if len(sequences) != 1 || len(sequences[0].Notes) != 32 {
		t.Fatalf("sequences = %+v", sequences)
	}
	notes := sequences[0].Notes
	check := func(note int, slurType ScoreSlurType, number uint8, placement ScoreSlurPlacement, rawCount int) {
		t.Helper()
		if len(notes[note].ScoreSlurs) != 1 {
			t.Errorf("note %d slurs = %+v", note, notes[note].ScoreSlurs)
			return
		}
		slur := notes[note].ScoreSlurs[0]
		if slur.Type != slurType || slur.Number != number || slur.Placement != placement || len(slur.Raw) != rawCount {
			t.Errorf("note %d slur = %+v", note, slur)
		}
	}
	check(0, ScoreSlurTypeStart, 1, ScoreSlurPlacementAutomatic, 1)
	check(4, ScoreSlurTypeStart, 1, ScoreSlurPlacementAutomatic, 2)
	check(8, ScoreSlurTypeStart, 1, ScoreSlurPlacementAutomatic, 3)
	check(14, ScoreSlurTypeStart, 1, ScoreSlurPlacementAutomatic, 3)
	for i := range 3 {
		check(20+i, ScoreSlurTypeStart, uint8(i+1), ScoreSlurPlacementAutomatic, 1)
		check(23+i, ScoreSlurTypeStop, uint8(i+1), ScoreSlurPlacementAutomatic, 1)
	}
	check(28, ScoreSlurTypeStart, 1, ScoreSlurPlacementAbove, 1)
	check(29, ScoreSlurTypeStop, 1, ScoreSlurPlacementAutomatic, 1)
	check(30, ScoreSlurTypeStart, 1, ScoreSlurPlacementBelow, 2)
	check(31, ScoreSlurTypeStop, 1, ScoreSlurPlacementAutomatic, 2)
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
	// A marker is three atoms; both trailing atoms set the continuation bit.
	events[39] = 0x88
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

func TestParseProjectData_ProjectChordLinkSelectsRecreatedChord(t *testing.T) {
	clean := parseFixtureProject(t, "chord-revisions-clean.logicx")
	revised := parseFixtureProject(t, "chord-revisions.logicx")
	if len(clean.ProjectChords) != 4 || len(revised.ProjectChords) != 4 {
		t.Fatalf("clean chords = %d, revised chords = %d", len(clean.ProjectChords), len(revised.ProjectChords))
	}
	want := []string{"C", "D", "E", "F"}
	got := make([]string, len(revised.ProjectChords))
	for i, chord := range revised.ProjectChords {
		got[i] = chord.Name
	}
	if !slices.Equal(got, want) || clean.ProjectChords[3].SequenceID != 40 || revised.ProjectChords[3].SequenceID != 44 {
		t.Fatalf("revised chords = %+v", revised.ProjectChords)
	}
}

func TestParseProjectData_RegionChordsStayWithMIDISequence(t *testing.T) {
	project := parseFixtureProject(t, "chord-regions.logicx")
	if len(project.Sequences) != 2 {
		t.Fatalf("sequences = %+v", project.Sequences)
	}
	for _, sequence := range project.Sequences {
		want := []string{"Aaug", "Ab/C", "Gm7", "Db7", "C7"}
		if sequence.Looped {
			want = []string{"Aaug", "Ab/C", "Aaug", "Ab/C"}
		}
		got := make([]string, len(sequence.Chords))
		for i, chord := range sequence.Chords {
			got[i] = chord.Name
		}
		if !slices.Equal(got, want) {
			t.Errorf("sequence %q chords = %q, want %q", sequence.Name, got, want)
		}
	}
}

func TestParseProjectData_RegionLoopingAndCropping(t *testing.T) {
	tests := []struct {
		name          string
		positions     []uint32
		durations     []uint32
		looped        []bool
		notes, chords []int
	}{
		{"chord-region-loop-clean.logicx", []uint32{38_400}, []uint32{7_680}, []bool{false}, []int{6}, []int{2}},
		{"chord-region-looped.logicx", []uint32{38_400}, []uint32{15_360}, []bool{true}, []int{12}, []int{4}},
		{"chord-region-cropped.logicx", []uint32{38_400, 46_080}, []uint32{7_680, 3_840}, []bool{false, false}, []int{6, 3}, []int{2, 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sequences := parseFixtureProject(t, test.name).Sequences
			if len(sequences) != len(test.positions) {
				t.Fatalf("sequences = %+v", sequences)
			}
			for i, sequence := range sequences {
				if sequence.Position != test.positions[i] || sequence.Duration != test.durations[i] || sequence.Looped != test.looped[i] || len(sequence.Notes) != test.notes[i] || len(sequence.Chords) != test.chords[i] {
					t.Errorf("sequence[%d] = %+v", i, sequence)
				}
				if len(sequence.Chords) == 0 {
					continue
				}
				last := sequence.Chords[len(sequence.Chords)-1]
				if last.Position+last.Duration != sequence.Position+sequence.Duration {
					t.Errorf("final chord ends at %d, region ends at %d", last.Position+last.Duration, sequence.Position+sequence.Duration)
				}
			}
		})
	}
}

func TestParseProjectData_TempoMapPreservesStepsAndCurves(t *testing.T) {
	steps := parseFixtureProject(t, "tempo-map-steps.logicx").TempoChanges
	linear := parseFixtureProject(t, "tempo-map-linear-ramp.logicx").TempoChanges
	smooth := parseFixtureProject(t, "tempo-map-smooth-ramp.logicx").TempoChanges
	if len(steps) != 3 || steps[0].Position != 38_400 || steps[0].BPM != 120 || steps[1].Position != 46_080 || steps[1].BPM != 90 || steps[2].Position != 53_760 || steps[2].BPM != 140 {
		t.Fatalf("step tempos = %+v", steps)
	}
	if len(linear) != 18 || len(smooth) != 18 || linear[1].Position != 42_240 || linear[17].Position != 49_920 || linear[17].BPM != 90 || math.Abs(linear[9].BPM-105.8824) > 0.000_001 || math.Abs(smooth[9].BPM-116.5284) > 0.000_001 {
		t.Fatalf("linear = %+v\nsmooth = %+v", linear, smooth)
	}
}

func TestParseProjectData_SignatureMapsPreserveKeysMetersAndGrouping(t *testing.T) {
	times := parseFixtureProject(t, "signature-map-time.logicx").TimeSignatures
	if len(times) != 3 || times[0].Position != 38_400 || times[0].Numerator != 4 || times[0].Denominator != 4 || times[1].Position != 46_080 || times[1].Numerator != 3 || times[1].Denominator != 4 || times[2].Position != 51_840 || times[2].Numerator != 5 || times[2].Denominator != 8 {
		t.Fatalf("time signatures = %+v", times)
	}
	keys := parseFixtureProject(t, "signature-map-key.logicx").KeySignatures
	if len(keys) != 3 || keys[0].Position != 38_400 || keys[0].Fifths != 0 || keys[0].Minor || keys[1].Position != 46_080 || keys[1].Fifths != 1 || keys[1].Minor || keys[2].Position != 53_760 || keys[2].Fifths != -6 || !keys[2].Minor {
		t.Fatalf("key signatures = %+v", keys)
	}
	five := parseFixtureProject(t, "signature-grouping-5-8.logicx").TimeSignatures
	seven := parseFixtureProject(t, "signature-grouping-7-8.logicx").TimeSignatures
	if len(five) != 2 || !slices.Equal(five[0].BeatGrouping, []uint8{2, 3}) || !slices.Equal(five[1].BeatGrouping, []uint8{3, 2}) {
		t.Fatalf("5/8 signatures = %+v", five)
	}
	if !five[0].PrintCompositeSignature || !five[1].PrintCompositeSignature || five[0].GroupingFlags != 0x0c {
		t.Fatalf("5/8 composite signature flags = %+v", five)
	}
	wantSeven := [][]uint8{{2, 2, 3}, {3, 2, 2}, {2, 3, 2}}
	if len(seven) != len(wantSeven) {
		t.Fatalf("7/8 signatures = %+v", seven)
	}
	for i := range seven {
		if !slices.Equal(seven[i].BeatGrouping, wantSeven[i]) || seven[i].PrintCompositeSignature || seven[i].GroupingFlags != 0x04 {
			t.Errorf("7/8 signature[%d] = %+v", i, seven[i])
		}
	}
}

func TestParseProjectData_MarkersDoNotDecodeAsTimeSignatures(t *testing.T) {
	// The middle of a marker record looks like a 1/1 meter header at tick
	// 2281701376; only the trailing record tells them apart.
	times := parseFixtureProject(t, "chords.logicx").TimeSignatures
	if len(times) != 1 || times[0].Position != 38_400 || times[0].Numerator != 4 || times[0].Denominator != 4 {
		t.Fatalf("time signatures = %+v", times)
	}
}

func TestSplitEvents_UsesTheContinuationBit(t *testing.T) {
	atom := func(tag, continuation byte) []byte {
		a := make([]byte, 16)
		a[0], a[7] = tag, continuation
		return a
	}
	var data []byte
	data = append(data, atom(0x12, 0)...)    // marker, three atoms
	data = append(data, atom(0x30, 0x88)...) // looks like a meter, but continues the marker
	data = append(data, atom(0x00, 0x88)...)
	data = append(data, atom(0x60, 0)...) // tempo, two atoms
	data = append(data, atom(0x00, 0x89)...)
	data = append(data, atom(0xf1, 0x3f)...) // sentinel

	events := splitEvents(data)
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	want := []struct {
		typ    byte
		offset int
		size   int
	}{{0x12, 0, 48}, {0x60, 48, 32}, {0xf1, 80, 16}}
	for i, w := range want {
		if events[i].Type != w.typ || events[i].Offset != w.offset || len(events[i].Data) != w.size {
			t.Errorf("event[%d] = type %#x offset %d size %d, want %#x %d %d",
				i, events[i].Type, events[i].Offset, len(events[i].Data), w.typ, w.offset, w.size)
		}
	}
}

func TestFindTracks_IncludesOutputChannelStrips(t *testing.T) {
	tracks := parseFixtureProject(t, "chords.logicx").Tracks
	kinds := map[TrackKind]int{}
	for _, track := range tracks {
		kinds[track.Kind]++
	}
	if kinds[TrackKindOutput] != 3 || kinds[TrackKindMaster] != 1 || kinds[TrackKindInstrument] == 0 {
		t.Fatalf("track kinds = %v", kinds)
	}
}

func TestFindAudioUnits_DecodesChainsSlotsAndBuiltins(t *testing.T) {
	var inst4 Track
	for _, track := range parseFixtureProject(t, "plugins.logicx").Tracks {
		if track.Name == "Inst 4" {
			inst4 = track
		}
	}
	if inst4.Instrument == nil {
		t.Fatal("Inst 4 has no instrument")
	}
	if got := inst4.Instrument; got.Name != "E-Piano" || !got.Builtin() || got.Setting != "#Custom#" {
		t.Errorf("instrument = %+v", got)
	}
	if len(inst4.MIDIFX) != 1 || inst4.MIDIFX[0].Name != "Arpeggiator" {
		t.Errorf("midi fx = %+v", inst4.MIDIFX)
	}
	// Inserts 2 to 4 are empty, so the slot numbers have a gap.
	if len(inst4.AudioFX) != 2 ||
		inst4.AudioFX[0].Name != "Channel EQ" || inst4.AudioFX[0].Slot != 1 ||
		inst4.AudioFX[1].Name != "Compressor" || inst4.AudioFX[1].Slot != 5 {
		t.Errorf("inserts = %+v", inst4.AudioFX)
	}
}

func TestFindAudioUnits_DecodesThirdPartyComponentDescription(t *testing.T) {
	var inst3 Track
	for _, track := range parseFixtureProject(t, "plugins.logicx").Tracks {
		if track.Name == "Inst 3" {
			inst3 = track
		}
	}
	unit := inst3.Instrument
	if unit == nil || unit.Fingerprint() != "aumu/FOne/FabF" || unit.Builtin() {
		t.Fatalf("instrument = %+v", unit)
	}
}

func TestFindAudioUnits_EmptyStripHasNoPlugins(t *testing.T) {
	for _, track := range parseFixtureProject(t, "plugins.logicx").Tracks {
		if track.Name != "Inst 1" {
			continue
		}
		if track.Instrument != nil || len(track.MIDIFX) != 0 || len(track.AudioFX) != 0 {
			t.Fatalf("Inst 1 = %+v", track)
		}
		return
	}
	t.Fatal("Inst 1 not found")
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

// appendAudioChunk appends an audio configuration chunk of the given header
// variant, belonging to the numbered channel strip.
func appendAudioChunk(data []byte, descriptor string, variant []byte, strip uint16, payload []byte) []byte {
	header := make([]byte, chunkHeaderSize)
	copy(header, descriptor)
	copy(header[channelStripHeaderStart:], variant)
	binary.LittleEndian.PutUint16(header[14:], strip)
	binary.LittleEndian.PutUint64(header[28:], uint64(len(payload)))
	return append(append(data, header...), payload...)
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

func TestParseProjectData_GroupedChordsExpandFromOneChild(t *testing.T) {
	project := parseFixtureProject(t, "chord-group.logicx")
	// C D E F are grouped into a single child sequence, C D E F follow as
	// separate children. All eight must land a bar apart.
	var got []string
	for _, chord := range project.ProjectChords {
		got = append(got, chord.Name)
	}
	if want := []string{"C", "D", "E", "F", "C", "D", "E", "F"}; !slices.Equal(got, want) {
		t.Fatalf("chords = %v, want %v", got, want)
	}
	for i, chord := range project.ProjectChords {
		if want := uint32(38_400 + 3_840*i); chord.Position != want || chord.Duration != 3_840 {
			t.Errorf("chord %d at %d for %d, want %d for 3840", i, chord.Position, chord.Duration, want)
		}
	}
}

func TestDecodeMIDINote_KeepsNotesCarryingVelocityAndTuning(t *testing.T) {
	// A Melodyne transcription fills in the per-note fields that a typed-in
	// note leaves zero; the record is still a note.
	var data [32]byte
	copy(data[:], []byte{
		0x90, 0, 0, 0, 0x80, 0x2a, 0, 0, 0, 0, 0, 0, 61, 0, 0, 1,
		0x40, 0, 0x19, 0xa9, 0x0d, 0, 0, 0x89, 0, 0, 0x2c, 0x01, 0xb0, 0x04, 0, 0,
	})
	note, ok := decodeMIDINote(data[:])
	if !ok {
		t.Fatal("note rejected")
	}
	if note.Position != 0x2a80 || note.Pitch != 61 || note.Duration != 1200 {
		t.Fatalf("note = %+v", note)
	}
}

func TestParseProjectData_KeyMapIgnoresChordRegionKeys(t *testing.T) {
	// Every chord child sequence carries a key record; only the signature
	// track holds the project key map.
	project := parseFixtureProject(t, "chord-group.logicx")
	if len(project.KeySignatures) != 1 {
		t.Fatalf("key signatures = %d, want 1", len(project.KeySignatures))
	}
	project = parseFixtureProject(t, "signature-map-key.logicx")
	if len(project.KeySignatures) != 3 {
		t.Fatalf("key map = %d changes, want 3", len(project.KeySignatures))
	}
}

func TestProjectData_MarshalBinaryRoundTrips(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*.logicx", "Alternatives", "*", "ProjectData"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		project, err := ParseProjectData(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got, err := project.MarshalBinary()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !slices.Equal(got, data) {
			t.Errorf("%s: wrote %d bytes that differ from the %d read", path, len(got), len(data))
		}
	}
}

func TestParseProjectData_AudioFileRegionAndTrackRename(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("cmd", "logicx-from-audio", "template.logicx", "Alternatives", "000", "ProjectData"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.AudioFiles) != 1 || len(project.AudioRegions) != 1 {
		t.Fatalf("got %d audio files and %d regions, want 1 each", len(project.AudioFiles), len(project.AudioRegions))
	}
	file, region := project.AudioFiles[0], project.AudioRegions[0]
	if file.Name != "Audio.wav" || file.Format != "WAVE" || file.Frames != 9106944 ||
		file.SampleRate != 44100 || file.Channels != 2 || file.BitDepth != 16 || file.Size != 36503036 ||
		file.Dir != "Audio Files" {
		t.Errorf("audio file = %+v", file)
	}
	if region.Name != "Audio" || region.Frames != 9106944 {
		t.Errorf("audio region = %+v", region)
	}
	track := slices.IndexFunc(project.Environment, func(o EnvironmentObject) bool { return o.Name == "Audio" })
	if track < 0 {
		t.Fatal("no environment object named after the region")
	}

	// Rewriting the decoded values must not disturb a byte.
	if err := project.SetAudioFile(0, file); err != nil {
		t.Fatal(err)
	}
	if err := project.SetAudioRegion(0, region); err != nil {
		t.Fatal(err)
	}
	if err := project.SetEnvironmentName(track, "Audio"); err != nil {
		t.Fatal(err)
	}
	if got, _ := project.MarshalBinary(); !slices.Equal(got, data) {
		t.Fatal("rewriting unchanged values changed the file")
	}

	file = AudioFile{Name: "Hole In Your Soul.wav", Dir: "Audio Files", Size: 1234, Format: "WAVE",
		Frames: 9750216, SampleRate: 48000, Channels: 1, BitDepth: 24}
	region = AudioRegion{Name: "Hole In Your Soul", Frames: 9750216}
	if err := project.SetAudioFile(0, file); err != nil {
		t.Fatal(err)
	}
	if err := project.SetAudioRegion(0, region); err != nil {
		t.Fatal(err)
	}
	if err := project.SetEnvironmentName(track, region.Name); err != nil {
		t.Fatal(err)
	}
	written, err := project.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	reread, err := ParseProjectData(written)
	if err != nil {
		t.Fatal(err)
	}
	if got := reread.AudioFiles[0]; got.Name != file.Name || got.Dir != file.Dir || got.Size != file.Size ||
		got.Frames != file.Frames || got.SampleRate != file.SampleRate || got.Channels != file.Channels ||
		got.BitDepth != file.BitDepth {
		t.Errorf("reread audio file = %+v", got)
	}
	if got := reread.AudioRegions[0]; got.Name != region.Name || got.Frames != region.Frames {
		t.Errorf("reread audio region = %+v", got)
	}
	if got := reread.Environment[track].Name; got != region.Name {
		t.Errorf("reread track name = %q", got)
	}
	// The region, the track and the two loop family archives.
	if n := bytes.Count(written, []byte(region.Name)); n != 4 {
		t.Errorf("new name written %d times, want 4", n)
	}
}
