// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"encoding/binary"
	"io/fs"
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
