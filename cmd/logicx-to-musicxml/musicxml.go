// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"cmp"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/loov/logicx"
)

const (
	// ticksPerQuarter is Logic's tick resolution, used directly as the MusicXML
	// divisions value so durations need no rescaling.
	ticksPerQuarter = uint32(960)
	// logicBarOneTick is the tick position of bar 1 in a Logic project.
	logicBarOneTick = uint32(38_400)
	// maxMeasures bounds the bar grid. A tick position is a 32-bit field, so
	// corrupt data can ask for millions of bars; no real score needs this many.
	maxMeasures = 100_000
)

// options are the export choices the command line offers.
type options struct {
	realizeChords bool
	quantize      uint32 // coarsest notation grid, in ticks; zero leaves timing alone
	quantizeChord uint32 // grid chord symbols land on, in ticks; zero leaves them alone
	noTriplets    bool   // write thirds of a beat on the straight grid instead
}

// writeMusicXML renders one project alternative as a MusicXML 4.0 partwise
// score. Every sequence becomes a part; global markers and the tempo map are
// attached to the first part only, as MusicXML expects.
func writeMusicXML(w io.Writer, alternative logicx.Alternative, opts options) error {
	sequences := scoreSequences(alternative.Project, opts)
	if len(sequences) == 0 {
		return errors.New("no MIDI notes or chords found")
	}

	origin := scoreOrigin(alternative, sequences)

	score := xmlScore{Version: "4.0"}
	for i, sequence := range sequences {
		id := "P" + strconv.Itoa(i+1)
		score.PartList.Parts = append(score.PartList.Parts, xmlScorePart{ID: id, Name: sequence.Name})
		var tempos []logicx.TempoChange
		if i == 0 {
			tempos = alternative.Project.TempoChanges
		}
		score.Parts = append(score.Parts, makePart(
			id, sequence.MIDISequence, sequence.slash,
			alternative.Project.Markers, i == 0, tempos, alternative.Project.TimeSignatures,
			alternative.Project.KeySignatures, alternative.Metadata, origin, opts,
			projectSpelling(alternative.Project),
		))
	}

	// MuseScore refuses to open a score whose parts run to different lengths,
	// so short parts get trailing measures of rest.
	var longest []xmlMeasure
	for _, part := range score.Parts {
		if len(part.Measures) > len(longest) {
			longest = part.Measures
		}
	}
	for i := range score.Parts {
		for len(score.Parts[i].Measures) < len(longest) {
			bar := longest[len(score.Parts[i].Measures)]
			score.Parts[i].Measures = append(score.Parts[i].Measures, restMeasure(len(score.Parts[i].Measures)+1, bar.length))
		}
		last := &score.Parts[i].Measures[len(score.Parts[i].Measures)-1]
		last.Barline = &xmlBarline{Location: "right", Style: "light-heavy"}
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(score); err != nil {
		return err
	}
	return encoder.Flush()
}

// restMeasure is a bar of silence, used to pad a part out to the score length.
func restMeasure(number int, length uint32) xmlMeasure {
	return xmlMeasure{Number: number, length: length, Items: measureItems(nil, length, beatGrid{}, 1)}
}

// scoreOrigin is the tick that bar one starts on: Logic's own bar one, unless
// the project holds something even earlier.
func scoreOrigin(alternative logicx.Alternative, sequences []scorePart) uint32 {
	origin := logicBarOneTick
	for _, sequence := range sequences {
		for _, note := range sequence.Notes {
			origin = min(origin, note.Position)
		}
		for _, chord := range sequence.Chords {
			origin = min(origin, chord.Position)
		}
	}
	for _, marker := range alternative.Project.Markers {
		origin = min(origin, marker.Position)
	}
	for _, tempo := range alternative.Project.TempoChanges {
		origin = min(origin, tempo.Position)
	}
	for _, signature := range alternative.Project.TimeSignatures {
		origin = min(origin, signature.Position)
	}
	for _, signature := range alternative.Project.KeySignatures {
		origin = min(origin, signature.Position)
	}
	return snap(uint64(origin), shortestNote)
}

// scorePart is a sequence together with how its staff is written. A staff with
// no recorded notes shows its chords, either realized into pitches or as one
// rhythm slash per beat.
type scorePart struct {
	logicx.MIDISequence
	slash bool
}

// scoreSequences prepares the parts to export. Sequences without notes are
// voiced from their chords, and the project chord track either joins a part
// that already plays it or becomes a part of its own.
func scoreSequences(project logicx.ProjectData, opts options) []scorePart {
	merged := mergeSequences(project.Sequences, project.Tracks)
	sequences := make([]scorePart, len(merged))
	for i, sequence := range merged {
		sequence.Chords = quantizeChords(sequence.Chords, opts.quantizeChord)
		sequences[i] = scorePart{MIDISequence: sequence}
		if len(sequence.Notes) != 0 {
			continue
		}
		if opts.realizeChords {
			sequences[i].Notes = chordNotes(sequence.Chords)
		} else {
			sequences[i].slash = true
		}
	}
	projectChords := quantizeChords(project.ProjectChords, opts.quantizeChord)
	if len(projectChords) == 0 {
		return sequences
	}
	if i := chordStaff(sequences, projectChords); i >= 0 {
		sequences[i].Chords = append(sequences[i].Chords, projectChords...)
		return sequences
	}
	chords := scorePart{MIDISequence: logicx.MIDISequence{Name: "Project Chords", Chords: projectChords}}
	if opts.realizeChords {
		chords.Notes = chordNotes(chords.Chords)
	} else {
		chords.slash = true
	}
	return append(sequences, chords)
}

// mergeSequences combines the regions of a track into one part named after the
// track as the Tracks area shows it. A region whose track is unknown is grouped
// by its own name instead. Tracks are told apart by name, so two sharing one
// merge.
func mergeSequences(sequences []logicx.MIDISequence, tracks []logicx.Track) []logicx.MIDISequence {
	trackNames := make(map[string]string)
	for _, track := range tracks {
		trackNames[track.Name] = cmp.Or(track.TrackName, track.Name)
	}
	indexes := make(map[string]int)
	var merged []logicx.MIDISequence
	for _, sequence := range sequences {
		name := cmp.Or(trackNames[sequence.Track], sequence.Track, sequence.Name)
		i, ok := indexes[name]
		if !ok {
			i = len(merged)
			indexes[name] = i
			merged = append(merged, logicx.MIDISequence{Name: name, Track: sequence.Track})
		}
		merged[i].Notes = append(merged[i].Notes, sequence.Notes...)
		merged[i].Chords = append(merged[i].Chords, sequence.Chords...)
	}
	return merged
}

// noteKey identifies a sounding note, used to compare chord voicings against
// notes that a part already contains.
type noteKey struct {
	position uint32
	fraction uint16
	pitch    uint8
}

// chordNotes voices chords as notes so that a chords-only part still renders
// on a staff. Duplicate pitches within a chord are emitted once.
func chordNotes(chords []logicx.Chord) []logicx.MIDINote {
	var notes []logicx.MIDINote
	seen := make(map[noteKey]bool)
	for i, chord := range chords {
		duration := chord.Duration
		if duration == 0 {
			// ponytail: absent chord lengths extend to the next chord or one
			// quarter; remove this fallback once the chord decoder proves length.
			duration = ticksPerQuarter
		}
		// Logic lets a chord run past the next one. On a staff that would be two
		// overlapping voice-1 chords, which MuseScore refuses to import, so the
		// chord stops where its successor begins.
		if i+1 < len(chords) && chords[i+1].Position > chord.Position {
			duration = min(duration, chords[i+1].Position-chord.Position)
		}
		for _, pitch := range chord.Pitches {
			key := noteKey{chord.Position, chord.PositionFraction, pitch}
			if seen[key] {
				continue
			}
			seen[key] = true
			notes = append(notes, logicx.MIDINote{
				Position: chord.Position, PositionFraction: chord.PositionFraction,
				Pitch: pitch, Duration: duration,
			})
		}
	}
	return notes
}

// quantizeChords snaps chord symbols onto their own grid, which is coarser
// than the note grid: a chord change is heard on the beat even when the playing
// drifts, and two symbols a few ticks apart read as one chord written twice.
func quantizeChords(chords []logicx.Chord, grid uint32) []logicx.Chord {
	if len(chords) == 0 || grid == 0 {
		return chords
	}
	quantized := make([]logicx.Chord, 0, len(chords))
	for _, chord := range chords {
		start := snap(uint64(chord.Position), grid)
		end := max(snap(uint64(chord.Position)+uint64(chord.Duration), grid), start+grid)
		if len(quantized) > 0 {
			previous := &quantized[len(quantized)-1]
			// Two chords in one slot cannot both be shown; the first holds it.
			if start <= previous.Position {
				continue
			}
			if previous.Position+previous.Duration > start {
				previous.Duration = start - previous.Position
			}
		}
		chord.Position, chord.Duration = start, end-start
		quantized = append(quantized, chord)
	}
	return quantized
}

// chordStaff returns the index of the part that already plays every chord
// tone, or -1 when no part does. Such a part shows the chord symbols instead
// of a duplicate chord staff.
func chordStaff(sequences []scorePart, chords []logicx.Chord) int {
	var required []noteKey
	for _, chord := range chords {
		for _, pitch := range chord.Pitches {
			required = append(required, noteKey{chord.Position, chord.PositionFraction, pitch})
		}
	}
	if len(required) == 0 {
		return -1
	}
	for i, sequence := range sequences {
		present := make(map[noteKey]bool, len(sequence.Notes))
		for _, note := range sequence.Notes {
			present[noteKey{note.Position, note.PositionFraction, note.Pitch}] = true
		}
		all := true
		for _, key := range required {
			if !present[key] {
				all = false
				break
			}
		}
		if all {
			return i
		}
	}
	return -1
}

// shortestNote is the finest value the exporter notates, a 64th note.
const shortestNote = 960 / 16 // ticksPerQuarter / 16, kept untyped

// snap rounds a tick value onto a grid. Logic stores raw performance timing —
// audio transcriptions in particular — and notation cannot render off-grid
// positions or durations at all.
func snap(ticks uint64, grid uint32) uint32 {
	limit := uint64(^uint32(0)) / uint64(grid) * uint64(grid)
	return uint32(min((ticks+uint64(grid)/2)/uint64(grid)*uint64(grid), limit))
}

// noteGrid halves the grid until it is fine enough for span, the tightest
// spacing around a note. A fast run quantized on the coarse grid would collapse
// onto one beat, so it earns a finer one.
func noteGrid(span uint64, coarsest uint32) uint32 {
	grid := coarsest
	for grid > shortestNote && span < uint64(grid) {
		grid /= 2
	}
	return grid
}

// notatable returns the longest prefix of a grid-aligned duration that maps to
// a single note value, optionally dotted or double-dotted. Anything else has
// to become a tied chain: MuseScore refuses to open a score containing a note
// it cannot render, such as Melodyne's raw 1200-tick notes.
func notatable(duration uint32) uint32 {
	for value := ticksPerQuarter * 8; value >= shortestNote; value /= 2 {
		for _, dotted := range [...]uint32{value * 7 / 4, value * 3 / 2, value} {
			if dotted <= duration {
				return dotted
			}
		}
	}
	return duration
}

// beatGrid describes the beats of one stretch of music and which of them hold
// a triplet. Beat starts are measured from whatever the caller anchors them to:
// project ticks while quantizing, measure ticks while writing a measure.
type beatGrid struct {
	beat    uint32
	triplet map[uint32]bool
}

// at returns the beat containing position and whether it is a triplet.
func (g beatGrid) at(position uint32) (start uint32, triplet bool) {
	if g.beat == 0 {
		return 0, false
	}
	start = position / g.beat * g.beat
	return start, g.triplet[start]
}

// nextTriplet returns the first triplet beat starting after position and before
// limit, so a note can be cut where a tuplet begins.
func (g beatGrid) nextTriplet(position, limit uint32) (uint32, bool) {
	if g.beat == 0 {
		return 0, false
	}
	for start := position/g.beat*g.beat + g.beat; start < limit; start += g.beat {
		if g.triplet[start] {
			return start, true
		}
	}
	return 0, false
}

// offGrid is how far a position sits from the nearest line of a grid anchored
// at anchor.
func offGrid(position, anchor, grid uint32) uint64 {
	within := uint64(position - anchor)
	return min(within%uint64(grid), uint64(grid)-within%uint64(grid))
}

// findTripletBeats picks out the beats whose onsets sit closer to a third of a
// beat than to the straight grid. Logic records what was played, so a triplet
// arrives as three notes no straight grid can express, and quantizing them
// straight collapses two of them onto one line.
func findTripletBeats(notes []logicx.MIDINote, measures *measureMap, coarsest uint32) beatGrid {
	grid := beatGrid{triplet: map[uint32]bool{}}
	onsets := make(map[uint32][]uint32)
	for _, note := range notes {
		if note.Duration == 0 {
			continue
		}
		measure, within := measures.locate(note.Position)
		beat := measures.beats[measure]
		grid.beat = beat
		start := measures.starts[measure] + within/beat*beat
		if last := onsets[start]; len(last) == 0 || last[len(last)-1] != note.Position {
			onsets[start] = append(onsets[start], note.Position)
		}
	}
	for start, positions := range onsets {
		measure, _ := measures.locate(start)
		beat := measures.beats[measure]
		slot := beat / 3
		// One stray onset is more likely a late note than a triplet, and a
		// slot finer than the shortest note cannot be written down.
		if len(positions) < 2 || slot < shortestNote || beat%3 != 0 {
			continue
		}
		var straight, thirds uint64
		for _, position := range positions {
			straight += offGrid(position, start, coarsest)
			thirds += offGrid(position, start, slot)
		}
		if straight > 0 && thirds*2 < straight {
			grid.triplet[start] = true
		}
	}
	return grid
}

// quantizeNotes snaps a sequence onto the notation grid. Notes that sound
// together share an onset and so share a grid, and a note that ended before the
// next one began still does, so quantizing never invents an overlap. Onsets
// inside a triplet beat snap to thirds of that beat instead.
func quantizeNotes(notes []logicx.MIDINote, coarsest uint32, triplets beatGrid) []logicx.MIDINote {
	if len(notes) == 0 {
		return nil
	}
	quantized := slices.Clone(notes)
	slices.SortFunc(quantized, func(a, b logicx.MIDINote) int { return cmp.Compare(a.Position, b.Position) })

	// Onsets, with the shortest note sounding at each.
	var onsets, shortest []uint32
	for _, note := range quantized {
		if len(onsets) == 0 || onsets[len(onsets)-1] != note.Position {
			onsets = append(onsets, note.Position)
			shortest = append(shortest, note.Duration)
			continue
		}
		shortest[len(shortest)-1] = min(shortest[len(shortest)-1], note.Duration)
	}

	// gridAt returns the grid a tick quantizes on, and what it is anchored to.
	gridAt := func(position uint32, span uint64) (grid, anchor uint32) {
		if start, ok := triplets.at(position); ok {
			return triplets.beat / 3, start
		}
		return noteGrid(span, coarsest), 0
	}
	snapIn := func(position, grid, anchor uint32) uint32 {
		return anchor + snap(uint64(position-anchor), grid)
	}

	grids := make([]uint32, len(onsets))
	anchors := make([]uint32, len(onsets))
	starts := make([]uint32, len(onsets))
	for i, onset := range onsets {
		span := uint64(shortest[i])
		if i > 0 {
			span = min(span, uint64(onset)-uint64(onsets[i-1]))
		}
		if i+1 < len(onsets) {
			span = min(span, uint64(onsets[i+1])-uint64(onset))
		}
		grids[i], anchors[i] = gridAt(onset, span)
		starts[i] = snapIn(onset, grids[i], anchors[i])
	}

	at := 0
	for i := range quantized {
		note := &quantized[i]
		for onsets[at] != note.Position {
			at++
		}
		grid, start := grids[at], starts[at]
		// The end quantizes on the grid of the beat it falls in, which is not
		// always the beat the note began in.
		stop := uint64(note.Position) + uint64(note.Duration)
		endGrid, endAnchor := grid, anchors[at]
		if endStart, ok := triplets.at(uint32(min(stop, math.MaxUint32))); ok {
			endGrid, endAnchor = triplets.beat/3, endStart
		} else if anchors[at] != 0 {
			endGrid, endAnchor = noteGrid(uint64(note.Duration), coarsest), 0
		}
		end := max(snapIn(uint32(min(stop, math.MaxUint32)), endGrid, endAnchor), start+endGrid)
		if at+1 < len(onsets) && stop <= uint64(onsets[at+1]) {
			end = min(end, max(starts[at+1], start+endGrid))
		}
		note.Position, note.Duration = start, end-start
	}
	return quantized
}

// slashPitch is where a rhythm slash sits on the staff, the middle line of a
// treble staff.
const slashPitch = 71

// slashNotes writes one rhythm slash per beat for as long as the chords last,
// so the staff reads as a strumming pattern under the chord symbols instead of
// a realized voicing. Slashes sit on the beat grid, so a chord that starts or
// ends off the beat still fills whole beats.
func slashNotes(chords []logicx.Chord, measures *measureMap) []logicx.MIDINote {
	var notes []logicx.MIDINote
	var written uint64 // end of the last slash, so chords never double up
	for _, chord := range chords {
		end := uint64(chord.Position) + uint64(chord.Duration)
		for position := uint64(chord.Position); position < end; {
			measure, within := measures.locate(uint32(position))
			beat := uint64(measures.beats[measure])
			start := uint64(measures.starts[measure]) + uint64(within)/beat*beat
			if start >= written {
				notes = append(notes, logicx.MIDINote{
					Position: uint32(start), Pitch: slashPitch, Duration: uint32(beat),
				})
				written = start + beat
			}
			position = start + beat
		}
	}
	return notes
}

// noteSegment is the part of a note that falls inside one measure. Notes
// crossing a barline become several tied segments.
type noteSegment struct {
	Start, Duration    uint32
	Pitch              uint8
	Step               string
	Alter              *int
	Lyrics             []logicx.Lyric
	ScoreArticulations []logicx.ScoreArticulation
	ScoreFermatas      []logicx.ScoreFermata
	ScoreOrnaments     []logicx.ScoreOrnament
	ScoreArpeggios     []logicx.ScoreArpeggio
	ScoreSlurs         []logicx.ScoreSlur
	TieStart, TieEnd   bool
	Slash              bool
	Channel            int // rank of the note's MIDI channel within the part
	Voice              int
}

// measureMap converts tick positions into measure numbers, growing the bar
// grid on demand and applying meter changes as it goes.
type measureMap struct {
	numerator   uint64
	denominator uint64
	changes     []logicx.TimeSignatureChange
	starts      []uint32
	durations   []uint32
	beats       []uint32
}

// newMeasureMap builds a bar grid starting at origin, taking the initial meter
// from the last signature change at or before origin, else from the metadata.
func newMeasureMap(origin uint32, metadata logicx.Metadata, changes []logicx.TimeSignatureChange) *measureMap {
	changes = slices.Clone(changes)
	slices.SortFunc(changes, func(a, b logicx.TimeSignatureChange) int { return cmp.Compare(a.Position, b.Position) })
	numerator, denominator := metadata.TimeSignature[0], metadata.TimeSignature[1]
	if numerator == 0 || denominator == 0 {
		numerator, denominator = 4, 4
	}
	for _, change := range changes {
		if change.Position <= origin {
			numerator, denominator = uint64(change.Numerator), uint64(change.Denominator)
		}
	}
	m := &measureMap{
		numerator: numerator, denominator: denominator,
		changes: changes, starts: []uint32{origin},
	}
	m.durations = append(m.durations, meterTicks(numerator, denominator))
	m.beats = append(m.beats, beatTicks(denominator))
	return m
}

// beatTicks returns the length of one beat, the note value the meter counts in.
func beatTicks(denominator uint64) uint32 {
	if denominator == 0 || denominator > uint64(ticksPerQuarter)*4 {
		return ticksPerQuarter
	}
	if ticks := uint32(uint64(ticksPerQuarter) * 4 / denominator); ticks != 0 {
		return ticks
	}
	return ticksPerQuarter
}

// meterTicks returns the length of one bar, falling back to 4/4 for meters
// that are absent or too large to represent.
func meterTicks(numerator, denominator uint64) uint32 {
	if numerator == 0 || denominator == 0 {
		return 4 * ticksPerQuarter
	}
	const quarterScale = uint64(ticksPerQuarter) * 4
	if numerator > uint64(^uint32(0))/quarterScale {
		return 4 * ticksPerQuarter
	}
	ticks := quarterScale * numerator / denominator
	if ticks == 0 || ticks > uint64(^uint32(0)) {
		return 4 * ticksPerQuarter
	}
	return uint32(ticks)
}

// locate returns the measure index containing position and the tick offset
// within it. Positions outside the grid clamp to its first or last measure.
func (m *measureMap) locate(position uint32) (int, uint32) {
	if position < m.starts[0] {
		return 0, 0
	}
	for len(m.starts) < maxMeasures &&
		uint64(position) >= uint64(m.starts[len(m.starts)-1])+uint64(m.durations[len(m.durations)-1]) {
		start := m.starts[len(m.starts)-1] + m.durations[len(m.durations)-1]
		numerator, denominator := m.numerator, m.denominator
		for _, change := range m.changes {
			if change.Position > start {
				break
			}
			numerator, denominator = uint64(change.Numerator), uint64(change.Denominator)
		}
		m.starts = append(m.starts, start)
		m.durations = append(m.durations, meterTicks(numerator, denominator))
		m.beats = append(m.beats, beatTicks(denominator))
	}
	measure := len(m.starts) - 1
	for measure > 0 && position < m.starts[measure] {
		measure--
	}
	return measure, min(position-m.starts[measure], m.durations[measure]-1)
}

// makePart lays a sequence out over the bar grid. Notes are split at barlines
// and tied; markers, tempos, chord symbols and signature changes are attached
// to the measures they fall in.
func makePart(
	id string,
	sequence logicx.MIDISequence,
	slash bool,
	markers []logicx.Marker,
	primary bool,
	tempos []logicx.TempoChange,
	timeSignatures []logicx.TimeSignatureChange,
	keySignatures []logicx.KeySignatureChange,
	metadata logicx.Metadata,
	origin uint32,
	opts options,
	spelling pitchSpelling,
) xmlPart {
	measures := newMeasureMap(origin, metadata, timeSignatures)
	// Triplets are found by how far the onsets sit off the straight grid, so
	// there is nothing to find, and nothing to snap them to, without one.
	var triplets beatGrid
	notes := sequence.Notes
	if opts.quantize != 0 {
		if !opts.noTriplets {
			triplets = findTripletBeats(sequence.Notes, measures, opts.quantize)
		}
		notes = quantizeNotes(sequence.Notes, opts.quantize, triplets)
	}
	if slash {
		notes, triplets = slashNotes(sequence.Chords, measures), beatGrid{}
	}
	// Each MIDI channel in the part gets its own voice, numbered by channel.
	var channels []uint8
	for _, note := range notes {
		if !slices.Contains(channels, note.Channel) {
			channels = append(channels, note.Channel)
		}
	}
	slices.Sort(channels)
	var byMeasure [][]noteSegment
	var gridByMeasure []beatGrid
	for _, note := range notes {
		if note.Duration == 0 {
			continue
		}
		// ponytail: sub-tick position fractions are dropped; use a higher
		// divisions value if real projects need them.
		position := note.Position
		remaining := max(note.Duration, shortestNote)
		first := true
		for remaining > 0 {
			measure, within := measures.locate(position)
			for len(byMeasure) <= measure {
				byMeasure = append(byMeasure, nil)
			}
			duration := min(remaining, measures.durations[measure]-within)
			// A tuplet is written inside its own beat, so nothing may straddle
			// the edge of one.
			beatStart, inTriplet := triplets.at(position)
			switch next, ok := triplets.nextTriplet(position, position+duration); {
			case inTriplet:
				duration = min(duration, beatStart+triplets.beat-position)
			case ok:
				duration = min(duration, next-position)
			}
			if !inTriplet {
				duration = notatable(duration)
			}
			step, alter, _ := harmonyPitch(note.Pitch%12, spelling[note.Pitch%12])
			byMeasure[measure] = append(byMeasure[measure], noteSegment{
				Start: within, Duration: duration, Pitch: note.Pitch,
				Step: step, Alter: alter,
				Lyrics:             note.Lyrics,
				ScoreArticulations: note.ScoreArticulations,
				ScoreFermatas:      note.ScoreFermatas,
				ScoreOrnaments:     note.ScoreOrnaments,
				ScoreArpeggios:     note.ScoreArpeggios,
				ScoreSlurs:         note.ScoreSlurs,
				TieStart:           !first && !slash, TieEnd: remaining > duration && !slash,
				Slash:   slash,
				Channel: slices.Index(channels, note.Channel),
			})
			for len(gridByMeasure) <= measure {
				gridByMeasure = append(gridByMeasure, beatGrid{})
			}
			if inTriplet {
				if gridByMeasure[measure].triplet == nil {
					gridByMeasure[measure] = beatGrid{beat: triplets.beat, triplet: map[uint32]bool{}}
				}
				gridByMeasure[measure].triplet[beatStart-measures.starts[measure]] = true
			}
			note.ScoreArticulations = nil
			note.ScoreFermatas = nil
			note.ScoreOrnaments = nil
			note.ScoreArpeggios = nil
			note.ScoreSlurs = nil
			note.Lyrics = nil
			position += duration
			remaining -= duration
			first = false
		}
	}
	markerMeasures := make(map[int][]xmlDirection)
	markerBarlines := make(map[int]bool)
	for _, marker := range markers {
		measure, offset := measures.locate(marker.Position)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		// A marker opens a section, so the bar before it ends with a double
		// barline. MuseScore only reads barlines on the right of a measure.
		if measure > 0 {
			markerBarlines[measure-1] = true
		}
		if !primary {
			continue
		}
		markerMeasures[measure] = append(markerMeasures[measure], xmlDirection{
			Placement: "above", System: "only-top", Offset: &offset,
			Type: xmlDirectionType{Words: &xmlWords{
				Weight: "bold", Enclosure: "rectangle", Text: marker.Name,
			}},
		})
	}
	tempoMeasures := make(map[int][]xmlDirection)
	for _, tempo := range tempos {
		measure, offset := measures.locate(tempo.Position)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		tempoMeasures[measure] = append(tempoMeasures[measure], xmlDirection{
			Placement: "above", Offset: &offset,
			Type:  xmlDirectionType{Metronome: &xmlMetronome{BeatUnit: "quarter", PerMinute: printedTempo(tempo.BPM)}},
			Sound: &xmlSound{Tempo: tempo.BPM},
		})
	}
	chordMeasures := make(map[int][]xmlHarmony)
	for _, chord := range sequence.Chords {
		if chord.Name == "" {
			continue
		}
		measure, offset := measures.locate(chord.Position)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		chordMeasures[measure] = append(chordMeasures[measure], makeHarmony(chord, offset))
	}
	timeMeasures := make(map[int]xmlTime)
	for _, signature := range timeSignatures {
		measure, _ := measures.locate(signature.Position)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		grouping := signature.BeatGrouping
		if !signature.PrintCompositeSignature {
			grouping = nil
		}
		timeMeasures[measure] = makeXMLTime(uint64(signature.Numerator), uint64(signature.Denominator), grouping)
	}
	keyMeasures := make(map[int]xmlKey)
	for _, signature := range keySignatures {
		measure, _ := measures.locate(signature.Position)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		mode := "major"
		if signature.Minor {
			mode = "minor"
		}
		keyMeasures[measure] = xmlKey{Fifths: int(signature.Fifths), Mode: mode}
	}
	if len(byMeasure) == 0 {
		byMeasure = append(byMeasure, nil)
	}

	part := xmlPart{ID: id}
	for i, notes := range byMeasure {
		slices.SortFunc(notes, func(a, b noteSegment) int {
			return cmp.Or(cmp.Compare(a.Start, b.Start),
				cmp.Compare(b.Duration, a.Duration), cmp.Compare(a.Pitch, b.Pitch))
		})
		assignVoices(notes, max(len(channels), 1))
		measure := xmlMeasure{Number: i + 1, length: measures.durations[i]}
		if i == 0 {
			mode := strings.ToLower(metadata.Mode)
			if mode != "major" && mode != "minor" {
				mode = ""
			}
			time := makeXMLTime(metadata.TimeSignature[0], metadata.TimeSignature[1], nil)
			key := xmlKey{Fifths: keyFifths(metadata.Key), Mode: mode}
			measure.Attributes = &xmlAttributes{Divisions: ticksPerQuarter, Key: &key, Time: &time}
			if metadata.BPM > 0 && len(tempos) == 0 {
				measure.Directions = append(measure.Directions, xmlDirection{
					Placement: "above",
					Type:      xmlDirectionType{Metronome: &xmlMetronome{BeatUnit: "quarter", PerMinute: printedTempo(metadata.BPM)}},
					Sound:     &xmlSound{Tempo: metadata.BPM},
				})
			}
		}
		if time, ok := timeMeasures[i]; ok {
			if measure.Attributes == nil {
				measure.Attributes = &xmlAttributes{}
			}
			measure.Attributes.Time = &time
		}
		if key, ok := keyMeasures[i]; ok {
			if measure.Attributes == nil {
				measure.Attributes = &xmlAttributes{}
			}
			measure.Attributes.Key = &key
		}
		if markerBarlines[i] {
			measure.Barline = &xmlBarline{Location: "right", Style: "light-light"}
		}
		measure.Directions = append(measure.Directions, markerMeasures[i]...)
		measure.Directions = append(measure.Directions, tempoMeasures[i]...)
		measure.Harmonies = append(measure.Harmonies, chordMeasures[i]...)
		grid := beatGrid{}
		if i < len(gridByMeasure) {
			grid = gridByMeasure[i]
		}
		measure.Items = measureItems(notes, measures.durations[i], grid, max(len(channels), 1))
		part.Measures = append(part.Measures, measure)
	}
	return part
}

// makeXMLTime renders a meter, using a composite "2+3" beats value when Logic
// records an explicit beat grouping.
func makeXMLTime(numerator, denominator uint64, grouping []uint8) xmlTime {
	if numerator == 0 || denominator == 0 {
		numerator, denominator = 4, 4
	}
	beats := strconv.FormatUint(numerator, 10)
	if len(grouping) != 0 {
		parts := make([]string, len(grouping))
		for i, group := range grouping {
			parts[i] = strconv.Itoa(int(group))
		}
		beats = strings.Join(parts, "+")
	}
	return xmlTime{Beats: beats, BeatType: uint64(denominator)}
}

// musicXMLChordKind maps a MusicXML kind value to the interval mask it
// describes.
type musicXMLChordKind struct {
	Name         string
	IntervalMask uint16
}

// MusicXML 4.0 kind-value table. Functional kinds that share pitch sets with
// pop chords are retained here but are not inferred from a mask alone.
var musicXMLChordKinds = [...]musicXMLChordKind{
	{"augmented", 0x111}, {"augmented-seventh", 0x511},
	{"diminished", 0x049}, {"diminished-seventh", 0x249},
	{"dominant", 0x491}, {"dominant-11th", 0x4b5},
	{"dominant-13th", 0x6b5}, {"dominant-ninth", 0x495},
	{"French", 0x451}, {"German", 0x491}, {"half-diminished", 0x449},
	{"Italian", 0x411}, {"major", 0x091}, {"major-11th", 0x8b5},
	{"major-13th", 0xab5}, {"major-minor", 0x889},
	{"major-ninth", 0x895}, {"major-seventh", 0x891},
	{"major-sixth", 0x291}, {"minor", 0x089}, {"minor-11th", 0x4ad},
	{"minor-13th", 0x6ad}, {"minor-ninth", 0x48d},
	{"minor-seventh", 0x489}, {"minor-sixth", 0x289},
	{"Neapolitan", 0x091}, {"none", 0}, {"other", 0}, {"pedal", 0x001},
	{"power", 0x081}, {"suspended-fourth", 0x0a1},
	{"suspended-second", 0x085}, {"Tristan", 0x449},
}

// makeHarmony renders a chord symbol. Chords without a matching MusicXML kind
// fall back to kind "other" plus explicit degrees, which keeps both the
// printed text and the pitch content.
func makeHarmony(chord logicx.Chord, offset uint32) xmlHarmony {
	if chord.NoChord {
		empty := ""
		return xmlHarmony{
			Placement: "above", Root: xmlHarmonyRoot{Step: xmlHarmonyStep{Value: "C", Text: &empty}},
			Kind: xmlHarmonyKind{Value: "none"}, Offset: offset,
		}
	}
	rootStep, rootAlter, rootName := harmonyPitch(chord.RootPitchClass, chord.RootSpelling)
	kind := harmonyKind(chord)
	harmony := xmlHarmony{
		Placement: "above",
		Root:      xmlHarmonyRoot{Step: xmlHarmonyStep{Value: rootStep}, Alter: rootAlter},
		Kind:      xmlHarmonyKind{Value: kind},
		Offset:    offset,
	}
	if chord.HasBass {
		bassStep, bassAlter, bassName := harmonyPitch(chord.BassPitchClass, chord.BassSpelling)
		harmony.Bass = &xmlHarmonyBass{Step: bassStep, Alter: bassAlter}
		chord.Name = strings.TrimSuffix(chord.Name, "/"+bassName)
	}
	if kind == "other" {
		harmony.Kind.Text = strings.TrimSpace(strings.TrimPrefix(chord.Name, rootName))
		harmony.Degrees = harmonyDegrees(chord)
	}
	return harmony
}

// harmonyKind returns the MusicXML kind for a chord's interval mask. Kinds
// that only differ from a pop chord by function are skipped, since a mask
// alone cannot distinguish them.
func harmonyKind(chord logicx.Chord) string {
	if chord.Scale {
		return "other"
	}
	for _, kind := range musicXMLChordKinds {
		switch kind.Name {
		case "French", "German", "Italian", "Neapolitan", "Tristan", "none", "other", "pedal":
			continue
		}
		if kind.IntervalMask == chord.IntervalMask {
			return kind.Name
		}
	}
	return "other"
}

// harmonyPitch splits a pitch class and Logic spelling code into a MusicXML
// step and alteration. Spellings out of range, or that do not land on a
// natural note name, fall back to naming the pitch class with sharps.
func harmonyPitch(pitch, spelling uint8) (step string, alter *int, name string) {
	naturals := [...]string{"C", "", "D", "", "E", "F", "", "G", "", "A", "", "B"}
	accidental := [...]string{"bb", "b", "", "#", "##"}
	pitch %= 12
	if int(spelling) >= len(accidental) {
		return sharpPitchClass(pitch)
	}
	value := int(spelling) - 2
	step = naturals[(int(pitch)-value+12)%12]
	if step == "" {
		return sharpPitchClass(pitch)
	}
	name = step + accidental[spelling]
	if value == 0 {
		return step, nil, name
	}
	return step, &value, name
}

// pitchSpelling holds one Logic spelling code per pitch class, saying how a
// staff writes each of the twelve notes.
type pitchSpelling [12]uint8

// blackKey reports whether a pitch class has to be written with an accidental.
func blackKey(class int) bool {
	return class == 1 || class == 3 || class == 6 || class == 8 || class == 10
}

// projectSpelling decides how to write each pitch class. Logic stores a MIDI
// pitch and nothing about how it was notated, so the spelling has to be
// recovered: the key signature says whether the black keys are sharps or flats,
// and where it is the C major Logic starts out with, the chord track says
// instead. A chord root or bass then names its own pitch class outright, so an
// F# chord keeps its sharp in a flat-sided piece.
func projectSpelling(project logicx.ProjectData) pitchSpelling {
	chords := slices.Clone(project.ProjectChords)
	for _, sequence := range project.Sequences {
		chords = append(chords, sequence.Chords...)
	}

	fifths := 0
	for _, signature := range project.KeySignatures {
		if signature.Fifths != 0 {
			fifths = int(signature.Fifths)
			break
		}
	}
	if fifths == 0 {
		var flats, sharps int
		for _, chord := range chords {
			switch chord.RootSpelling {
			case 1:
				flats++
			case 3:
				sharps++
			}
		}
		if flats > sharps {
			fifths = -1
		}
	}

	black := uint8(3) // sharp
	if fifths < 0 {
		black = 1 // flat
	}
	var spelling pitchSpelling
	for class := range spelling {
		spelling[class] = 2 // natural
		if blackKey(class) {
			spelling[class] = black
		}
	}

	// A chord names the black keys it uses; ties keep the key's side.
	var votes [12][5]int
	for _, chord := range chords {
		for _, named := range [...]struct{ class, code uint8 }{
			{chord.RootPitchClass, chord.RootSpelling},
			{chord.BassPitchClass, chord.BassSpelling},
		} {
			if blackKey(int(named.class%12)) && (named.code == 1 || named.code == 3) {
				votes[named.class%12][named.code]++
			}
		}
	}
	for class, tally := range votes {
		switch {
		case tally[1] > tally[3]:
			spelling[class] = 1
		case tally[3] > tally[1]:
			spelling[class] = 3
		}
	}
	return spelling
}

// sharpPitchClass names a pitch class using sharps, for pitches that carry no
// usable spelling of their own.
func sharpPitchClass(pitch uint8) (step string, alter *int, name string) {
	steps := [...]string{"C", "C", "D", "D", "E", "F", "F", "G", "G", "A", "A", "B"}
	sharps := [...]bool{false, true, false, true, false, false, true, false, true, false, true, false}
	step = steps[pitch%12]
	if !sharps[pitch%12] {
		return step, nil, step
	}
	one := 1
	return step, &one, step + "#"
}

// harmonyDegrees lists a chord's tones as scale degrees relative to its root,
// for kinds MusicXML cannot name.
func harmonyDegrees(chord logicx.Chord) []xmlHarmonyDegree {
	// Indexed by semitones above the root; index 0 is the root itself.
	table := [...]struct{ value, alter int }{
		{}, {9, -1}, {9, 0}, {3, -1}, {3, 0}, {11, 0},
		{5, -1}, {5, 0}, {13, -1}, {13, 0}, {7, -1}, {7, 0},
	}
	mask := chord.IntervalMask
	if chord.Scale {
		mask = chord.ScaleMask
	}
	var degrees []xmlHarmonyDegree
	for interval := 1; interval < len(table); interval++ {
		if mask&(1<<interval) == 0 {
			continue
		}
		degree := table[interval]
		if interval == 3 && strings.Contains(chord.Name, "#9") {
			degree.value, degree.alter = 9, 1
		}
		if interval == 6 && strings.Contains(chord.Name, "#11") {
			degree.value, degree.alter = 11, 1
		}
		degrees = append(degrees, xmlHarmonyDegree{Value: degree.value, Alter: degree.alter, Type: "add"})
	}
	return degrees
}

// measureItems turns the segments of one measure into note elements, moving
// the MusicXML cursor forward or back between them. Segments that share a
// start and a duration become one chord; each voice is written in turn, backing
// the cursor up to the barline in between. Voices up to channels are the
// channels' own and get rests; voices beyond hold their overlapping notes.
func measureItems(notes []noteSegment, duration uint32, grid beatGrid, channels int) []xmlMeasureItem {
	voices := 0
	for _, segment := range notes {
		voices = max(voices, segment.Voice)
	}
	if voices == 0 {
		return []xmlMeasureItem{{Note: &xmlNote{
			Rest: &xmlRest{Measure: "yes"}, Duration: duration, Voice: 1,
		}}}
	}
	var items []xmlMeasureItem
	var cursor uint32
	for voice := 1; voice <= voices; voice++ {
		var previous noteSegment
		first := true
		for _, segment := range notes {
			if segment.Voice != voice {
				continue
			}
			if first && len(items) > 0 {
				// Voices restart at the beginning of the measure.
				items = append(items, xmlMeasureItem{Backup: &xmlMove{Duration: cursor}})
				cursor = 0
			}
			// MusicXML gives a chord the duration of its first note, so segments
			// that start together but end apart cannot share one; they get their
			// own cursor move instead.
			chord := !first && segment.Start == previous.Start && segment.Duration == previous.Duration
			if !chord {
				if segment.Start > cursor {
					items = append(items, restItems(cursor, segment.Start-cursor, voice, channels, grid)...)
				} else if segment.Start < cursor {
					items = append(items, xmlMeasureItem{Backup: &xmlMove{Duration: cursor - segment.Start}})
				}
				cursor = segment.Start + segment.Duration
			}
			note := makeXMLNote(segment, chord)
			markTuplet(note, grid, segment.Start, chord)
			items = append(items, xmlMeasureItem{Note: note})
			previous = segment
			first = false
		}
		if !first && cursor < duration {
			items = append(items, restItems(cursor, duration-cursor, voice, channels, grid)...)
			cursor = duration
		}
	}
	return items
}

// restItems fills a gap so the staff shows rests rather than blank space.
// Only each channel's own voice is filled; overlap voices are sparse by nature
// and a full rest chain in each would clutter the staff.
func restItems(start, duration uint32, voice, channels int, grid beatGrid) []xmlMeasureItem {
	if voice > channels {
		return []xmlMeasureItem{{Forward: &xmlMove{Duration: duration}}}
	}
	var items []xmlMeasureItem
	for duration > 0 {
		part := duration
		// Rests obey the same tuplet edges as notes, or the beat would not add
		// up to three of anything.
		beatStart, inTriplet := grid.at(start)
		switch next, ok := grid.nextTriplet(start, start+duration); {
		case inTriplet:
			part = min(part, beatStart+grid.beat-start)
		case ok:
			part = min(part, next-start)
		}
		if !inTriplet {
			part = notatable(part)
		}
		rest := &xmlNote{Rest: &xmlRest{}, Duration: part, Voice: voice}
		markTuplet(rest, grid, start, false)
		items = append(items, xmlMeasureItem{Note: rest})
		start += part
		duration -= part
	}
	return items
}

// markTuplet notates one element of a triplet beat: three of them stand in for
// two of the printed value. The tuplet bracket opens on the first element of
// the beat and closes on the last, and a chord note carries only the scaling.
func markTuplet(note *xmlNote, grid beatGrid, start uint32, chord bool) {
	beatStart, ok := grid.at(start)
	// A note filling the whole beat, as another voice's triplets sound
	// against it, is plain.
	if !ok || note.Duration == 0 || note.Duration == grid.beat {
		return
	}
	slot := grid.beat / 3
	note.Type = noteTypeName(note.Duration / slot * (grid.beat / 2))
	note.TimeModification = &xmlTimeModification{Actual: 3, Normal: 2}
	if chord {
		return
	}
	var tuplets []xmlTuplet
	if start == beatStart {
		tuplets = append(tuplets, xmlTuplet{Type: "start"})
	}
	if start+note.Duration == beatStart+grid.beat {
		tuplets = append(tuplets, xmlTuplet{Type: "stop"})
	}
	if len(tuplets) == 0 {
		return
	}
	if note.Notations == nil {
		note.Notations = &xmlNotations{}
	}
	note.Notations.Tuplets = append(note.Notations.Tuplets, tuplets...)
}

// noteTypeName is the printed note value of a duration, empty when no single
// value matches.
func noteTypeName(duration uint32) string {
	names := [...]string{"whole", "half", "quarter", "eighth", "16th", "32nd", "64th"}
	value := ticksPerQuarter * 4
	for _, name := range names {
		if value == duration {
			return name
		}
		value /= 2
	}
	return ""
}

// assignVoices spreads overlapping notes across voices. A MusicXML voice is
// monophonic apart from chords, and MuseScore refuses to import a measure whose
// voice plays two notes at once. Notes must be sorted by start, longest first.
//
// Channel rank r plays voice r+1; its overlapping notes take voices after the
// last channel's, so a channel keeps its voice from measure to measure.
func assignVoices(notes []noteSegment, channels int) {
	extra := channels
	for rank := range channels {
		var ends []uint32 // tick at which each of the channel's voices is free
		var voices []int  // the voice number of each
		var previous *noteSegment
		for i := range notes {
			note := &notes[i]
			if note.Channel != rank {
				continue
			}
			if previous != nil && previous.Start == note.Start && previous.Duration == note.Duration {
				note.Voice = previous.Voice
				continue
			}
			voice := 0
			for voice < len(ends) && ends[voice] > note.Start {
				voice++
			}
			if voice == len(ends) {
				ends = append(ends, 0)
				if voice == 0 {
					voices = append(voices, rank+1)
				} else {
					extra++
					voices = append(voices, extra)
				}
			}
			ends[voice] = note.Start + note.Duration
			note.Voice = voices[voice]
			previous = note
		}
	}
}

// makeXMLNote renders one segment, attaching its ties, notations and lyrics.
func makeXMLNote(segment noteSegment, chord bool) *xmlNote {
	// TODO(logicx): Does the chord record preserve spelling for every tone?
	// A chord staff spells its tones from the project's pitch classes, not from
	// the voicing Logic recorded.
	note := &xmlNote{
		Pitch:    &xmlPitch{Step: segment.Step, Alter: segment.Alter, Octave: int(segment.Pitch)/12 - 1},
		Duration: segment.Duration, Voice: max(segment.Voice, 1),
	}
	if chord {
		note.Chord = &struct{}{}
	}
	if segment.Slash {
		note.Stem, note.Notehead = "none", "slash"
		return note
	}
	var notations xmlNotations
	if segment.TieStart {
		note.Ties = append(note.Ties, xmlTie{Type: "stop"})
		notations.Tied = append(notations.Tied, xmlTie{Type: "stop"})
	}
	if segment.TieEnd {
		note.Ties = append(note.Ties, xmlTie{Type: "start"})
		notations.Tied = append(notations.Tied, xmlTie{Type: "start"})
	}
	for _, slur := range segment.ScoreSlurs {
		notations.Slurs = append(notations.Slurs, xmlSlur{
			Type: string(slur.Type), Number: slur.Number, Placement: string(slur.Placement),
		})
	}
	notations.Articulations = makeXMLArticulations(segment.ScoreArticulations)
	for _, fermata := range segment.ScoreFermatas {
		typeName := "upright"
		if fermata.Inverted {
			typeName = "inverted"
		}
		notations.Fermatas = append(notations.Fermatas, xmlFermata{Type: typeName, Value: "normal"})
	}
	for _, arpeggio := range segment.ScoreArpeggios {
		notations.Arpeggiates = append(notations.Arpeggiates, xmlArpeggiate{Direction: string(arpeggio.Direction)})
	}
	notations.Ornaments = makeXMLOrnaments(segment.ScoreOrnaments)
	if !notations.empty() {
		note.Notations = &notations
	}
	for _, lyric := range segment.Lyrics {
		number := ""
		if lyric.Verse != 0 {
			number = strconv.Itoa(int(lyric.Verse))
		}
		note.Lyrics = append(note.Lyrics, xmlLyric{Number: number, Text: lyric.Text})
	}
	return note
}

// makeXMLOrnaments collects a note's ornaments, or nil when it has none.
func makeXMLOrnaments(ornaments []logicx.ScoreOrnament) *xmlOrnaments {
	var result xmlOrnaments
	for _, ornament := range ornaments {
		switch ornament.Kind {
		case logicx.ScoreOrnamentTurn:
			result.Turn = &struct{}{}
		case logicx.ScoreOrnamentInvertedTurn:
			result.InvertedTurn = &struct{}{}
		case logicx.ScoreOrnamentInvertedTurnWithLine:
			result.InvertedVerticalTurn = &struct{}{}
		case logicx.ScoreOrnamentMordent:
			result.Mordent = &struct{}{}
		case logicx.ScoreOrnamentInvertedMordent:
			result.InvertedMordent = &struct{}{}
		case logicx.ScoreOrnamentTrill:
			result.TrillMark = &struct{}{}
		case logicx.ScoreOrnamentTremolo:
			value := 3
			result.Tremolo = &value
		}
	}
	if result == (xmlOrnaments{}) {
		return nil
	}
	return &result
}

// makeXMLArticulations collects a note's articulations, or nil when it has
// none.
func makeXMLArticulations(articulations []logicx.ScoreArticulation) *xmlArticulations {
	var result xmlArticulations
	for _, articulation := range articulations {
		switch articulation.Kind {
		case logicx.ScoreArticulationStaccato:
			result.Staccato = &struct{}{}
		case logicx.ScoreArticulationTenuto:
			result.Tenuto = &struct{}{}
		case logicx.ScoreArticulationAccent:
			result.Accent = &struct{}{}
		case logicx.ScoreArticulationMarcato:
			direction := "up"
			if articulation.Flipped {
				direction = "down"
			}
			result.StrongAccent = &xmlStrongAccent{Type: direction}
		case logicx.ScoreArticulationStaccatissimo:
			result.Staccatissimo = &struct{}{}
		}
	}
	if result == (xmlArticulations{}) {
		return nil
	}
	return &result
}

// keyFifthsByName counts fifths from C for each major key name.
var keyFifthsByName = map[string]int{
	"CB": -7, "GB": -6, "DB": -5, "AB": -4, "EB": -3, "BB": -2, "F": -1,
	"C": 0, "G": 1, "D": 2, "A": 3, "E": 4, "B": 5, "F#": 6, "C#": 7,
}

// keyFifths converts a metadata key name to a MusicXML fifths value,
// defaulting to C for names it does not know.
func keyFifths(key string) int {
	return keyFifthsByName[strings.ToUpper(key)]
}

// xmlScore is the score-partwise document root. The types below mirror the
// MusicXML 4.0 elements they are named after; element order within each struct
// is the order MusicXML requires.
type xmlScore struct {
	XMLName  xml.Name    `xml:"score-partwise"`
	Version  string      `xml:"version,attr"`
	PartList xmlPartList `xml:"part-list"`
	Parts    []xmlPart   `xml:"part"`
}

// xmlPartList is the part-list element.
type xmlPartList struct {
	Parts []xmlScorePart `xml:"score-part"`
}

// xmlScorePart is a score-part entry in the part list.
type xmlScorePart struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"part-name"`
}

// xmlPart is a part element holding one staff's measures.
type xmlPart struct {
	ID       string       `xml:"id,attr"`
	Measures []xmlMeasure `xml:"measure"`
}

// xmlMeasure is a measure element. Items carries the notes and cursor moves,
// which must keep their relative order.
type xmlMeasure struct {
	Number     int              `xml:"number,attr"`
	Attributes *xmlAttributes   `xml:"attributes,omitempty"`
	Directions []xmlDirection   `xml:"direction,omitempty"`
	Harmonies  []xmlHarmony     `xml:"harmony,omitempty"`
	Items      []xmlMeasureItem `xml:",any"`
	Barline    *xmlBarline      `xml:"barline,omitempty"`
	length     uint32           // bar length in ticks; not part of the document
}

// xmlBarline is a barline element: a section divider or the final barline.
type xmlBarline struct {
	Location string `xml:"location,attr"`
	Style    string `xml:"bar-style"`
}

// xmlHarmony is a harmony element: one chord symbol.
type xmlHarmony struct {
	Placement string             `xml:"placement,attr,omitempty"`
	Root      xmlHarmonyRoot     `xml:"root"`
	Kind      xmlHarmonyKind     `xml:"kind"`
	Bass      *xmlHarmonyBass    `xml:"bass,omitempty"`
	Degrees   []xmlHarmonyDegree `xml:"degree,omitempty"`
	Offset    uint32             `xml:"offset"`
}

// xmlHarmonyRoot is a chord symbol's root.
type xmlHarmonyRoot struct {
	Step  xmlHarmonyStep `xml:"root-step"`
	Alter *int           `xml:"root-alter,omitempty"`
}

// xmlHarmonyStep is a root-step element. Text overrides the printed text, an
// empty value suppressing it.
type xmlHarmonyStep struct {
	Value string  `xml:",chardata"`
	Text  *string `xml:"text,attr,omitempty"`
}

// xmlHarmonyKind is a kind element. Text is the printed chord suffix.
type xmlHarmonyKind struct {
	Value string `xml:",chardata"`
	Text  string `xml:"text,attr,omitempty"`
}

// xmlHarmonyBass is a chord symbol's slash bass note.
type xmlHarmonyBass struct {
	Step  string `xml:"bass-step"`
	Alter *int   `xml:"bass-alter,omitempty"`
}

// xmlHarmonyDegree is one added or altered degree of a chord symbol.
type xmlHarmonyDegree struct {
	Value int    `xml:"degree-value"`
	Alter int    `xml:"degree-alter"`
	Type  string `xml:"degree-type"`
}

// xmlAttributes is an attributes element: divisions, key and meter.
type xmlAttributes struct {
	Divisions uint32   `xml:"divisions,omitempty"`
	Key       *xmlKey  `xml:"key,omitempty"`
	Time      *xmlTime `xml:"time,omitempty"`
}

// xmlKey is a key element, with fifths counted from C.
type xmlKey struct {
	Fifths int    `xml:"fifths"`
	Mode   string `xml:"mode,omitempty"`
}

// xmlTime is a time element. Beats is a string so composite meters can be
// written as "2+3".
type xmlTime struct {
	Beats    string `xml:"beats"`
	BeatType uint64 `xml:"beat-type"`
}

// xmlDirection is a direction element: a marker, tempo or other instruction.
type xmlDirection struct {
	Placement string           `xml:"placement,attr,omitempty"`
	System    string           `xml:"system,attr,omitempty"`
	Type      xmlDirectionType `xml:"direction-type"`
	Offset    *uint32          `xml:"offset,omitempty"`
	Sound     *xmlSound        `xml:"sound,omitempty"`
}

// xmlWords is a text direction. Markers use it rather than a rehearsal
// element, which notation programs renumber into their own A, B, C sequence
// and so lose the section name.
type xmlWords struct {
	Weight    string `xml:"font-weight,attr,omitempty"`
	Enclosure string `xml:"enclosure,attr,omitempty"`
	Text      string `xml:",chardata"`
}

// xmlDirectionType is the direction-type element's content.
type xmlDirectionType struct {
	Metronome *xmlMetronome `xml:"metronome,omitempty"`
	Words     *xmlWords     `xml:"words,omitempty"`
}

// printedTempo rounds a tempo for the metronome mark above the staff. Logic
// stores it as a float, so a plain 133 arrives as 132.99989318847656 and would
// be printed in full. The sound element keeps the exact value for playback.
func printedTempo(bpm float64) string {
	return strconv.FormatFloat(math.Round(bpm*100)/100, 'f', -1, 64)
}

// xmlMetronome is a printed metronome mark.
type xmlMetronome struct {
	BeatUnit  string `xml:"beat-unit"`
	PerMinute string `xml:"per-minute"`
}

// xmlSound is a sound element carrying playback tempo.
type xmlSound struct {
	Tempo float64 `xml:"tempo,attr"`
}

// xmlMeasureItem is one of the order-sensitive measure children, encoded by
// MarshalXML as whichever field is set.
type xmlMeasureItem struct {
	Forward *xmlMove `xml:"forward,omitempty"`
	Backup  *xmlMove `xml:"backup,omitempty"`
	Note    *xmlNote `xml:"note,omitempty"`
}

// MarshalXML writes whichever of the alternatives is set, so that forward,
// backup and note elements keep their document order within a measure.
func (item xmlMeasureItem) MarshalXML(encoder *xml.Encoder, _ xml.StartElement) error {
	switch {
	case item.Forward != nil:
		return encoder.EncodeElement(item.Forward, xml.StartElement{Name: xml.Name{Local: "forward"}})
	case item.Backup != nil:
		return encoder.EncodeElement(item.Backup, xml.StartElement{Name: xml.Name{Local: "backup"}})
	default:
		return encoder.EncodeElement(item.Note, xml.StartElement{Name: xml.Name{Local: "note"}})
	}
}

// xmlMove is a forward or backup element moving the measure cursor.
type xmlMove struct {
	Duration uint32 `xml:"duration"`
}

// xmlNote is a note element. Chord is set on every note but the first of a
// simultaneity.
type xmlNote struct {
	Chord    *struct{} `xml:"chord,omitempty"`
	Rest     *xmlRest  `xml:"rest,omitempty"`
	Pitch    *xmlPitch `xml:"pitch,omitempty"`
	Duration uint32    `xml:"duration"`
	Ties     []xmlTie  `xml:"tie,omitempty"`
	Voice    int       `xml:"voice"`
	Type     string    `xml:"type,omitempty"`

	TimeModification *xmlTimeModification `xml:"time-modification,omitempty"`

	Stem      string        `xml:"stem,omitempty"`
	Notehead  string        `xml:"notehead,omitempty"`
	Notations *xmlNotations `xml:"notations,omitempty"`
	Lyrics    []xmlLyric    `xml:"lyric,omitempty"`
}

// xmlTimeModification scales a tuplet's real duration against its printed
// note value.
type xmlTimeModification struct {
	Actual int `xml:"actual-notes"`
	Normal int `xml:"normal-notes"`
}

// xmlTuplet is a tuplet bracket endpoint.
type xmlTuplet struct {
	Type string `xml:"type,attr"`
}

// xmlRest is a rest element. Measure is "yes" for a whole-measure rest.
type xmlRest struct {
	Measure string `xml:"measure,attr,omitempty"`
}

// xmlPitch is a note's pitch, with Alter in semitones.
type xmlPitch struct {
	Step   string `xml:"step"`
	Alter  *int   `xml:"alter,omitempty"`
	Octave int    `xml:"octave"`
}

// xmlTie is a tie or tied element.
type xmlTie struct {
	Type string `xml:"type,attr"`
}

// xmlNotations is a notations element gathering a note's markings.
type xmlNotations struct {
	Tied          []xmlTie          `xml:"tied,omitempty"`
	Slurs         []xmlSlur         `xml:"slur,omitempty"`
	Fermatas      []xmlFermata      `xml:"fermata,omitempty"`
	Arpeggiates   []xmlArpeggiate   `xml:"arpeggiate,omitempty"`
	Tuplets       []xmlTuplet       `xml:"tuplet,omitempty"`
	Articulations *xmlArticulations `xml:"articulations,omitempty"`
	Ornaments     *xmlOrnaments     `xml:"ornaments,omitempty"`
}

// empty reports whether the notations carry nothing, so the element can be
// left out rather than written as an empty tag on every plain note.
func (n xmlNotations) empty() bool {
	return len(n.Tied) == 0 && len(n.Slurs) == 0 && len(n.Fermatas) == 0 &&
		len(n.Arpeggiates) == 0 && len(n.Tuplets) == 0 &&
		n.Articulations == nil && n.Ornaments == nil
}

// xmlSlur is a slur endpoint.
type xmlSlur struct {
	Type      string `xml:"type,attr"`
	Number    uint8  `xml:"number,attr,omitempty"`
	Placement string `xml:"placement,attr,omitempty"`
}

// xmlFermata is a fermata element.
type xmlFermata struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

// xmlArpeggiate is an arpeggiate element.
type xmlArpeggiate struct {
	Direction string `xml:"direction,attr,omitempty"`
}

// xmlArticulations is an articulations element.
type xmlArticulations struct {
	Accent        *struct{}        `xml:"accent,omitempty"`
	StrongAccent  *xmlStrongAccent `xml:"strong-accent,omitempty"`
	Staccato      *struct{}        `xml:"staccato,omitempty"`
	Tenuto        *struct{}        `xml:"tenuto,omitempty"`
	Staccatissimo *struct{}        `xml:"staccatissimo,omitempty"`
}

// xmlStrongAccent is a strong-accent element, whose type gives its direction.
type xmlStrongAccent struct {
	Type string `xml:"type,attr,omitempty"`
}

// xmlOrnaments is an ornaments element.
type xmlOrnaments struct {
	TrillMark            *struct{} `xml:"trill-mark,omitempty"`
	Turn                 *struct{} `xml:"turn,omitempty"`
	InvertedTurn         *struct{} `xml:"inverted-turn,omitempty"`
	InvertedVerticalTurn *struct{} `xml:"inverted-vertical-turn,omitempty"`
	Mordent              *struct{} `xml:"mordent,omitempty"`
	InvertedMordent      *struct{} `xml:"inverted-mordent,omitempty"`
	Tremolo              *int      `xml:"tremolo,omitempty"`
}

// xmlLyric is a lyric element. Number is the verse number, if any.
type xmlLyric struct {
	Number string `xml:"number,attr,omitempty"`
	Text   string `xml:"text"`
}
