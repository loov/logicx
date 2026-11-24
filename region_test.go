// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"encoding/binary"
	"testing"
)

func TestProjectData_CopyRegionOwnsItsSource(t *testing.T) {
	p := parseFixtureProject(t, "tracks-region-base.logicx")
	if _, err := p.DuplicateArrangeTrack(&p.ArrangeTracks[2]); err != nil {
		t.Fatal(err)
	}
	p.Refresh()
	original := regionOn(t, &p, "Inst 3")
	copied, err := p.CopyRegion(original)
	if err != nil {
		t.Fatal(err)
	}
	if copied.SequenceID == original.SequenceID || len(copied.Notes) != len(original.Notes) {
		t.Fatalf("copy %d with %d notes, original %d with %d", copied.SequenceID, len(copied.Notes), original.SequenceID, len(original.Notes))
	}
	copied.Name, copied.Position, copied.Row = "Copy", original.Position+3840, original.Row+1
	if err := copied.Save(); err != nil {
		t.Fatal(err)
	}
	note := copied.Notes[0]
	note.Pitch = 72
	if err := note.Save(); err != nil {
		t.Fatal(err)
	}

	got := reparse(t, &p)
	if len(got.Sequences) != 2 {
		t.Fatalf("regions = %d, want 2", len(got.Sequences))
	}
	a, b := got.Sequences[0], got.Sequences[1]
	if a.Name != original.Name || a.Notes[0].Pitch != original.Notes[0].Pitch || a.Row != original.Row {
		t.Errorf("original became %q row %d pitch %d", a.Name, a.Row, a.Notes[0].Pitch)
	}
	if b.Name != "Copy" || b.Position != original.Position+3840 || b.Row != original.Row+1 || b.Notes[0].Pitch != 72 ||
		b.Notes[0].Position != original.Notes[0].Position+3840 {
		t.Errorf("copy = %q at %d row %d, note %d at %d", b.Name, b.Position, b.Row, b.Notes[0].Pitch, b.Notes[0].Position)
	}
	tags := map[uint32]bool{}
	for _, c := range got.Chunks {
		if c.Type == "MSeq" {
			tag := binary.LittleEndian.Uint32(c.Data[sequenceTag:])
			if tags[tag] {
				t.Errorf("sequence tag %#x used twice", tag)
			}
			tags[tag] = true
		}
	}

	if err := got.DeleteRegion(&got.Sequences[1]); err != nil {
		t.Fatal(err)
	}
	again := reparse(t, &got)
	if len(again.Sequences) != 1 || len(again.Chunks) != len(got.Chunks) {
		t.Errorf("after delete: %d regions, %d chunks, want 1 and %d", len(again.Sequences), len(again.Chunks), len(got.Chunks))
	}
	for _, c := range again.Chunks {
		if chunkSequenceID(c) == (chordSequenceID{arrangeTrackGroup, b.SequenceID}) {
			t.Errorf("deleted region's %s chunk remains", c.Type)
		}
	}
}

func TestChord_SaveMovesItsGroup(t *testing.T) {
	p := parseFixtureProject(t, "chord-regions.logicx")
	first := p.ProjectChords[0]
	second, err := first.Duplicate()
	if err != nil {
		t.Fatal(err)
	}
	if first.GroupDuration == 0 || second.GroupDuration != 0 {
		t.Fatalf("group durations %d, %d", first.GroupDuration, second.GroupDuration)
	}
	second.SourcePosition += 960
	second.RootPitchClass = 7
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}
	first.Position += 1920
	first.GroupDuration = 9600
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &p)
	var group []Chord
	for _, c := range got.ProjectChords {
		if c.SequenceID == first.SequenceID {
			group = append(group, c)
		}
	}
	if len(group) != 2 || group[0].Position != first.Position || group[1].Position != first.Position+960 ||
		group[1].RootPitchClass != 7 || group[1].Duration != 9600-960 || group[0].GroupDuration != 9600 {
		t.Errorf("group = %+v", group)
	}
}
