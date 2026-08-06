// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/egonelbre/logicx"
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
	if err := writeMusicXML(&output, alternative); err != nil {
		t.Fatal(err)
	}
	xml := output.String()
	for _, want := range []string{
		`<score-partwise version="4.0">`, `<part-name>Trumpet</part-name>`, `<fifths>1</fifths>`,
		`<sound tempo="120"></sound>`, `<sound tempo="90"></sound>`, `<offset>1920</offset>`,
		`<chord></chord>`, `<tie type="start"></tie>`, `<tie type="stop"></tie>`,
		`<rehearsal>Chorus</rehearsal>`, `<beats>2+3</beats>`, `<beat-type>8</beat-type>`,
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
	if err := writeMusicXML(&output, alternative); err != nil {
		t.Fatal(err)
	}
	xml := output.String()
	if !strings.Contains(xml, "<part-name>Project Chords</part-name>") || !strings.Contains(xml, "<kind>major</kind>") || strings.Count(xml, "<note>") != 3 {
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
	sequences := scoreSequences(project)
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
	sequences := scoreSequences(project)
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
	sequences := scoreSequences(project)
	if len(sequences) != 1 || len(sequences[0].Notes) != 1 || sequences[0].Notes[0].Pitch != 62 {
		t.Fatalf("score sequences = %+v", sequences)
	}
}
