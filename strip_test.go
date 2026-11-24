// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestProjectData_AddTrackReproducesLogic(t *testing.T) {
	// Logic's New Track filled the spare Inst 7 below Inst 6.
	p := parseFixtureProject(t, "regions.logicx")
	logic := parseFixtureProject(t, "regions-more.logicx")
	added, err := p.AddTrack(trackNamed(t, &p, "Inst 6"), "")
	if err != nil {
		t.Fatal(err)
	}
	if added.Row != 7 {
		t.Errorf("added row %d, want 7", added.Row)
	}
	got := reparse(t, &p)
	mine, want := trackNamed(t, &got, "Inst 7"), trackNamed(t, &logic, "Inst 7")

	masked := func(b []byte, ranges ...[2]int) []byte {
		b = bytes.Clone(b)
		for _, r := range ranges {
			clear(b[r[0]:r[1]])
		}
		return b
	}
	n := len(want.chunk.Data)
	// Logic sets 0x08 at 0x50 on every strip when it saves.
	if g, w := masked(mine.chunk.Data, [2]int{0x50, 0x51}, [2]int{n - 48, n - 32}), masked(want.chunk.Data, [2]int{0x50, 0x51}, [2]int{n - 48, n - 32}); !bytes.Equal(g, w) {
		t.Errorf("strip\n got % x\nwant % x", g, w)
	}
	if !bytes.Equal(mine.chunk.Data[n-48:n-32], mine.environment.Data[len(mine.environment.Data)-16:]) {
		t.Error("strip and environment object IDs differ")
	}
	plugins := func(p ProjectData, strip uint16) [][]byte {
		var out [][]byte
		for _, c := range p.Chunks {
			if c.Type == pluginChunk && bytes.Equal(c.Header[4:8], pluginVariant) && binary.LittleEndian.Uint16(c.Header[14:]) == strip {
				out = append(out, c.Data)
			}
		}
		return out
	}
	mp, wp := plugins(got, mine.strip), plugins(logic, want.strip)
	if len(mp) != len(wp) {
		t.Fatalf("plug-ins %d, want %d", len(mp), len(wp))
	}
	for i := range wp {
		diff := 0
		for k := range wp[i] {
			if k < len(mp[i]) && mp[i][k] != wp[i][k] {
				diff++
			}
		}
		// Only the instance IDs differ, at most 16 bytes each.
		if len(mp[i]) != len(wp[i]) || diff > 32 {
			t.Errorf("plug-in %d: %d bytes differ, lengths %d and %d", i, diff, len(mp[i]), len(wp[i]))
		}
		if i == 2 && !bytes.Contains(mp[i], []byte{0x10, 0x70}) {
			t.Error("the archive still names the source's track")
		}
	}
	e := len(want.environment.Data)
	if g, w := masked(mine.environment.Data, [2]int{0x50, 0x51}, [2]int{e - 16, e}), masked(want.environment.Data, [2]int{0x50, 0x51}, [2]int{e - 16, e}); !bytes.Equal(g, w) {
		t.Errorf("environment object\n got % x\nwant % x", g, w)
	}
	if mine.trak == nil || mine.TrackName != "Deluxe Classic" {
		t.Errorf("new track has arrange track %v, name %q", mine.trak != nil, mine.TrackName)
	}

	named, err := got.AddTrack(trackNamed(t, &got, "Inst 7"), "Viola")
	if err != nil {
		t.Fatal(err)
	}
	again := reparse(t, &got)
	// An odd-length name keeps the strip number after it aligned.
	if v := trackNamed(t, &again, named.Track); v.TrackName != "Viola" || v.trak == nil || v.environment == nil {
		t.Errorf("named track %q: %q", named.Track, v.TrackName)
	}
	first := trackNamed(t, &again, "Inst 1")
	first.TrackName = "Flute"
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	if got := trackNamed(t, ptr(reparse(t, &again)), "Inst 1"); got.TrackName != "Flute" {
		t.Errorf("renamed track %q", got.TrackName)
	}
}

// With no spare strip left, AddTrack makes one in place of the click's
// strip, which moves along with every strip after it, as Logic does.
func TestProjectData_AddTrackMakesAStrip(t *testing.T) {
	p := parseFixtureProject(t, "chord-regions.logicx")
	if p.spareStrip() != nil {
		t.Fatal("fixture has a spare strip")
	}
	stripOf := func(p *ProjectData, name string) uint16 { return trackNamed(t, p, name).strip }
	click, master, out := stripOf(&p, "Inst 3"), stripOf(&p, "Master"), stripOf(&p, "Output 1-2")
	units := len(p.AudioUnits)

	added, err := p.AddTrack(trackNamed(t, &p, "Inst 2"), "Cello")
	if err != nil {
		t.Fatal(err)
	}
	got := reparse(t, &p)
	cello := trackNamed(t, &got, added.Track)
	if added.Track != "Inst 3" || cello.strip != click || cello.TrackName != "Cello" || cello.Instrument == nil {
		t.Fatalf("new track %q on strip %d named %q, want Inst 3 on %d", added.Track, cello.strip, cello.TrackName, click)
	}
	moved := trackNamed(t, &got, "Inst 4")
	if moved.strip != click+1 || moved.TrackName != "Click" || moved.Instrument == nil || moved.Instrument.Name != "Klopfgeist" {
		t.Errorf("click strip %d named %q, want %d", moved.strip, moved.TrackName, click+1)
	}
	if stripOf(&got, "Master") != master+1 || stripOf(&got, "Output 1-2") != out+1 {
		t.Errorf("master %d, output %d, want %d, %d", stripOf(&got, "Master"), stripOf(&got, "Output 1-2"), master+1, out+1)
	}
	for _, c := range got.Chunks {
		if c.Type == environmentChunk && chunkSequenceID(c).group == environmentGroup && c.Data[environmentKind] == 0x11 {
			var tr *Track
			for i := range got.Tracks {
				if got.Tracks[i].environment == c {
					tr = &got.Tracks[i]
				}
			}
			if strip, ok := environmentStrip(c.Data); ok && tr != nil && strip != tr.strip {
				t.Errorf("environment object of %q names strip %d, want %d", tr.Name, strip, tr.strip)
			}
		}
	}
	if len(got.AudioUnits) <= units {
		t.Errorf("plug-ins %d, want more than %d", len(got.AudioUnits), units)
	}
	if a := got.ArrangeTracks[added.Row-1]; a.Track != "Inst 3" || a.chunk.Data[arrangeTrackSelectedAlso]&0x40 == 0 {
		t.Errorf("arrange track %+v", a)
	}
}
