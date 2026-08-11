// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	encodingxml "encoding/xml"
	"slices"
	"strings"
	"testing"

	"github.com/loov/logicx"
)

func TestWriteMusicXML(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{Key: "G", Mode: "major", BPM: 120, TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{
			Sequences: []logicx.MIDISequence{{
				Name: "Trumpet", Notes: []logicx.MIDINote{
					{Position: logicBarOneTick, Pitch: 67, Duration: 720, Lyrics: []logicx.Lyric{{Verse: 1, Text: "hello"}}, ScoreArticulations: []logicx.ScoreArticulation{
						{Kind: logicx.ScoreArticulationStaccato},
						{Kind: logicx.ScoreArticulationTenuto},
						{Kind: logicx.ScoreArticulationAccent},
						{Kind: logicx.ScoreArticulationMarcato, Flipped: true},
						{Kind: logicx.ScoreArticulationStaccatissimo},
					}, ScoreFermatas: []logicx.ScoreFermata{{}, {Inverted: true}}, ScoreOrnaments: []logicx.ScoreOrnament{
						{Kind: logicx.ScoreOrnamentTrill}, {Kind: logicx.ScoreOrnamentTurn},
						{Kind: logicx.ScoreOrnamentInvertedTurn}, {Kind: logicx.ScoreOrnamentInvertedTurnWithLine},
						{Kind: logicx.ScoreOrnamentMordent}, {Kind: logicx.ScoreOrnamentInvertedMordent},
						{Kind: logicx.ScoreOrnamentTremolo},
					}, ScoreArpeggios: []logicx.ScoreArpeggio{{}, {Direction: logicx.ScoreArpeggioDirectionUp}, {Direction: logicx.ScoreArpeggioDirectionDown}},
						ScoreSlurs: []logicx.ScoreSlur{{Type: logicx.ScoreSlurTypeStart, Number: 1, Placement: logicx.ScoreSlurPlacementAbove}}},
					{Position: logicBarOneTick, Pitch: 71, Duration: 720},
					{Position: logicBarOneTick + 3_600, Pitch: 74, Duration: 480, ScoreSlurs: []logicx.ScoreSlur{{Type: logicx.ScoreSlurTypeStop, Number: 1}}},
				},
			}},
			Markers: []logicx.Marker{{Position: logicBarOneTick, Name: "Chorus"}},
			TempoChanges: []logicx.TempoChange{
				{Position: logicBarOneTick, BPM: 120},
				{Position: logicBarOneTick + 1_920, BPM: 90},
			},
			TimeSignatures: []logicx.TimeSignatureChange{
				{Position: logicBarOneTick, Numerator: 4, Denominator: 4},
				{Position: logicBarOneTick + 3_840, Numerator: 5, Denominator: 8, BeatGrouping: []uint8{2, 3}, PrintCompositeSignature: true},
			},
			KeySignatures: []logicx.KeySignatureChange{
				{Position: logicBarOneTick, Fifths: 1},
				{Position: logicBarOneTick + 3_840, Fifths: -6, Minor: true},
			},
		},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{realizeChords: true, quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}
	xml := output.String()
	for _, want := range []string{
		`<score-partwise version="4.0">`, `<part-name>Trumpet</part-name>`, `<fifths>1</fifths>`,
		`<sound tempo="120"></sound>`, `<sound tempo="90"></sound>`, `<offset>1920</offset>`,
		`<per-minute>120</per-minute>`, `<per-minute>90</per-minute>`,
		`<chord></chord>`, `<tie type="start"></tie>`, `<tie type="stop"></tie>`,
		`<words font-weight="bold" enclosure="rectangle">Chorus</words>`, `<beats>2+3</beats>`, `<beat-type>8</beat-type>`,
		`<fifths>-6</fifths>`, `<mode>minor</mode>`,
		`<staccato></staccato>`, `<tenuto></tenuto>`, `<accent></accent>`,
		`<strong-accent type="down"></strong-accent>`, `<staccatissimo></staccatissimo>`,
		`<fermata type="upright">normal</fermata>`, `<fermata type="inverted">normal</fermata>`,
		`<arpeggiate></arpeggiate>`, `<arpeggiate direction="up"></arpeggiate>`, `<arpeggiate direction="down"></arpeggiate>`,
		`<trill-mark></trill-mark>`, `<turn></turn>`, `<inverted-turn></inverted-turn>`,
		`<inverted-vertical-turn></inverted-vertical-turn>`, `<mordent></mordent>`,
		`<inverted-mordent></inverted-mordent>`, `<tremolo>3</tremolo>`,
		`<slur type="start" number="1" placement="above"></slur>`, `<slur type="stop" number="1"></slur>`,
		`<lyric number="1">`, `<text>hello</text>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("output does not contain %q:\n%s", want, xml)
		}
	}
}

func TestMergeSequences(t *testing.T) {
	merged := mergeSequences([]logicx.MIDISequence{
		{Name: "Trumpet", Notes: []logicx.MIDINote{{Pitch: 60}}},
		{Name: "Bass", Notes: []logicx.MIDINote{{Pitch: 36}}},
		{Name: "Trumpet", Notes: []logicx.MIDINote{{Pitch: 64}}, Chords: []logicx.Chord{{Name: "C"}}},
	})
	if len(merged) != 2 || len(merged[0].Notes) != 2 || merged[0].Notes[1].Pitch != 64 || len(merged[0].Chords) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestWriteMusicXML_ProjectChordsGetSeparateStaff(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{Key: "C", Mode: "major", TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{ProjectChords: []logicx.Chord{{
			Position: logicBarOneTick, Duration: 1_920, Name: "C", Pitches: []uint8{60, 64, 67},
			IntervalMask: 0x091, RootSpelling: 2,
		}}},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{realizeChords: true, quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}
	xml := output.String()
	// Three chord tones for the half bar, then a rest filling the rest of it.
	if !strings.Contains(xml, "<part-name>Project Chords</part-name>") || !strings.Contains(xml, "<kind>major</kind>") ||
		strings.Count(xml, "<note>") != 4 || strings.Count(xml, "<rest></rest>") != 1 {
		t.Fatalf("project chord staff missing:\n%s", xml)
	}
}

func TestMusicXMLChordKinds_CoversMusicXML40Vocabulary(t *testing.T) {
	if len(musicXMLChordKinds) != 33 {
		t.Fatalf("chord kinds = %d, want 33", len(musicXMLChordKinds))
	}
	seen := make(map[string]bool, len(musicXMLChordKinds))
	for _, kind := range musicXMLChordKinds {
		if seen[kind.Name] {
			t.Fatalf("duplicate chord kind %q", kind.Name)
		}
		seen[kind.Name] = true
	}
}

func TestMakeHarmony_UsesKindsAndAlteredDegrees(t *testing.T) {
	dominant := makeHarmony(logicx.Chord{IntervalMask: 0x491, RootSpelling: 2}, 0)
	if dominant.Kind.Value != "dominant" || len(dominant.Degrees) != 0 {
		t.Fatalf("dominant harmony = %+v", dominant)
	}

	altered := makeHarmony(logicx.Chord{
		Name: "C(#11)", IntervalMask: 0x0d1, RootSpelling: 2,
	}, 0)
	wantDegrees := []xmlHarmonyDegree{
		{Value: 3, Type: "add"}, {Value: 11, Alter: 1, Type: "add"}, {Value: 5, Type: "add"},
	}
	if altered.Kind.Value != "other" || altered.Kind.Text != "(#11)" || !slices.Equal(altered.Degrees, wantDegrees) {
		t.Fatalf("altered harmony = %+v", altered)
	}
}

func TestScoreSequences_ProjectChordsReuseExistingStaff(t *testing.T) {
	chord := logicx.Chord{Position: logicBarOneTick, Name: "C", Pitches: []uint8{60, 64, 67}}
	project := logicx.ProjectData{
		Sequences: []logicx.MIDISequence{{Name: "Piano", Notes: []logicx.MIDINote{
			{Position: logicBarOneTick, Pitch: 60},
			{Position: logicBarOneTick, Pitch: 64},
			{Position: logicBarOneTick, Pitch: 67},
		}}},
		ProjectChords: []logicx.Chord{chord},
	}
	sequences := scoreSequences(project, options{realizeChords: true})
	if len(sequences) != 1 || sequences[0].Name != "Piano" || len(sequences[0].Chords) != 1 {
		t.Fatalf("score sequences = %+v", sequences)
	}
}

func TestScoreSequences_RegionChordsStayOnRegionStaff(t *testing.T) {
	project := logicx.ProjectData{Sequences: []logicx.MIDISequence{{
		Name: "Guitar", Chords: []logicx.Chord{{
			Position: logicBarOneTick, Duration: 960, Name: "Dm", Pitches: []uint8{62, 65, 69},
		}},
	}}}
	sequences := scoreSequences(project, options{realizeChords: true})
	if len(sequences) != 1 || sequences[0].Name != "Guitar" || len(sequences[0].Notes) != 3 {
		t.Fatalf("score sequences = %+v", sequences)
	}
}

func TestScoreSequences_RegionChordsPreserveRecordedNotes(t *testing.T) {
	project := logicx.ProjectData{Sequences: []logicx.MIDISequence{{
		Name:  "Guitar",
		Notes: []logicx.MIDINote{{Position: logicBarOneTick, Pitch: 62, Duration: 960}},
		Chords: []logicx.Chord{{
			Position: logicBarOneTick, Duration: 960, Name: "Dm", Pitches: []uint8{62, 65, 69},
		}},
	}}}
	sequences := scoreSequences(project, options{realizeChords: true})
	if len(sequences) != 1 || len(sequences[0].Notes) != 1 || sequences[0].Notes[0].Pitch != 62 {
		t.Fatalf("score sequences = %+v", sequences)
	}
}

// TestWriteMusicXML_NotatableAndMonophonicVoices covers what MuseScore refuses
// to import: durations no note symbol can express, two notes sounding at once
// in one voice, and parts of unequal length.
func TestWriteMusicXML_NotatableAndMonophonicVoices(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{BPM: 120, TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{Sequences: []logicx.MIDISequence{
			{Name: "Melody", Notes: []logicx.MIDINote{
				{Position: logicBarOneTick, Pitch: 60, Duration: 1_200},         // quarter + 16th
				{Position: logicBarOneTick + 1_200, Pitch: 62, Duration: 1_920}, // overlaps the next
				{Position: logicBarOneTick + 1_920, Pitch: 64, Duration: 481},   // off-grid
			}},
			{Name: "Bass", Notes: []logicx.MIDINote{
				{Position: logicBarOneTick + 3_840*4, Pitch: 36, Duration: 3_840},
			}},
		}},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{realizeChords: true, quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}

	var score struct {
		Parts []struct {
			Measures []struct {
				Notes []struct {
					Chord    *struct{} `xml:"chord"`
					Duration uint32    `xml:"duration"`
					Voice    int       `xml:"voice"`
				} `xml:"note"`
			} `xml:"measure"`
		} `xml:"part"`
	}
	if err := encodingxml.Unmarshal(output.Bytes(), &score); err != nil {
		t.Fatal(err)
	}
	if len(score.Parts) != 2 {
		t.Fatalf("parts = %d", len(score.Parts))
	}
	if a, b := len(score.Parts[0].Measures), len(score.Parts[1].Measures); a != b {
		t.Errorf("parts have %d and %d measures", a, b)
	}
	for _, part := range score.Parts {
		for _, measure := range part.Measures {
			for _, note := range measure.Notes {
				if notatable(note.Duration) != note.Duration {
					t.Errorf("duration %d is not a single note value", note.Duration)
				}
			}
		}
	}

	// The overlapping pair must not share a voice.
	voices := map[int]bool{}
	for _, measure := range score.Parts[0].Measures {
		for _, note := range measure.Notes {
			voices[note.Voice] = true
		}
	}
	if len(voices) < 2 {
		t.Errorf("overlapping notes stayed in voices %v", voices)
	}
}

func TestWriteMusicXML_MarkersOpenSectionsWithDoubleBarlines(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{BPM: 120, TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{
			Sequences: []logicx.MIDISequence{
				{Name: "Lead", Notes: []logicx.MIDINote{{Position: logicBarOneTick, Pitch: 60, Duration: 3_840}}},
				{Name: "Bass", Notes: []logicx.MIDINote{{Position: logicBarOneTick, Pitch: 36, Duration: 3_840}}},
			},
			Markers: []logicx.Marker{
				{Position: logicBarOneTick, Name: "intro"},           // bar 1 keeps its plain barline
				{Position: logicBarOneTick + 3_840*2, Name: "verse"}, // bar 3 opens a section
			},
		},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{realizeChords: true, quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}

	var score struct {
		Parts []struct {
			Measures []struct {
				Number  int `xml:"number,attr"`
				Barline *struct {
					Location string `xml:"location,attr"`
					Style    string `xml:"bar-style"`
				} `xml:"barline"`
			} `xml:"measure"`
		} `xml:"part"`
	}
	if err := encodingxml.Unmarshal(output.Bytes(), &score); err != nil {
		t.Fatal(err)
	}
	if len(score.Parts) != 2 {
		t.Fatalf("parts = %d", len(score.Parts))
	}
	// Every part carries the divider, or it shows on one staff only. The bar
	// before the marker ends with it, which is where MuseScore reads it.
	for _, part := range score.Parts {
		last := len(part.Measures)
		for _, measure := range part.Measures {
			want := ""
			switch measure.Number {
			case 2:
				want = "light-light"
			case last:
				want = "light-heavy"
			}
			if measure.Barline == nil {
				if want != "" {
					t.Errorf("measure %d has no barline, want %s", measure.Number, want)
				}
				continue
			}
			if measure.Barline.Location != "right" || measure.Barline.Style != want {
				t.Errorf("measure %d barline = %+v, want right %s", measure.Number, *measure.Barline, want)
			}
		}
	}
}

func TestWriteMusicXML_ChordStaffWritesOneSlashPerBeat(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{BPM: 120, TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{ProjectChords: []logicx.Chord{
			// Two bars of C, starting and ending off the beat.
			{Position: logicBarOneTick + 120, Duration: 3_840*2 - 120, Name: "C", Pitches: []uint8{60, 64, 67}},
		}},
	}

	type note struct {
		Pitch struct {
			Step   string `xml:"step"`
			Octave int    `xml:"octave"`
		} `xml:"pitch"`
		Rest     *struct{} `xml:"rest"`
		Duration uint32    `xml:"duration"`
		Stem     string    `xml:"stem"`
		Notehead string    `xml:"notehead"`
	}
	parse := func(t *testing.T, realizeChords bool) []note {
		t.Helper()
		var output bytes.Buffer
		if err := writeMusicXML(&output, alternative, options{realizeChords: realizeChords, quantize: ticksPerQuarter / 4}); err != nil {
			t.Fatal(err)
		}
		var score struct {
			Parts []struct {
				Measures []struct {
					Notes []note `xml:"note"`
				} `xml:"measure"`
			} `xml:"part"`
		}
		if err := encodingxml.Unmarshal(output.Bytes(), &score); err != nil {
			t.Fatal(err)
		}
		var notes []note
		for _, part := range score.Parts {
			for _, measure := range part.Measures {
				for _, n := range measure.Notes {
					if n.Rest == nil {
						notes = append(notes, n)
					}
				}
			}
		}
		return notes
	}

	slashes := parse(t, false)
	if len(slashes) != 8 {
		t.Fatalf("slashes = %d, want 8 (two 4/4 bars)", len(slashes))
	}
	for i, note := range slashes {
		if note.Duration != ticksPerQuarter || note.Stem != "none" || note.Notehead != "slash" ||
			note.Pitch.Step != "B" || note.Pitch.Octave != 4 {
			t.Errorf("slash %d = %+v", i, note)
		}
	}

	realized := parse(t, true)
	if len(realized) == 0 {
		t.Fatal("realized chords produced no notes")
	}
	for i, note := range realized {
		if note.Notehead != "" || note.Stem != "" {
			t.Fatalf("realized note %d = %+v, want a plain notehead", i, note)
		}
	}
	if realized[0].Pitch.Step != "C" {
		t.Errorf("realized chord starts on %q, want C", realized[0].Pitch.Step)
	}
}

func TestWriteMusicXML_GapsAndEmptyBarsBecomeRests(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{Sequences: []logicx.MIDISequence{{
			Name: "Lead", Notes: []logicx.MIDINote{
				// One quarter on beat 2 of bar 1; bar 2 is silent.
				{Position: logicBarOneTick + 960, Pitch: 60, Duration: 960},
				{Position: logicBarOneTick + 3_840*2, Pitch: 60, Duration: 3_840},
			},
		}}},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}
	var score struct {
		Parts []struct {
			Measures []struct {
				Notes []struct {
					Rest *struct {
						Measure string `xml:"measure,attr"`
					} `xml:"rest"`
					Duration uint32 `xml:"duration"`
				} `xml:"note"`
				Forwards []struct{} `xml:"forward"`
			} `xml:"measure"`
		} `xml:"part"`
	}
	if err := encodingxml.Unmarshal(output.Bytes(), &score); err != nil {
		t.Fatal(err)
	}
	measures := score.Parts[0].Measures
	if len(measures) != 3 {
		t.Fatalf("measures = %d, want 3", len(measures))
	}
	// Every voice-1 gap is a rest, so nothing is left as blank space.
	for i, measure := range measures {
		if len(measure.Forwards) != 0 {
			t.Errorf("measure %d leaves %d gaps unfilled", i+1, len(measure.Forwards))
		}
		var total uint32
		for _, note := range measure.Notes {
			total += note.Duration
		}
		if total != 3_840 {
			t.Errorf("measure %d holds %d ticks, want a full 3840", i+1, total)
		}
	}
	if rest := measures[1].Notes[0].Rest; len(measures[1].Notes) != 1 || rest == nil || rest.Measure != "yes" {
		t.Errorf("silent bar = %+v, want one whole-measure rest", measures[1].Notes)
	}
}

func TestQuantizeNotes_RefinesTheGridForFastRuns(t *testing.T) {
	const sixteenth = ticksPerQuarter / 4
	notes := []logicx.MIDINote{
		// Two sloppy quarters, then a run of 32nds that a 1/16 grid would
		// collapse onto one another.
		{Position: 0, Pitch: 60, Duration: 947},
		{Position: 971, Pitch: 62, Duration: 940},
		{Position: 1_920, Pitch: 64, Duration: 110},
		{Position: 2_042, Pitch: 65, Duration: 115},
		{Position: 2_160, Pitch: 67, Duration: 120},
	}
	got := quantizeNotes(notes, sixteenth, beatGrid{})
	want := []struct{ position, duration uint32 }{
		{0, 960}, {960, 960}, {1_920, 120}, {2_040, 120}, {2_160, 120},
	}
	for i, note := range got {
		if note.Position != want[i].position || note.Duration != want[i].duration {
			t.Errorf("note %d at %d for %d, want %d for %d",
				i, note.Position, note.Duration, want[i].position, want[i].duration)
		}
	}
	// A note that stopped before the next began must not grow over it.
	for i := 1; i < len(got); i++ {
		if notes[i-1].Position+notes[i-1].Duration <= notes[i].Position &&
			got[i-1].Position+got[i-1].Duration > got[i].Position {
			t.Errorf("note %d now overlaps note %d", i-1, i)
		}
	}
}

// TestWriteMusicXML_QuantizationAndTripletsOff checks the two switches that
// hand the raw performance through: no grid, and no tuplets.
func TestWriteMusicXML_QuantizationAndTripletsOff(t *testing.T) {
	alternative := tripletAlternative()
	loosest := alternative.Project.Sequences[0].Notes[1].Position

	for _, test := range []struct {
		name    string
		options options
		tuplets bool
		onset   uint32 // where the second note starts, in ticks from bar one
	}{
		{name: "quantized", options: options{quantize: ticksPerQuarter / 4}, tuplets: true, onset: 320},
		{name: "no triplets", options: options{quantize: ticksPerQuarter / 4, noTriplets: true}, onset: 240},
		{name: "no quantization", options: options{}, onset: loosest - logicBarOneTick},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeMusicXML(&output, alternative, test.options); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(output.String(), "<tuplet"); got != test.tuplets {
				t.Errorf("tuplets in score = %v, want %v", got, test.tuplets)
			}
			// Durations up to the second note say where it was placed.
			var score struct {
				Parts []struct {
					Measures []struct {
						Notes []struct {
							Duration uint32    `xml:"duration"`
							Rest     *struct{} `xml:"rest"`
						} `xml:"note"`
					} `xml:"measure"`
				} `xml:"part"`
			}
			if err := encodingxml.Unmarshal(output.Bytes(), &score); err != nil {
				t.Fatal(err)
			}
			var onset uint32
			for _, note := range score.Parts[0].Measures[0].Notes {
				if onset >= test.onset {
					break
				}
				onset += note.Duration
			}
			if onset != test.onset {
				t.Errorf("the second note starts at %d ticks, want %d", onset, test.onset)
			}
		})
	}
}

// tripletAlternative is bar 1 beat 1: three eighth-note triplets, played a
// little loose. Beat 2: two straight eighths. The rest of the bar is silent.
func tripletAlternative() logicx.Alternative {
	return logicx.Alternative{
		Metadata: logicx.Metadata{TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{Sequences: []logicx.MIDISequence{{
			Name: "Lead", Notes: []logicx.MIDINote{
				{Position: logicBarOneTick + 3, Pitch: 60, Duration: 310},
				{Position: logicBarOneTick + 327, Pitch: 62, Duration: 300},
				{Position: logicBarOneTick + 634, Pitch: 64, Duration: 320},
				{Position: logicBarOneTick + 960, Pitch: 65, Duration: 480},
				{Position: logicBarOneTick + 1_440, Pitch: 67, Duration: 480},
			},
		}}},
	}
}

func TestWriteMusicXML_TripletBeatsNotateAsTuplets(t *testing.T) {
	// Bar 1 beat 1: three eighth-note triplets, played a little loose. Beat 2:
	// two straight eighths. The rest of the bar is silent.
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{Sequences: []logicx.MIDISequence{{
			Name: "Lead", Notes: []logicx.MIDINote{
				{Position: logicBarOneTick + 3, Pitch: 60, Duration: 310},
				{Position: logicBarOneTick + 327, Pitch: 62, Duration: 300},
				{Position: logicBarOneTick + 634, Pitch: 64, Duration: 320},
				{Position: logicBarOneTick + 960, Pitch: 65, Duration: 480},
				{Position: logicBarOneTick + 1_440, Pitch: 67, Duration: 480},
			},
		}}},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}
	var score struct {
		Parts []struct {
			Measures []struct {
				Notes []struct {
					Duration         uint32 `xml:"duration"`
					Type             string `xml:"type"`
					TimeModification *struct {
						Actual int `xml:"actual-notes"`
						Normal int `xml:"normal-notes"`
					} `xml:"time-modification"`
					Notations struct {
						Tuplets []struct {
							Type string `xml:"type,attr"`
						} `xml:"tuplet"`
					} `xml:"notations"`
				} `xml:"note"`
			} `xml:"measure"`
		} `xml:"part"`
	}
	if err := encodingxml.Unmarshal(output.Bytes(), &score); err != nil {
		t.Fatal(err)
	}
	notes := score.Parts[0].Measures[0].Notes

	// The first three notes are the tuplet, bracketed and scaled 3:2.
	for i := range 3 {
		note := notes[i]
		if note.Duration != 320 || note.Type != "eighth" {
			t.Errorf("triplet note %d = %d ticks, type %q", i, note.Duration, note.Type)
		}
		if note.TimeModification == nil || note.TimeModification.Actual != 3 || note.TimeModification.Normal != 2 {
			t.Errorf("triplet note %d has no 3:2 scaling", i)
		}
	}
	if got := len(notes[0].Notations.Tuplets); got != 1 || notes[0].Notations.Tuplets[0].Type != "start" {
		t.Errorf("tuplet does not open on the first note: %+v", notes[0].Notations.Tuplets)
	}
	if got := len(notes[2].Notations.Tuplets); got != 1 || notes[2].Notations.Tuplets[0].Type != "stop" {
		t.Errorf("tuplet does not close on the third note: %+v", notes[2].Notations.Tuplets)
	}

	// The straight beat that follows keeps out of it.
	for i := 3; i < 5; i++ {
		if notes[i].Duration != 480 || notes[i].TimeModification != nil {
			t.Errorf("straight note %d = %d ticks, scaling %+v", i, notes[i].Duration, notes[i].TimeModification)
		}
	}

	// The bar still adds up.
	var total uint32
	for _, note := range notes {
		total += note.Duration
	}
	if total != 3_840 {
		t.Errorf("bar holds %d ticks, want 3840", total)
	}
}

func TestQuantizeChords_SnapsSymbolsToTheirOwnGrid(t *testing.T) {
	chords := []logicx.Chord{
		{Position: logicBarOneTick + 7, Duration: 1_910, Name: "C"},     // drifted off the beat
		{Position: logicBarOneTick + 1_925, Duration: 40, Name: "D"},    // too short to notate
		{Position: logicBarOneTick + 1_960, Duration: 1_880, Name: "E"}, // lands in D's slot
	}
	got := quantizeChords(chords, ticksPerQuarter)
	if len(got) != 2 {
		t.Fatalf("chords = %d, want 2: the third shares a slot with the second", len(got))
	}
	if got[0].Name != "C" || got[0].Position != logicBarOneTick || got[0].Duration != 1_920 {
		t.Errorf("first chord = %+v", got[0])
	}
	// The second keeps its slot, and grows to at least one grid unit.
	if got[1].Name != "D" || got[1].Position != logicBarOneTick+1_920 || got[1].Duration != ticksPerQuarter {
		t.Errorf("second chord = %+v", got[1])
	}
}

func TestPrintedTempo_RoundsForTheStaffOnly(t *testing.T) {
	for _, test := range []struct {
		bpm  float64
		want string
	}{
		{132.99989318847656, "133"},
		{114.98590087890625, "114.99"},
		{128.00379943847656, "128"},
		{60, "60"},
	} {
		if got := printedTempo(test.bpm); got != test.want {
			t.Errorf("printedTempo(%v) = %q, want %q", test.bpm, got, test.want)
		}
	}
}

func TestProjectSpelling_TakesItsSideFromTheKeyThenTheChords(t *testing.T) {
	const (
		flat    = uint8(1)
		natural = uint8(2)
		sharp   = uint8(3)
	)
	tests := []struct {
		name    string
		project logicx.ProjectData
		class   uint8
		want    uint8
	}{{
		name:    "sharp key spells black keys sharp",
		project: logicx.ProjectData{KeySignatures: []logicx.KeySignatureChange{{Fifths: 3, Minor: true}}},
		class:   10, want: sharp, // A#, not Bb
	}, {
		name:    "flat key spells black keys flat",
		project: logicx.ProjectData{KeySignatures: []logicx.KeySignatureChange{{Fifths: -2}}},
		class:   10, want: flat,
	}, {
		// Logic starts every project in C major, so an untouched key signature
		// says nothing and the chord track has to.
		name: "chords decide when the key is untouched",
		project: logicx.ProjectData{
			KeySignatures: []logicx.KeySignatureChange{{Fifths: 0}},
			ProjectChords: []logicx.Chord{
				{Name: "Bb", RootPitchClass: 10, RootSpelling: flat},
				{Name: "Gm", RootPitchClass: 7, RootSpelling: natural},
			},
		},
		class: 10, want: flat,
	}, {
		name: "a chord names its own pitch class against the side",
		project: logicx.ProjectData{
			ProjectChords: []logicx.Chord{
				{Name: "Bb", RootPitchClass: 10, RootSpelling: flat},
				{Name: "F#", RootPitchClass: 6, RootSpelling: sharp},
			},
		},
		class: 6, want: sharp,
	}, {
		name:    "white keys stay natural",
		project: logicx.ProjectData{KeySignatures: []logicx.KeySignatureChange{{Fifths: -4}}},
		class:   4, want: natural,
	}}
	for _, test := range tests {
		if got := projectSpelling(test.project)[test.class]; got != test.want {
			t.Errorf("%s: class %d spells %d, want %d", test.name, test.class, got, test.want)
		}
	}
}

func TestWriteMusicXML_StaffNotesFollowTheProjectSpelling(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{TimeSignature: [2]uint64{4, 4}},
		Project: logicx.ProjectData{
			Sequences: []logicx.MIDISequence{{Name: "Lead", Notes: []logicx.MIDINote{
				{Position: logicBarOneTick, Pitch: 70, Duration: 3_840}, // A#/Bb
			}}},
			ProjectChords: []logicx.Chord{{
				Position: logicBarOneTick, Duration: 3_840, Name: "Bb",
				RootPitchClass: 10, RootSpelling: 1, Pitches: []uint8{70, 74, 77},
			}},
		},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative, options{quantize: ticksPerQuarter / 4}); err != nil {
		t.Fatal(err)
	}
	if xml := output.String(); !strings.Contains(xml, "<step>B</step>") || strings.Contains(xml, "<step>A</step>") {
		t.Errorf("staff note is not spelled Bb:\n%s", xml)
	}
}
