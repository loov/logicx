// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixtureProjects returns the path and contents of every fixture ProjectData.
func fixtureProjects(t *testing.T) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.logicx", "Alternatives", "*", "ProjectData"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, filepath.Join("cmd", "logicx-from-audio", "template.logicx", "Alternatives", "000", "ProjectData"))
	projects := make(map[string][]byte)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		projects[path] = data
	}
	return projects
}

// saveAll saves every decoded value in p, failing on the first error. Track
// names, written only when changed, are forced to be written; lyrics and
// plug-in text are not, since their decoded form drops stored spaces and
// unprintable bytes.
func saveAll(t *testing.T, p *ProjectData) {
	t.Helper()
	var saves []func() error
	add := func(save func() error) { saves = append(saves, save) }
	for i := range p.TempoChanges {
		add(p.TempoChanges[i].Save)
	}
	for i := range p.TimeSignatures {
		add(p.TimeSignatures[i].Save)
	}
	for i := range p.KeySignatures {
		add(p.KeySignatures[i].Save)
	}
	for i := range p.Markers {
		add(p.Markers[i].Save)
	}
	for i := range p.ProjectChords {
		add(p.ProjectChords[i].Save)
	}
	for i := range p.Sequences {
		s := &p.Sequences[i]
		add(s.Save)
		for j := range s.Chords {
			add(s.Chords[j].Save)
		}
		for j := range s.Notes {
			n := &s.Notes[j]
			add(n.Save)
			for k := range n.Lyrics {
				add(n.Lyrics[k].Save)
			}
			for k := range n.ScoreArticulations {
				add(n.ScoreArticulations[k].Save)
			}
			for k := range n.ScoreFermatas {
				add(n.ScoreFermatas[k].Save)
			}
			for k := range n.ScoreOrnaments {
				add(n.ScoreOrnaments[k].Save)
			}
			for k := range n.ScoreArpeggios {
				add(n.ScoreArpeggios[k].Save)
			}
		}
	}
	for i := range p.AudioFiles {
		add(p.AudioFiles[i].Save)
	}
	for i := range p.AudioRegions {
		add(p.AudioRegions[i].Save)
	}
	for i := range p.Environment {
		add(p.Environment[i].Save)
	}
	for i := range p.Tracks {
		p.Tracks[i].name = ""
		add(p.Tracks[i].Save)
		for j := range p.Tracks[i].Sends {
			add(p.Tracks[i].Sends[j].Save)
		}
	}
	for i := range p.AudioUnits {
		add(p.AudioUnits[i].Save)
	}
	for _, save := range saves {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
}

// everySave returns the Save of every decoded value in p.
func everySave(p *ProjectData) []func() error {
	var saves []func() error
	for i := range p.TempoChanges {
		saves = append(saves, p.TempoChanges[i].Save)
	}
	for i := range p.TimeSignatures {
		saves = append(saves, p.TimeSignatures[i].Save)
	}
	for i := range p.KeySignatures {
		saves = append(saves, p.KeySignatures[i].Save)
	}
	for i := range p.Markers {
		saves = append(saves, p.Markers[i].Save)
	}
	for i := range p.ProjectChords {
		saves = append(saves, p.ProjectChords[i].Save)
	}
	for i := range p.Sequences {
		s := &p.Sequences[i]
		saves = append(saves, s.Save)
		for j := range s.Chords {
			saves = append(saves, s.Chords[j].Save)
		}
		for j := range s.Notes {
			n := &s.Notes[j]
			saves = append(saves, n.Save)
			for k := range n.Lyrics {
				saves = append(saves, n.Lyrics[k].Save)
			}
			for k := range n.ScoreArticulations {
				saves = append(saves, n.ScoreArticulations[k].Save)
			}
			for k := range n.ScoreFermatas {
				saves = append(saves, n.ScoreFermatas[k].Save)
			}
			for k := range n.ScoreOrnaments {
				saves = append(saves, n.ScoreOrnaments[k].Save)
			}
			for k := range n.ScoreArpeggios {
				saves = append(saves, n.ScoreArpeggios[k].Save)
			}
		}
	}
	for i := range p.AudioFiles {
		saves = append(saves, p.AudioFiles[i].Save)
	}
	for i := range p.AudioRegions {
		saves = append(saves, p.AudioRegions[i].Save)
	}
	for i := range p.Environment {
		saves = append(saves, p.Environment[i].Save)
	}
	for i := range p.Tracks {
		saves = append(saves, p.Tracks[i].Save)
		for j := range p.Tracks[i].Sends {
			saves = append(saves, p.Tracks[i].Sends[j].Save)
		}
	}
	for i := range p.AudioUnits {
		saves = append(saves, p.AudioUnits[i].Save)
	}
	return saves
}

func TestSave_UnchangedValuesKeepEveryByte(t *testing.T) {
	for path, data := range fixtureProjects(t) {
		project, err := ParseProjectData(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		saveAll(t, &project)
		got, err := project.MarshalBinary()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !slices.Equal(got, data) {
			at := 0
			for at < min(len(got), len(data)) && got[at] == data[at] {
				at++
			}
			t.Errorf("%s: saving unchanged values changed the file at byte %d", path, at)
		}
	}
}

func TestEncodeLyricText_MatchesStoredLyrics(t *testing.T) {
	checked := 0
	for path, data := range fixtureProjects(t) {
		project, err := ParseProjectData(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, sequence := range project.Sequences {
			for _, note := range sequence.Notes {
				for _, lyric := range note.Lyrics {
					stored := lyric.Raw[lyricText:]
					if lyricStoredText(stored) != lyric.Text {
						continue
					}
					got, err := encodeLyricText(lyric.Text)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.Equal(got, stored) {
						t.Errorf("%s: lyric %q encoded as % x, stored as % x", path, lyric.Text, got, stored)
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no lyrics checked")
	}
	t.Logf("checked %d lyrics", checked)
}

// reparse writes p and parses the result, checking that every event sequence
// is still in position order and ends with its sentinel.
func reparse(t *testing.T, p *ProjectData) ProjectData {
	t.Helper()
	data, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	reread, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range reread.Chunks {
		if chunk.Type == "EvSq" && chunk.Events == nil && len(chunk.Data) != 0 {
			t.Fatalf("event sequence at %d no longer splits into events", chunk.Offset)
		}
		for i, event := range chunk.Events {
			if i > 0 && eventPosition(chunk.Events[i-1]) > eventPosition(event) {
				t.Fatalf("event sequence at %d out of order at event %d", chunk.Offset, i)
			}
		}
		if n := len(chunk.Events); n > 0 && chunk.Events[n-1].Type != eventSentinel {
			t.Fatalf("event sequence at %d does not end with its sentinel", chunk.Offset)
		}
	}
	return reread
}

func TestMIDINote_SaveMovesTheRecordInOrder(t *testing.T) {
	project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
	notes := project.Sequences[0].Notes
	note := notes[0]
	last := notes[len(notes)-1]
	note.Pitch = 100
	note.SourcePosition = last.SourcePosition + 960
	if err := note.Save(); err != nil {
		t.Fatal(err)
	}
	reread := reparse(t, &project).Sequences[0].Notes
	if len(reread) != len(notes) {
		t.Fatalf("got %d notes, want %d", len(reread), len(notes))
	}
	moved := reread[len(reread)-1]
	if moved.Pitch != 100 || moved.Position != last.Position+960 || moved.Duration != note.Duration {
		t.Fatalf("moved note = %+v", moved)
	}
	if reread[0].Position != notes[1].Position {
		t.Fatalf("first note is at %d, want %d", reread[0].Position, notes[1].Position)
	}

	project.Refresh()
	if got := project.Sequences[0].Notes; got[len(got)-1].Pitch != 100 {
		t.Fatal("Refresh() did not pick up the saved note")
	}
}

func TestTempoChange_DeleteAndDuplicate(t *testing.T) {
	project := parseFixtureProject(t, "tempo-map-steps.logicx")
	tempos := project.TempoChanges
	if err := tempos[1].Delete(); err != nil {
		t.Fatal(err)
	}
	if err := tempos[1].Save(); err == nil {
		t.Fatal("Save() wrote a deleted tempo change")
	}
	copied, err := tempos[2].Duplicate()
	if err != nil {
		t.Fatal(err)
	}
	copied.Position, copied.BPM = tempos[0].Position+480, 60
	if err := copied.Save(); err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &project).TempoChanges
	if len(got) != 3 || got[0].BPM != 120 || got[1].Position != tempos[0].Position+480 || got[1].BPM != 60 || got[2].BPM != 140 {
		t.Fatalf("tempos = %+v", got)
	}
}

func TestLyric_SaveResizesForLongerText(t *testing.T) {
	project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
	lyric := project.Sequences[0].Notes[0].Lyrics[0]
	lyric.Text = "a lyric too long for one atom"
	if err := lyric.Save(); err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &project).Sequences[0].Notes[0].Lyrics
	if len(got) != 1 || got[0].Text != lyric.Text || len(got[0].Raw) != lyricText+2*atomSize {
		t.Fatalf("lyrics = %+v", got)
	}
	lyric.Text = "bad\x00"
	if err := lyric.Save(); err == nil {
		t.Fatal("Save() accepted unprintable lyric text")
	}
}

func TestScoreSymbols_SaveFollowsKind(t *testing.T) {
	project := parseFixtureProject(t, "score-notation-native-logic.logicx")
	notes := project.Sequences[0].Notes
	ornament := notes[13].ScoreOrnaments[0]
	ornament.Kind = ScoreOrnamentMordent
	fermata := notes[6].ScoreFermatas[0]
	fermata.Inverted = true
	arpeggio := notes[30].ScoreArpeggios[0]
	arpeggio.Direction = ScoreArpeggioDirectionDown
	for _, save := range []func() error{ornament.Save, fermata.Save, arpeggio.Save} {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
	got := reparse(t, &project).Sequences[0].Notes
	if got[13].ScoreOrnaments[0].Kind != ScoreOrnamentMordent || !got[6].ScoreFermatas[0].Inverted ||
		got[30].ScoreArpeggios[0].Direction != ScoreArpeggioDirectionDown {
		t.Fatalf("ornament %+v, fermata %+v, arpeggio %+v", got[13].ScoreOrnaments, got[6].ScoreFermatas, got[30].ScoreArpeggios)
	}
	ornament.Kind = ScoreOrnamentUnknown
	if err := ornament.Save(); err == nil {
		t.Fatal("Save() accepted an unknown ornament kind")
	}

	project = parseFixtureProject(t, "musicxml-roundtrip.logicx")
	articulation := project.Sequences[0].Notes[6].ScoreArticulations[0]
	articulation.Kind, articulation.Flipped = ScoreArticulationMarcato, true
	if err := articulation.Save(); err != nil {
		t.Fatal(err)
	}
	if got := reparse(t, &project).Sequences[0].Notes[6].ScoreArticulations[0]; got.Kind != ScoreArticulationMarcato || !got.Flipped || got.Code != 6 {
		t.Fatalf("articulation = %+v", got)
	}
}

func TestChord_SaveFoldsRootScaleAndBass(t *testing.T) {
	project := parseFixtureProject(t, "chords.logicx")
	chord := project.ProjectChords[0]
	chord.RootPitchClass, chord.RootSpelling = 2, 2
	chord.HasBass, chord.BassPitchClass, chord.BassSpelling = true, 6, 3
	if err := chord.Save(); err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &project).ProjectChords[0]
	if got.RootPitchClass != 2 || !got.HasBass || got.BassPitchClass != 6 || got.Position != chord.Position ||
		got.Name != "D"+chordSuffixes[chord.IntervalMask]+"/F#" {
		t.Fatalf("chord = %+v", got)
	}

	chord.HasBass, chord.NoChord = false, true
	if err := chord.Save(); err != nil {
		t.Fatal(err)
	}
	if got := reparse(t, &project).ProjectChords[0]; !got.NoChord || got.HasBass || got.Name != "no chord" {
		t.Fatalf("chord = %+v", got)
	}
}

func TestSignatures_SaveMeterGroupingAndKey(t *testing.T) {
	project := parseFixtureProject(t, "signature-grouping-7-8.logicx")
	meter := project.TimeSignatures[0]
	if len(meter.BeatGrouping) == 0 {
		t.Fatalf("meter = %+v", meter)
	}
	slices.Reverse(meter.BeatGrouping)
	reversed := slices.Clone(meter.BeatGrouping)
	if err := meter.Save(); err != nil {
		t.Fatal(err)
	}
	if got := reparse(t, &project).TimeSignatures[0]; !slices.Equal(got.BeatGrouping, reversed) {
		t.Fatalf("grouping = %v, want %v", got.BeatGrouping, reversed)
	}
	meter.Numerator, meter.Denominator, meter.BeatGrouping = 3, 4, []uint8{1, 1, 1, 1}
	if err := meter.Save(); err == nil {
		t.Fatal("Save() accepted a grouping that does not sum to the numerator")
	}

	project = parseFixtureProject(t, "signature-map-key.logicx")
	key := project.KeySignatures[0]
	key.Fifths, key.Minor = -3, true
	if err := key.Save(); err != nil {
		t.Fatal(err)
	}
	if got := reparse(t, &project).KeySignatures[0]; got.Fifths != -3 || !got.Minor {
		t.Fatalf("key = %+v", got)
	}
	meter = project.TimeSignatures[0]
	meter.Numerator, meter.Denominator = 6, 8
	if err := meter.Save(); err != nil {
		t.Fatal(err)
	}
	if got := reparse(t, &project).TimeSignatures[0]; got.Numerator != 6 || got.Denominator != 8 {
		t.Fatalf("meter = %+v", got)
	}
}

func TestTrackAndAudioUnit_SaveNames(t *testing.T) {
	project := parseFixtureProject(t, "plugins.logicx")
	track := project.Tracks[0]
	track.Name = "Renamed Track"
	unit := project.AudioUnits[0]
	unit.Name, unit.Setting, unit.Slot = "Renamed", "My Setting", unit.Slot+1
	for _, save := range []func() error{track.Save, unit.Save} {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
	reread := reparse(t, &project)
	if got := reread.Tracks[0].Name; got != track.Name {
		t.Fatalf("track name = %q", got)
	}
	if got := reread.AudioUnits[0]; got.Name != unit.Name || got.Setting != unit.Setting || got.Slot != unit.Slot {
		t.Fatalf("plug-in = %+v", got)
	}
	track.Name = "A name far too long"
	if err := track.Save(); err == nil {
		t.Fatal("Save() accepted a track name longer than its field")
	}
}

func TestUnknown_ExcludesDecodedFields(t *testing.T) {
	project := parseFixtureProject(t, "tempo-map-steps.logicx")
	var tempos, chunks int
	for _, r := range project.Unknown() {
		if r.Event == nil {
			chunks++
		}
		if r.Kind != "tempo" {
			continue
		}
		tempos++
		// Bytes 0..7 hold the type, fraction and position, and 16..19 the
		// tempo; none of them may be listed as unknown.
		for _, s := range r.Spans {
			for i := s.Offset; i < s.Offset+len(s.Data); i++ {
				if i < 8 || i >= 16 && i < 20 {
					t.Fatalf("tempo byte %d listed as unknown: %+v", i, r.Spans)
				}
			}
		}
	}
	if tempos != len(project.TempoChanges) || chunks == 0 {
		t.Fatalf("got %d tempo records for %d tempo changes, %d chunks", tempos, len(project.TempoChanges), chunks)
	}
}

// TestLibrary_RoundTrips checks every ProjectData under the folders listed in
// LOGICX_LIBRARY, one per line: each must write back byte for byte, and again
// after saving every decoded value unchanged. The files are only read.
func TestLibrary_RoundTrips(t *testing.T) {
	roots := os.Getenv("LOGICX_LIBRARY")
	if roots == "" {
		t.Skip("LOGICX_LIBRARY is not set")
	}
	var paths []string
	for _, root := range strings.Split(roots, "\n") {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == "ProjectData" {
				paths = append(paths, path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		project, err := ParseProjectData(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		saveAll(t, &project)
		if got, err := project.MarshalBinary(); err != nil || !slices.Equal(got, data) {
			t.Errorf("%s: does not write back unchanged", path)
		}
	}
	t.Logf("checked %d projects", len(paths))
}

func TestMIDISequence_SaveMovesRenamesAndLoops(t *testing.T) {
	project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
	region := project.Sequences[0]
	notes := len(region.Notes)
	first := region.Notes[0].Position
	region.Name = "Väike Lind"
	region.Position += 3840
	if err := region.Save(); err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &project).Sequences[0]
	if got.Name != region.Name || got.Position != region.Position || len(got.Notes) != notes || got.Notes[0].Position != first+3840 {
		t.Fatalf("region = %q at %d with %d notes, first at %d", got.Name, got.Position, len(got.Notes), got.Notes[0].Position)
	}

	region.Looped, region.Duration = true, 2*region.SourceDuration
	if err := region.Save(); err != nil {
		t.Fatal(err)
	}
	got = reparse(t, &project).Sequences[0]
	if !got.Looped || got.Duration != region.Duration || len(got.Notes) != 2*notes {
		t.Fatalf("looped region lasts %d (looped %v) with %d notes, want %d with %d", got.Duration, got.Looped, len(got.Notes), region.Duration, 2*notes)
	}

	region.Looped = false
	if err := region.Save(); err != nil {
		t.Fatal(err)
	}
	if got := reparse(t, &project).Sequences[0]; got.Looped || len(got.Notes) != notes {
		t.Fatalf("unlooped region looped %v with %d notes", got.Looped, len(got.Notes))
	}

	region.Position = 0
	if err := region.Save(); err == nil {
		t.Fatal("Save() accepted a region before the project start")
	}

	// Moving one of several regions placing a source would move them all.
	region = reparse(t, &project).Sequences[0]
	region.shared = true
	region.Position += 960
	if err := region.Save(); err == nil {
		t.Fatal("Save() moved a region whose source other regions place")
	}
}

func TestMarker_SaveRenamesThroughItsText(t *testing.T) {
	project := parseFixtureProject(t, "chords.logicx")
	if len(project.Markers) == 0 {
		t.Fatal("no markers")
	}
	marker := project.Markers[0]
	marker.Name = `Verse {2} \ end`
	if err := marker.Save(); err != nil {
		t.Fatal(err)
	}
	reread := reparse(t, &project)
	if got := reread.Markers[0]; got.Name != marker.Name || got.Position != marker.Position {
		t.Fatalf("marker = %q at %d, want %q", got.Name, got.Position, marker.Name)
	}
	for _, other := range reread.Markers[1:] {
		if other.TextID != marker.TextID && other.Name == marker.Name {
			t.Fatalf("marker %d renamed too", other.TextID)
		}
	}
	marker.Name = "Grüße"
	if err := marker.Save(); err == nil {
		t.Fatal("Save() accepted a marker name RTF decoding cannot read back")
	}
}

func TestRenameMarkerText_RejectsShortChunk(t *testing.T) {
	// Length fields that agree with a chunk too short to hold its RTF.
	data := make([]byte, 30)
	binary.LittleEndian.PutUint32(data[0:], 30)
	binary.LittleEndian.PutUint32(data[16:], markerTextStart)
	binary.LittleEndian.PutUint32(data[20:], 30)
	if _, err := renameMarkerText(data, "a", "b"); err == nil {
		t.Fatal("renameMarkerText() accepted a chunk shorter than its RTF offset")
	}
}

// FuzzProjectData checks that no input makes parsing, listing unknown bytes or
// saving every decoded value panic, and that what parses writes back
// unchanged.
func FuzzProjectData(f *testing.F) {
	for _, data := range fixtureProjects(&testing.T{}) {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		project, err := ParseProjectData(data)
		if err != nil {
			return
		}
		project.Unknown()
		got, err := project.MarshalBinary()
		if err != nil || !slices.Equal(got, data[:len(got)]) {
			t.Fatalf("parsed input does not write back: %v", err)
		}
		for _, save := range everySave(&project) {
			save() // errors are expected for malformed records; panics are not
		}
		for _, marker := range project.Markers {
			marker.Name = "Renamed"
			marker.Save()
		}
		for _, sequence := range project.Sequences {
			sequence.Name, sequence.Position = "Renamed", sequence.Position+960
			sequence.Save()
		}
	})
}

func TestRenameSequence_KeepsTheTailEvenlyAligned(t *testing.T) {
	project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
	before := project.Sequences[0].descriptor.Data
	start, _ := sequenceTail(before)
	tail := slices.Clone(before[start:])
	// "Roundtrip Oracle" ends at an even offset, "Väike Lind" at an odd one,
	// so the tail moves and gains a byte of padding.
	for _, name := range []string{"Väike Lind", "Even", "Roundtrip Oracle"} {
		got, err := renameSequence(before, name)
		if err != nil {
			t.Fatal(err)
		}
		at, ok := sequenceTail(got)
		if !ok || at%2 != 0 || !slices.Equal(got[at:], tail) || sequenceName(got) != name {
			t.Fatalf("%q: tail at %d, name %q", name, at, sequenceName(got))
		}
		if end := sequenceNameLength + 2 + len(name); end%2 == 1 && got[end] != 0 {
			t.Fatalf("%q: padding byte is %#x", name, got[end])
		}
	}
	if got, _ := renameSequence(before, "Roundtrip Oracle"); !slices.Equal(got, before) {
		t.Fatal("renaming to the same name changed the descriptor")
	}
}

// TestMIDINote_SaveMatchesLogic checks note fields against the bytes Logic
// wrote when the same change was made to this fixture's first note in Logic.
func TestMIDINote_SaveMatchesLogic(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(n *MIDINote)
		at   int
		want []byte
	}{
		{"release velocity 100", func(n *MIDINote) { n.ReleaseVelocity = 100 }, 16, []byte{0x64, 0x92}},
		{"channel 5", func(n *MIDINote) { n.Channel = 5 }, 0, []byte{0x94}},
		{"articulation ID 3", func(n *MIDINote) { n.ArticulationID = 3 }, 14, []byte{0x03}},
		{"muted", func(n *MIDINote) { n.Muted = true }, 15, []byte{0x10}},
	} {
		project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
		note := project.Sequences[0].Notes[0]
		c.edit(&note)
		if err := note.Save(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := note.Raw[c.at : c.at+len(c.want)]; !slices.Equal(got, c.want) {
			t.Errorf("%s: bytes %d.. = % x, want % x", c.name, c.at, got, c.want)
		}
		got := reparse(t, &project).Sequences[0].Notes
		if !slices.ContainsFunc(got, func(n MIDINote) bool {
			return n.Position == note.Position && n.Pitch == note.Pitch && n.Channel == note.Channel &&
				n.ReleaseVelocity == note.ReleaseVelocity && n.ArticulationID == note.ArticulationID && n.Muted == note.Muted
		}) {
			t.Errorf("%s: note not read back", c.name)
		}
	}
}

func TestParseProjectData_NotesOnEveryChannel(t *testing.T) {
	project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
	notes := len(project.Sequences[0].Notes)
	for i, note := range project.Sequences[0].Notes {
		note.Channel = uint8(i%16 + 1)
		if err := note.Save(); err != nil {
			t.Fatal(err)
		}
	}
	got := reparse(t, &project).Sequences[0].Notes
	if len(got) != notes {
		t.Fatalf("read back %d notes, want %d", len(got), notes)
	}
	for i, note := range got {
		if note.Channel != uint8(i%16+1) {
			t.Fatalf("note %d on channel %d, want %d", i, note.Channel, i%16+1)
		}
	}
}

// noteAttributeEdits are the Note Attributes set in Logic on the first notes
// of the note-attributes fixture, and on notes 1 to 5 of note-spelling.
var noteAttributeEdits = map[string][]NoteAttributes{
	"note-attributes.logicx": {
		{AccidentalType: AccidentalHide},
		{AccidentalType: AccidentalGuide},
		{AccidentalPosition: 3},
		{NoteHead: NoteHeadCross},
		{Tie: TieDown},
		{StemDirection: StemDown},
		{StemPosition: StemPositionSide},
		{Syncopation: SyncopationDefeat},
		{Interpretation: InterpretationForce},
		{HorizontalPosition: 5},
		{Size: 2},
		{NoteHead: NoteHeadHidden},
	},
	"note-spelling.logicx": {
		{},
		{EnharmonicShift: EnharmonicDoubleSharp},
		{EnharmonicShift: EnharmonicSharp},
		{EnharmonicShift: EnharmonicFlat},
		{AccidentalType: AccidentalForce},
		{EnharmonicShift: EnharmonicDoubleFlat, AccidentalType: AccidentalForce},
	},
}

func TestNoteAttributes_DecodeWhatLogicSet(t *testing.T) {
	// The original fixture, imported from MusicXML, already carries note heads
	// on two notes: a filled diamond and a cross.
	original := parseFixtureProject(t, "musicxml-roundtrip.logicx").Sequences[0].Notes
	if original[37].Attributes.NoteHead != NoteHeadFilledDiamond || original[38].Attributes.NoteHead != NoteHeadCross {
		t.Errorf("imported note heads = %d, %d", original[37].Attributes.NoteHead, original[38].Attributes.NoteHead)
	}
	for fixture, want := range noteAttributeEdits {
		notes := parseFixtureProject(t, fixture).Sequences[0].Notes
		for i, n := range notes {
			w := original[i].Attributes
			if i < len(want) {
				w = want[i]
			}
			if n.Attributes != w {
				t.Errorf("%s note %d: attributes %+v, want %+v", fixture, i, n.Attributes, w)
			}
		}
	}
}

func TestNoteAttributes_SaveReproducesLogic(t *testing.T) {
	for fixture, edits := range noteAttributeEdits {
		logic := parseFixtureProject(t, fixture).Sequences[0].Notes
		project := parseFixtureProject(t, "musicxml-roundtrip.logicx")
		for i, a := range edits {
			note := project.Sequences[0].Notes[i]
			// Respelling some notes in Logic also moved them a semitone.
			note.Attributes, note.Pitch = a, logic[i].Pitch
			if err := note.Save(); err != nil {
				t.Fatal(err)
			}
			ours, theirs := note.ref.event.Data, logic[i].ref.event.Data
			if len(ours) != len(theirs) || !bytes.Equal(ours[:15], theirs[:15]) || !bytes.Equal(ours[16:], theirs[16:]) ||
				ours[15]&^0x01 != theirs[15]&^0x01 {
				t.Errorf("%s note %d:\n ours  % x\n Logic % x", fixture, i, ours, theirs)
			}
		}
	}
}

func TestNoteAttributes_ClearingRemovesTheAtom(t *testing.T) {
	project := parseFixtureProject(t, "note-attributes.logicx")
	note := project.Sequences[0].Notes[5] // stem down, ahead of a marcato
	if len(note.ScoreArticulations) != 1 {
		t.Fatalf("articulations = %+v", note.ScoreArticulations)
	}
	note.Attributes = NoteAttributes{}
	if err := note.Save(); err != nil {
		t.Fatal(err)
	}
	if len(note.ref.event.Data) != 48 {
		t.Fatalf("note record is %d bytes, want 48", len(note.ref.event.Data))
	}
	// The marcato moved up into the removed atom's place; saving it must
	// write there.
	articulation := note.ScoreArticulations[0]
	articulation.Kind, articulation.Flipped = ScoreArticulationAccent, false
	if err := articulation.Save(); err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &project).Sequences[0].Notes[5]
	if got.Attributes != (NoteAttributes{}) || len(got.ScoreArticulations) != 1 || got.ScoreArticulations[0].Kind != ScoreArticulationAccent {
		t.Fatalf("note = %+v %+v", got.Attributes, got.ScoreArticulations)
	}

	// A copy taken before the note was saved points at a moved atom.
	project = parseFixtureProject(t, "note-attributes.logicx")
	note = project.Sequences[0].Notes[5]
	stale := note.ScoreArticulations[0]
	note.Attributes = NoteAttributes{}
	if err := note.Save(); err != nil {
		t.Fatal(err)
	}
	if err := stale.Save(); err == nil {
		t.Fatal("Save() wrote an articulation whose atom moved")
	}
}

// mixerEdits are the mixer settings set in Logic on the instrument tracks of
// the mixer fixture; the rest of the tracks were left at their defaults.
var mixerEdits = map[string]func(*Track){
	"Inst 1":  func(t *Track) { t.SetVolumeDB(-6) },
	"Inst 2":  func(t *Track) { t.SetVolumeDB(3) },
	"Inst 3":  func(t *Track) { t.SetVolumeDB(-20) },
	"Inst 4":  func(t *Track) { t.Pan = -64 },
	"Inst 5":  func(t *Track) { t.Pan = 20 },
	"Inst 6":  func(t *Track) { t.Mute = true },
	"Inst 7":  func(t *Track) { t.Solo = true },
	"Inst 11": func(t *Track) { t.InputMonitoring = true },
}

func trackNamed(t *testing.T, p *ProjectData, name string) *Track {
	t.Helper()
	for i := range p.Tracks {
		if p.Tracks[i].Name == name {
			return &p.Tracks[i]
		}
	}
	t.Fatalf("no track %q", name)
	return nil
}

func TestTrack_DecodesTheMixer(t *testing.T) {
	logic := parseFixtureProject(t, "mixer.logicx")
	base := parseFixtureProject(t, "mixer-base.logicx")
	for name, edit := range mixerEdits {
		want := *trackNamed(t, &base, name)
		edit(&want)
		got := trackNamed(t, &logic, name)
		// The fader was dragged, so its level is only near what was typed.
		if math.Abs(got.VolumeDB()-want.VolumeDB()) > 0.05 || got.Pan != want.Pan ||
			got.Mute != want.Mute || got.Solo != want.Solo || got.InputMonitoring != want.InputMonitoring {
			t.Errorf("%s: got %.2f dB pan %d mute %v solo %v monitor %v, want %.2f dB pan %d mute %v solo %v monitor %v",
				name, got.VolumeDB(), got.Pan, got.Mute, got.Solo, got.InputMonitoring,
				want.VolumeDB(), want.Pan, want.Mute, want.Solo, want.InputMonitoring)
		}
	}
	if db := trackNamed(t, &base, "Inst 1").VolumeDB(); db != 0 {
		t.Errorf("default volume = %v dB", db)
	}
}

func TestTrack_SaveReproducesLogicsMixer(t *testing.T) {
	logic := parseFixtureProject(t, "mixer.logicx")
	project := parseFixtureProject(t, "mixer-base.logicx")
	for name, edit := range mixerEdits {
		track, theirs := trackNamed(t, &project, name), trackNamed(t, &logic, name)
		edit(track)
		track.Volume = theirs.Volume
		if err := track.Save(); err != nil {
			t.Fatal(err)
		}
		ours, want := track.chunk.Data, theirs.chunk.Data
		// Logic marks every other strip as muted by the solo.
		if len(ours) != len(want) || !bytes.Equal(ours[:channelStripMute], want[:channelStripMute]) ||
			ours[channelStripMute]&^0x02 != want[channelStripMute]&^0x02 ||
			!bytes.Equal(ours[channelStripMute+1:], want[channelStripMute+1:]) {
			t.Errorf("%s:\n ours  % x\n Logic % x", name, ours, want)
		}
	}
}

func TestTrack_SetVolumeDB(t *testing.T) {
	var track Track
	for _, db := range []float64{-20, -10, 0, 3} {
		track.SetVolumeDB(db)
		if got := track.VolumeDB(); math.Abs(got-db) > 1e-6 {
			t.Errorf("SetVolumeDB(%v) reads back %v", db, got)
		}
	}
	track.SetVolumeDB(math.Inf(-1))
	if track.Volume != 0 {
		t.Errorf("-Inf dB = %#x", track.Volume)
	}
	track.SetVolumeDB(20)
	if track.Volume != 127<<24 {
		t.Errorf("+20 dB = %#x, want Logic's maximum", track.Volume)
	}
}

func TestTrack_DecodesRoutingAndColor(t *testing.T) {
	p := parseFixtureProject(t, "mixer-routing.logicx")
	for name, want := range map[string]int16{"Inst 1": 1, "Inst 2": 3, "Inst 3": -2, "Inst 4": 0} {
		if got := trackNamed(t, &p, name).Output; got != want {
			t.Errorf("%s output = %d, want %d", name, got, want)
		}
	}
	for name, want := range map[string]int16{"Aux 1": 1, "Aux 2": 3, "Inst 1": -1} {
		if got := trackNamed(t, &p, name).Input; got != want {
			t.Errorf("%s input = %d, want %d", name, got, want)
		}
	}
	for name, want := range map[string][2]int{"Inst 7": {0, 0}, "Inst 8": {3, 23}, "Inst 9": {1, 8}} {
		if row, column := trackNamed(t, &p, name).PaletteColor(); row != want[0] || column != want[1] {
			t.Errorf("%s color at %d,%d, want %v", name, row, column, want)
		}
	}
}

func TestSend_DecodesWhatLogicSet(t *testing.T) {
	p := parseFixtureProject(t, "mixer-routing.logicx")
	type send struct {
		Index, Bus uint8
		DB         float64
		PreFader   bool
	}
	want := map[string][]send{
		// Sends whose level was left alone stay at -Inf, where Logic starts
		// them.
		"Inst 4": {{0, 1, math.Inf(-1), false}},
		"Inst 5": {{0, 4, -10, false}, {1, 5, -20, false}},
		"Inst 6": {{0, 1, math.Inf(-1), true}},
		"Inst 7": nil,
	}
	for name, sends := range want {
		var got []send
		for _, s := range trackNamed(t, &p, name).Sends {
			db := math.Round(40*math.Log10(float64(s.Level)/unityVolume)*1e6) / 1e6
			got = append(got, send{s.Index, s.Bus, db, s.PreFader})
		}
		if !slices.Equal(got, sends) {
			t.Errorf("%s sends = %v, want %v", name, got, sends)
		}
	}
}

func TestSend_LevelUsesTheFaderScale(t *testing.T) {
	// The same sends, raised in Logic to 0 dB and to the top of the knob.
	p := parseFixtureProject(t, "mixer-routing-alt.logicx")
	if got := trackNamed(t, &p, "Inst 4").Sends[0].Level; got != unityVolume {
		t.Errorf("0 dB send level = %#x, want %#x", got, unityVolume)
	}
	if got := trackNamed(t, &p, "Inst 6").Sends[0].Level; got != 127<<24 {
		t.Errorf("+6 dB send level = %#x, want %#x", got, 127<<24)
	}
}

func TestSend_Save(t *testing.T) {
	p := parseFixtureProject(t, "mixer-routing.logicx")
	send := &trackNamed(t, &p, "Inst 5").Sends[0]
	send.Level, send.Pan, send.PreFader = volumeFromDB(-3), -10, true
	if err := send.Save(); err != nil {
		t.Fatal(err)
	}
	got := trackNamed(t, ptr(reparse(t, &p)), "Inst 5").Sends[0]
	if got.Level != volumeFromDB(-3) || got.Pan != -10 || !got.PreFader || got.Bus != 4 {
		t.Fatalf("send = %+v", got)
	}
	if d := got.chunk.Data; d[sendLevel7] != uint8(got.Level>>24) || d[sendPost] != 0 {
		t.Fatalf("send record % x", d[:sendMinimum])
	}
}

func TestTrack_SaveColor(t *testing.T) {
	p := parseFixtureProject(t, "mixer-base.logicx")
	logic := parseFixtureProject(t, "mixer-routing.logicx")
	track := trackNamed(t, &p, "Inst 8")
	track.SetPaletteColor(3, 23)
	if err := track.Save(); err != nil {
		t.Fatal(err)
	}
	if got, want := track.environment.Data[environmentColor], trackNamed(t, &logic, "Inst 8").Color; got != want {
		t.Fatalf("color = %d, Logic saved %d", got, want)
	}
	if got := trackNamed(t, ptr(reparse(t, &p)), "Inst 8").Color; got != track.Color {
		t.Fatalf("reparsed color = %d", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestTrack_DecodesAutomation(t *testing.T) {
	p := parseFixtureProject(t, "mixer-automation.logicx")
	db := func(v uint32) float64 { return math.Round(40*math.Log10(float64(v)/unityVolume)*10) / 10 }
	type point struct {
		Position  uint32
		Parameter AutomationParameter
		Value     float64
	}
	// The values Logic showed on the points; pan and mute as stored.
	want := map[string][]point{
		"Inst 1": {{38400, AutomationVolume, 0}, {42160, AutomationVolume, -9.7}, {46400, AutomationVolume, -18.8}},
		"Inst 2": {{38400, AutomationVolume, 0}, {41600, AutomationVolume, 0}, {53920, AutomationVolume, -20.7}},
		"Inst 3": {{38400, AutomationPan, 0}, {42320, AutomationPan, 64}, {46160, AutomationPan, 127}},
		"Inst 4": {{38400, AutomationMute, 0}, {42240, AutomationMute, 0}, {46000, AutomationMute, 1}},
		"Inst 5": nil,
	}
	for name, points := range want {
		var got []point
		for _, a := range trackNamed(t, &p, name).Automation {
			v := math.Round(float64(a.Value) / (1 << 24))
			if a.Parameter == AutomationVolume {
				v = db(a.Value)
			}
			got = append(got, point{a.Position, a.Parameter, v})
		}
		if !slices.Equal(got, points) {
			t.Errorf("%s automation = %v, want %v", name, got, points)
		}
	}
}
