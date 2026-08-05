// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
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
					{Position: logicBarOneTick, Pitch: 67, Duration: 720},
					{Position: logicBarOneTick, Pitch: 71, Duration: 720},
					{Position: logicBarOneTick + 3_600, Pitch: 74, Duration: 480},
				},
			}},
			Markers: []logicx.Marker{{Position: logicBarOneTick, Name: "Chorus"}},
		},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative); err != nil {
		t.Fatal(err)
	}
	xml := output.String()
	for _, want := range []string{
		`<score-partwise version="4.0">`, `<part-name>Trumpet</part-name>`, `<fifths>1</fifths>`,
		`<sound tempo="120"></sound>`, `<chord></chord>`, `<tie type="start"></tie>`, `<tie type="stop"></tie>`,
		`<rehearsal>Chorus</rehearsal>`,
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
		}}},
	}
	var output bytes.Buffer
	if err := writeMusicXML(&output, alternative); err != nil {
		t.Fatal(err)
	}
	xml := output.String()
	if !strings.Contains(xml, "<part-name>Project Chords</part-name>") || !strings.Contains(xml, "<words>C</words>") || strings.Count(xml, "<note>") != 3 {
		t.Fatalf("project chord staff missing:\n%s", xml)
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
