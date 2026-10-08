// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-check tests the write path against Logic Pro itself.
//
// It writes edited copies of the test fixtures, one edit per copy, then opens
// each in Logic Pro, records any dialog Logic shows, takes a screenshot, has
// Logic save the project and closes it. Reading back what Logic saved shows
// whether the edit survived and which chunks Logic rewrote; the "unchanged"
// copy shows what Logic rewrites on every save.
//
//	go run ./internal/cmd/logicx-check -out /tmp/logicx-check
//
// It drives Logic through System Events, so the terminal needs Accessibility
// and Screen Recording permission. It refuses to start while Logic is running,
// so that no open project is touched, and quits Logic when every edit passed.
// It stops at the first failure, leaving Logic showing it. Popups about the
// audio output, which concern this Mac rather than the project, are
// confirmed; any other popup is a failure. -write only writes the copies,
// without opening Logic.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/loov/logicx"
)

const logicBundleID = "com.apple.logic10"

// edit is one change made to a copy of a fixture. apply changes the project
// and returns a check that the change is still present in a project read
// back after Logic saved it.
type edit struct {
	name    string
	fixture string
	apply   func(p *logicx.ProjectData) (check func(p logicx.ProjectData) error, err error)
}

var edits = []edit{
	{"unchanged", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		return func(logicx.ProjectData) error { return nil }, nil
	}},
	{"tempo-bpm", "tempo-map-steps", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		t := p.TempoChanges[1]
		t.BPM = 97.5
		return func(p logicx.ProjectData) error {
			return expect(slices.ContainsFunc(p.TempoChanges, func(c logicx.TempoChange) bool {
				return c.Position == t.Position && c.BPM == 97.5
			}), "no 97.5 BPM tempo at %d", t.Position)
		}, t.Save()
	}},
	{"tempo-delete", "tempo-map-steps", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		t := p.TempoChanges[1]
		return func(p logicx.ProjectData) error {
			return expect(!slices.ContainsFunc(p.TempoChanges, func(c logicx.TempoChange) bool { return c.Position == t.Position }),
				"tempo at %d still present", t.Position)
		}, t.Delete()
	}},
	{"tempo-duplicate", "tempo-map-steps", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		c, err := p.TempoChanges[0].Duplicate()
		if err != nil {
			return nil, err
		}
		c.Position, c.BPM = p.TempoChanges[0].Position+1920, 60
		return func(p logicx.ProjectData) error {
			return expect(slices.ContainsFunc(p.TempoChanges, func(t logicx.TempoChange) bool {
				return t.Position == c.Position && t.BPM == 60
			}), "no 60 BPM tempo at %d", c.Position)
		}, c.Save()
	}},
	{"note-transpose", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		n := p.Sequences[0].Notes[0]
		n.Pitch += 12
		return noteCheck(n), n.Save()
	}},
	{"note-move", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		n := p.Sequences[0].Notes[0]
		n.Pitch, n.SourcePosition, n.Position = 96, n.SourcePosition+960, n.Position+960
		return noteCheck(n), n.Save()
	}},
	{"note-duplicate", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		n, err := p.Sequences[0].Notes[0].Duplicate()
		if err != nil {
			return nil, err
		}
		n.Pitch, n.SourcePosition, n.Position = 96, n.SourcePosition+480, n.Position+480
		return noteCheck(n), n.Save()
	}},
	{"note-delete", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		count := len(p.Sequences[0].Notes)
		return func(p logicx.ProjectData) error {
			return expect(len(p.Sequences) > 0 && len(p.Sequences[0].Notes) == count-1, "notes = %d, want %d", len(p.Sequences[0].Notes), count-1)
		}, p.Sequences[0].Notes[0].Delete()
	}},
	{"lyric-long", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		l := p.Sequences[0].Notes[0].Lyrics[0]
		l.Text = "a lyric too long for one atom"
		return func(p logicx.ProjectData) error {
			return expect(slices.ContainsFunc(p.Sequences[0].Notes, func(n logicx.MIDINote) bool {
				return slices.ContainsFunc(n.Lyrics, func(got logicx.Lyric) bool { return got.Text == l.Text })
			}), "no lyric %q", l.Text)
		}, l.Save()
	}},
	{"articulation", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		a := p.Sequences[0].Notes[6].ScoreArticulations[0]
		a.Kind, a.Flipped = logicx.ScoreArticulationAccent, false
		return func(p logicx.ProjectData) error {
			got := p.Sequences[0].Notes[6].ScoreArticulations
			return expect(len(got) == 1 && got[0].Kind == logicx.ScoreArticulationAccent, "articulations = %+v", got)
		}, a.Save()
	}},
	{"ornament-fermata-arpeggio", "score-notation-native-logic", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		notes := p.Sequences[0].Notes
		o, f, a := notes[13].ScoreOrnaments[0], notes[6].ScoreFermatas[0], notes[30].ScoreArpeggios[0]
		o.Kind, f.Inverted, a.Direction = logicx.ScoreOrnamentMordent, true, logicx.ScoreArpeggioDirectionDown
		return func(p logicx.ProjectData) error {
			notes := p.Sequences[0].Notes
			return expect(notes[13].ScoreOrnaments[0].Kind == logicx.ScoreOrnamentMordent && notes[6].ScoreFermatas[0].Inverted &&
				notes[30].ScoreArpeggios[0].Direction == logicx.ScoreArpeggioDirectionDown,
				"ornament %+v, fermata %+v, arpeggio %+v", notes[13].ScoreOrnaments, notes[6].ScoreFermatas, notes[30].ScoreArpeggios)
		}, errors.Join(o.Save(), f.Save(), a.Save())
	}},
	{"chord-root-bass", "chords", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		c := p.ProjectChords[0]
		c.RootPitchClass, c.RootSpelling = 2, 2
		c.HasBass, c.BassPitchClass, c.BassSpelling = true, 6, 3
		return func(p logicx.ProjectData) error {
			got := p.ProjectChords[0]
			return expect(got.RootPitchClass == 2 && got.HasBass && got.BassPitchClass == 6, "chord = %s %+v", got.Name, got)
		}, c.Save()
	}},
	{"key-signature", "signature-map-key", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		k := p.KeySignatures[0]
		k.Fifths, k.Minor = -3, true
		return func(p logicx.ProjectData) error {
			got := p.KeySignatures[0]
			return expect(got.Fifths == -3 && got.Minor, "key = %+v", got)
		}, k.Save()
	}},
	{"meter", "signature-map-time", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		m := p.TimeSignatures[0]
		m.Numerator, m.Denominator = 6, 8
		return func(p logicx.ProjectData) error {
			got := p.TimeSignatures[0]
			return expect(got.Numerator == 6 && got.Denominator == 8, "meter = %+v", got)
		}, m.Save()
	}},
	{"beat-grouping", "signature-grouping-7-8", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		m := p.TimeSignatures[0]
		slices.Reverse(m.BeatGrouping)
		want := slices.Clone(m.BeatGrouping)
		return func(p logicx.ProjectData) error {
			got := p.TimeSignatures[0].BeatGrouping
			return expect(slices.Equal(got, want), "grouping = %v, want %v", got, want)
		}, m.Save()
	}},
	{"note-velocity", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		n := p.Sequences[0].Notes[0]
		n.Velocity = 37
		return func(p logicx.ProjectData) error {
			got := p.Sequences[0].Notes[0]
			return expect(got.Velocity == 37, "first note velocity %d, want 37", got.Velocity)
		}, n.Save()
	}},
	{"region-move-rename", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		s := p.Sequences[0]
		count, first := len(s.Notes), s.Notes[0].Position
		s.Name, s.Position = "Väike Lind", s.Position+3840
		return func(p logicx.ProjectData) error {
			got := p.Sequences[0]
			return expect(got.Name == s.Name && got.Position == s.Position && len(got.Notes) == count && got.Notes[0].Position == first+3840,
				"region %q at %d with %d notes, first at %d", got.Name, got.Position, len(got.Notes), got.Notes[0].Position)
		}, s.Save()
	}},
	{"region-loop", "musicxml-roundtrip", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		s := p.Sequences[0]
		count := len(s.Notes)
		s.Looped, s.Duration = true, 2*s.SourceDuration
		return func(p logicx.ProjectData) error {
			got := p.Sequences[0]
			return expect(got.Looped && got.Duration == s.Duration && len(got.Notes) == 2*count,
				"region looped %v for %d with %d notes", got.Looped, got.Duration, len(got.Notes))
		}, s.Save()
	}},
	{"marker-rename", "chords", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		m := p.Markers[0]
		m.Name = "Renamed Marker"
		return func(p logicx.ProjectData) error {
			return expect(p.Markers[0].Name == m.Name, "marker %q", p.Markers[0].Name)
		}, m.Save()
	}},
	{"track-plugin-names", "plugins", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		t, u := p.Tracks[0], p.AudioUnits[0]
		t.Name, u.Setting = "Renamed Track", "Renamed Setting"
		return func(p logicx.ProjectData) error {
			return expect(p.Tracks[0].Name == t.Name && p.AudioUnits[0].Setting == u.Setting,
				"track %q, setting %q", p.Tracks[0].Name, p.AudioUnits[0].Setting)
		}, errors.Join(t.Save(), u.Save())
	}},
	{"mixer", "mixer-base", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		volume, pan, mute, monitor, color := track(p, "Inst 1"), track(p, "Inst 2"), track(p, "Inst 3"), track(p, "Inst 4"), track(p, "Inst 5")
		volume.SetVolumeDB(-12)
		pan.Pan, mute.Mute, monitor.InputMonitoring = -30, true, true
		color.SetPaletteColor(1, 3)
		return func(p logicx.ProjectData) error {
			volume, pan, mute, monitor, color := track(&p, "Inst 1"), track(&p, "Inst 2"), track(&p, "Inst 3"), track(&p, "Inst 4"), track(&p, "Inst 5")
			row, column := color.PaletteColor()
			return expect(math.Abs(volume.VolumeDB()+12) < 0.01 && pan.Pan == -30 && mute.Mute && monitor.InputMonitoring && row == 1 && column == 3,
				"volume %.2f dB, pan %d, mute %v, monitoring %v, color at %d,%d",
				volume.VolumeDB(), pan.Pan, mute.Mute, monitor.InputMonitoring, row, column)
		}, errors.Join(volume.Save(), pan.Save(), mute.Save(), monitor.Save(), color.Save())
	}},
	{"send", "mixer-routing", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		s := &track(p, "Inst 5").Sends[0]
		s.Level, s.Pan, s.PreFader = track(p, "Inst 1").Volume/2, -10, true
		want := *s
		return func(p logicx.ProjectData) error {
			sends := track(&p, "Inst 5").Sends
			return expect(len(sends) == 2 && sends[0].Level == want.Level && sends[0].Pan == -10 && sends[0].PreFader && sends[0].Bus == 4,
				"sends %+v", sends)
		}, s.Save()
	}},
	{"automation-samples", "mixer-automation", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		// Drop the samples Logic keeps between automation points; Logic's
		// save shows whether it puts them back.
		for _, chunk := range p.Chunks {
			chunk.Events = slices.DeleteFunc(chunk.Events, func(e *logicx.Event) bool {
				return len(e.Data) >= 16 && e.Data[0] == 0x50 && e.Data[15]&0x40 != 0
			})
		}
		points := map[string]int{}
		for _, t := range p.Tracks {
			points[t.Name] = len(t.Automation)
		}
		return func(p logicx.ProjectData) error {
			for _, t := range p.Tracks {
				if n, ok := points[t.Name]; ok && len(t.Automation) != n {
					return fmt.Errorf("%s has %d automation points, want %d", t.Name, len(t.Automation), n)
				}
			}
			return nil
		}, nil
	}},
	{"automation-edit", "mixer-automation", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		moved := track(p, "Inst 1").Automation[1]
		moved.Position, moved.Value = 44160, track(p, "Inst 1").Automation[0].Value/2
		gone := track(p, "Inst 3").Automation[1]
		unmute, err := track(p, "Inst 4").Automation[2].Duplicate()
		if err != nil {
			return nil, err
		}
		unmute.Position, unmute.Value = 53760, 0
		return func(p logicx.ProjectData) error {
			volume, pan, mute := track(&p, "Inst 1").Automation, track(&p, "Inst 3").Automation, track(&p, "Inst 4").Automation
			return expect(len(volume) == 3 && volume[1].Position == moved.Position && volume[1].Value == moved.Value &&
				len(pan) == 2 && len(mute) == 4 && mute[3].Position == unmute.Position && mute[3].Value == 0,
				"volume %+v, pan %+v, mute %+v", volume, pan, mute)
		}, errors.Join(moved.Save(), gone.Delete(), unmute.Save())
	}},
	{"automation-curve", "mixer-curves", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		bend, flip, straighten := track(p, "Inst 1").Automation[0], track(p, "Inst 6").Automation[0], track(p, "Inst 8").Automation[0]
		bend.Curve = 80
		flip.Curve, flip.SCurve = -50, true
		straighten.Curve = 0
		return func(p logicx.ProjectData) error {
			bend, flip, straighten := track(&p, "Inst 1").Automation[0], track(&p, "Inst 6").Automation[0], track(&p, "Inst 8").Automation[0]
			return expect(bend.Curve == 80 && !bend.SCurve && flip.Curve == -50 && flip.SCurve && straighten.Curve == 0,
				"curves %d %v, %d %v, %d", bend.Curve, bend.SCurve, flip.Curve, flip.SCurve, straighten.Curve)
		}, errors.Join(bend.Save(), flip.Save(), straighten.Save())
	}},
	{"reroute", "mixer-routing", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		toStereo, toBus := track(p, "Inst 1"), track(p, "Inst 6")
		send := &track(p, "Inst 5").Sends[1]
		toStereo.Output, toBus.Output, send.Bus = 0, 4, 3
		return func(p logicx.ProjectData) error {
			sends := track(&p, "Inst 5").Sends
			return expect(track(&p, "Inst 1").Output == 0 && track(&p, "Inst 6").Output == 4 && len(sends) == 2 && sends[1].Bus == 3,
				"outputs %d and %d, sends %+v", track(&p, "Inst 1").Output, track(&p, "Inst 6").Output, sends)
		}, errors.Join(toStereo.Save(), toBus.Save(), send.Save())
	}},
	{"transport", "mixer-base", func(p *logicx.ProjectData) (func(logicx.ProjectData) error, error) {
		t := p.Transport
		if t == nil {
			return nil, errors.New("no transport")
		}
		t.Cycle, t.CycleStart, t.CycleEnd, t.End = true, 46080, 61440, 226560
		want := *t
		return func(p logicx.ProjectData) error {
			got := p.Transport
			return expect(got != nil && got.Cycle && got.CycleStart == want.CycleStart && got.CycleEnd == want.CycleEnd &&
				got.End == want.End, "transport %+v", got)
		}, t.Save()
	}},
}

// track returns p's track named name, or an empty one when there is none.
func track(p *logicx.ProjectData, name string) *logicx.Track {
	for i := range p.Tracks {
		if p.Tracks[i].Name == name {
			return &p.Tracks[i]
		}
	}
	return &logicx.Track{}
}

// noteCheck checks that a note with n's pitch sits at n's position.
func noteCheck(n logicx.MIDINote) func(p logicx.ProjectData) error {
	return func(p logicx.ProjectData) error {
		return expect(len(p.Sequences) > 0 && slices.ContainsFunc(p.Sequences[0].Notes, func(got logicx.MIDINote) bool {
			return got.Position == n.Position && got.Pitch == n.Pitch
		}), "no note %d at %d", n.Pitch, n.Position)
	}
}

func expect(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func main() {
	out := flag.String("out", "", "directory for the edited copies and screenshots; must not exist")
	writeOnly := flag.Bool("write", false, "only write the edited copies")
	only := flag.String("only", "", "comma-separated edits to run (default: all)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: logicx-check -out dir [-write] [-only name,...]")
		os.Exit(2)
	}
	if err := run(*out, *writeOnly, *only); err != nil {
		fmt.Fprintln(os.Stderr, "logicx-check:", err)
		os.Exit(1)
	}
}

func run(out string, writeOnly bool, only string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s already exists", out)
	}
	if !writeOnly {
		running, err := osascript(`application id "` + logicBundleID + `" is running`)
		if err != nil {
			return fmt.Errorf("ask whether Logic is running: %w", err)
		}
		if running == "true" {
			return errors.New("Logic Pro is running; quit it first so that no open project is touched")
		}
	}

	for _, e := range edits {
		if only != "" && !slices.Contains(strings.Split(only, ","), e.name) {
			continue
		}
		bundle := filepath.Join(out, e.name+".logicx")
		check, written, err := writeEdit(e, bundle)
		if err != nil {
			return fmt.Errorf("%s: %w", e.name, err)
		}
		if writeOnly {
			fmt.Printf("%-26s written to %s\n", e.name, bundle)
			continue
		}
		// Stop at the first failure, leaving Logic as it is to look at.
		if err := verify(e.name, bundle, written, check); err != nil {
			return fmt.Errorf("%s: %w", e.name, err)
		}
	}
	if !writeOnly {
		osascript(`tell application id "` + logicBundleID + `" to quit`)
	}
	return nil
}

// writeEdit copies the edit's fixture to bundle and applies the edit,
// returning its check and the ProjectData written.
func writeEdit(e edit, bundle string) (func(logicx.ProjectData) error, []byte, error) {
	if err := os.CopyFS(bundle, os.DirFS(filepath.Join("testdata", e.fixture+".logicx"))); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(bundle, "Alternatives", "000", "ProjectData")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	p, err := logicx.ParseProjectData(data)
	if err != nil {
		return nil, nil, err
	}
	check, err := e.apply(&p)
	if err != nil {
		return nil, nil, err
	}
	written, err := p.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}
	reread, err := logicx.ParseProjectData(written)
	if err != nil {
		return nil, nil, fmt.Errorf("reread before Logic: %w", err)
	}
	if err := check(reread); err != nil {
		return nil, nil, fmt.Errorf("edit missing before Logic: %w", err)
	}
	return check, written, os.WriteFile(path, written, 0o644)
}

// verify opens bundle in Logic, saves and closes it, and checks what Logic
// wrote.
func verify(name, bundle string, written []byte, check func(logicx.ProjectData) error) error {
	abs, err := filepath.Abs(bundle)
	if err != nil {
		return err
	}
	if out, err := exec.Command("open", "-b", logicBundleID, abs).CombinedOutput(); err != nil {
		return fmt.Errorf("open: %v: %s", err, out)
	}
	title := strings.TrimSuffix(filepath.Base(bundle), ".logicx")
	screenshot := strings.TrimSuffix(bundle, ".logicx") + ".png"
	isProject := func(w string) bool { return strings.Contains(w, title) }
	opened := waitFor(2*time.Minute, func() bool {
		dismissBenign(name, isProject)
		return slices.ContainsFunc(windowNames(), isProject)
	})
	// Give the project a moment to finish loading and any popup to appear.
	time.Sleep(5 * time.Second)
	if err := clearPopups(name, isProject, screenshot); err != nil {
		return err
	}
	if err := exec.Command("screencapture", "-x", screenshot).Run(); err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}
	if !opened {
		return fmt.Errorf("no project window after two minutes; windows %q, see %s", windowNames(), screenshot)
	}

	if err := clickMenu("File", "Save"); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	if err := clearPopups(name, isProject, screenshot); err != nil {
		return err
	}
	if err := clickMenu("File", "Close Project"); err != nil {
		return err
	}
	closed := waitFor(30*time.Second, func() bool {
		dismissBenign(name, isProject)
		return !slices.ContainsFunc(windowNames(), isProject)
	})
	if !closed {
		return fmt.Errorf("project did not close; popups %q", popups(isProject))
	}

	saved, err := os.ReadFile(filepath.Join(bundle, "Alternatives", "000", "ProjectData"))
	if err != nil {
		return err
	}
	p, err := logicx.ParseProjectData(saved)
	if err != nil {
		return fmt.Errorf("parse Logic's save: %w", err)
	}
	if err := check(p); err != nil {
		return fmt.Errorf("edit lost in Logic's save: %w", err)
	}
	fmt.Printf("%-26s ok; Logic rewrote %s\n", name, chunkDiff(written, saved))
	return nil
}

// chunkDiff summarizes which chunk types differ between two ProjectData files.
func chunkDiff(before, after []byte) string {
	if bytes.Equal(before, after) {
		return "nothing"
	}
	a, _ := logicx.ParseProjectData(before)
	b, _ := logicx.ParseProjectData(after)
	count := func(p logicx.ProjectData) map[string][][]byte {
		m := make(map[string][][]byte)
		for _, c := range p.Chunks {
			m[c.Type] = append(m[c.Type], c.Payload())
		}
		return m
	}
	ca, cb := count(a), count(b)
	var types []string
	for t := range ca {
		types = append(types, t)
	}
	for t := range cb {
		if _, ok := ca[t]; !ok {
			types = append(types, t)
		}
	}
	slices.Sort(types)
	var parts []string
	for _, t := range types {
		x, y := ca[t], cb[t]
		changed := 0
		for i := range min(len(x), len(y)) {
			if !bytes.Equal(x[i], y[i]) {
				changed++
			}
		}
		switch {
		case len(x) != len(y):
			parts = append(parts, fmt.Sprintf("%s %d→%d", t, len(x), len(y)))
		case changed > 0:
			parts = append(parts, fmt.Sprintf("%s %d/%d", t, changed, len(x)))
		}
	}
	if !bytes.Equal(a.Header[:], b.Header[:]) {
		parts = append(parts, "header")
	}
	return strings.Join(parts, ", ")
}

func waitFor(timeout time.Duration, done func() bool) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		if done() {
			return true
		}
	}
	return false
}

// logicProcess addresses Logic's process in System Events.
const logicProcess = `(first process whose bundle identifier is "` + logicBundleID + `")`

func windowNames() []string {
	out, err := osascript(`tell application "System Events" to get name of every window of ` + logicProcess)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, ", ")
}

// benignPopups are phrases of popups that are confirmed with Return rather
// than stopping the run: they concern this Mac's setup, not the project.
var benignPopups = []string{"audio output", "output device"}

// popups returns the text of each Logic window that is not a project window,
// from every element within it. Untitled windows count too, since alerts
// often have no title. Project windows are skipped: they are too large to
// read this way.
func popups(isProject func(string) bool) [][]string {
	var texts [][]string
	for i, name := range windowNames() {
		if isProject(name) {
			continue
		}
		out, err := osascript(fmt.Sprintf(`tell application "System Events" to tell %s
	set texts to {}
	repeat with e in (entire contents of window %d)
		try
			if role of e is "AXStaticText" then set end of texts to (value of e as text)
		end try
	end repeat
	set AppleScript's text item delimiters to " | "
	return (name of window %d as text) & " | " & (texts as text)
end tell`, logicProcess, i+1, i+1))
		if err != nil {
			out = name + " | " + err.Error()
		}
		texts = append(texts, strings.Split(out, " | "))
	}
	return texts
}

// dismissBenign confirms the popups that concern this Mac's setup, logging
// what they said, and reports whether any other popup is showing.
func dismissBenign(name string, isProject func(string) bool) (others [][]string) {
	for _, texts := range popups(isProject) {
		joined := strings.ToLower(strings.Join(texts, " "))
		if !slices.ContainsFunc(benignPopups, func(p string) bool { return strings.Contains(joined, p) }) {
			others = append(others, texts)
			continue
		}
		fmt.Printf("%-26s dismissed popup %q\n", name, texts)
		osascript(`tell application "System Events" to tell ` + logicProcess + `
	set frontmost to true
	keystroke return
end tell`)
		time.Sleep(time.Second)
	}
	return others
}

// clearPopups dismisses benign popups and fails on any other, taking a
// screenshot of it and leaving it showing.
func clearPopups(name string, isProject func(string) bool, screenshot string) error {
	others := dismissBenign(name, isProject)
	if len(others) == 0 {
		return nil
	}
	exec.Command("screencapture", "-x", screenshot).Run()
	return fmt.Errorf("Logic showed %q; left open, see %s", others, screenshot)
}

func clickMenu(menu, item string) error {
	_, err := osascript(fmt.Sprintf(`tell application "System Events" to tell %s
	set frontmost to true
	click menu item %q of menu 1 of menu bar item %q of menu bar 1
end tell`, logicProcess, item, menu))
	if err != nil {
		return fmt.Errorf("%s > %s: %w", menu, item, err)
	}
	return nil
}

func osascript(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
	}
	return string(bytes.TrimSpace(out)), nil
}
