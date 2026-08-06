// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"cmp"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/egonelbre/logicx"
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

// writeMusicXML renders one project alternative as a MusicXML 4.0 partwise
// score. Every sequence becomes a part; global markers and the tempo map are
// attached to the first part only, as MusicXML expects.
func writeMusicXML(w io.Writer, alternative logicx.Alternative) error {
	sequences := scoreSequences(alternative.Project)
	if len(sequences) == 0 {
		return errors.New("no MIDI notes or chords found")
	}

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

	origin = quantize(uint64(origin))

	score := xmlScore{Version: "4.0"}
	for i, sequence := range sequences {
		id := "P" + strconv.Itoa(i+1)
		score.PartList.Parts = append(score.PartList.Parts, xmlScorePart{ID: id, Name: sequence.Name})
		var tempos []logicx.TempoChange
		if i == 0 {
			tempos = alternative.Project.TempoChanges
		}
		score.Parts = append(score.Parts, makePart(
			id, sequence, alternative.Project.Markers, i == 0, tempos, alternative.Project.TimeSignatures,
			alternative.Project.KeySignatures, alternative.Metadata, origin,
		))
	}

	// MuseScore refuses to open a score whose parts run to different lengths,
	// so short parts get trailing empty measures.
	bars := 0
	for _, part := range score.Parts {
		bars = max(bars, len(part.Measures))
	}
	for i := range score.Parts {
		for len(score.Parts[i].Measures) < bars {
			score.Parts[i].Measures = append(score.Parts[i].Measures,
				xmlMeasure{Number: len(score.Parts[i].Measures) + 1})
		}
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

// scoreSequences prepares the parts to export. Sequences without notes are
// voiced from their chords, and the project chord track either joins a part
// that already plays it or becomes a part of its own.
func scoreSequences(project logicx.ProjectData) []logicx.MIDISequence {
	sequences := mergeSequences(project.Sequences)
	for i := range sequences {
		if len(sequences[i].Notes) == 0 {
			sequences[i].Notes = chordNotes(sequences[i].Chords)
		}
	}
	if len(project.ProjectChords) == 0 {
		return sequences
	}
	if i := chordStaff(sequences, project.ProjectChords); i >= 0 {
		sequences[i].Chords = append(sequences[i].Chords, project.ProjectChords...)
		return sequences
	}
	chords := logicx.MIDISequence{Name: "Project Chords", Chords: project.ProjectChords}
	chords.Notes = chordNotes(chords.Chords)
	return append(sequences, chords)
}

// mergeSequences combines the regions of a track into one part, keyed by name.
func mergeSequences(sequences []logicx.MIDISequence) []logicx.MIDISequence {
	indexes := make(map[string]int)
	var merged []logicx.MIDISequence
	for _, sequence := range sequences {
		i, ok := indexes[sequence.Name]
		if !ok {
			i = len(merged)
			indexes[sequence.Name] = i
			merged = append(merged, logicx.MIDISequence{Name: sequence.Name})
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

// chordStaff returns the index of the part that already plays every chord
// tone, or -1 when no part does. Such a part shows the chord symbols instead
// of a duplicate chord staff.
func chordStaff(sequences []logicx.MIDISequence, chords []logicx.Chord) int {
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

// quantize snaps a tick value onto the shortestNote grid. Logic stores raw
// performance timing — audio transcriptions in particular — and notation
// cannot render off-grid positions or durations at all.
func quantize(ticks uint64) uint32 {
	const limit = uint64(^uint32(0)) / shortestNote * shortestNote
	return uint32(min((ticks+shortestNote/2)/shortestNote*shortestNote, limit))
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

// noteSegment is the part of a note that falls inside one measure. Notes
// crossing a barline become several tied segments.
type noteSegment struct {
	Start, Duration    uint32
	Pitch              uint8
	Lyrics             []logicx.Lyric
	ScoreArticulations []logicx.ScoreArticulation
	ScoreFermatas      []logicx.ScoreFermata
	ScoreOrnaments     []logicx.ScoreOrnament
	ScoreArpeggios     []logicx.ScoreArpeggio
	ScoreSlurs         []logicx.ScoreSlur
	TieStart, TieEnd   bool
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
	return m
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
	markers []logicx.Marker,
	primary bool,
	tempos []logicx.TempoChange,
	timeSignatures []logicx.TimeSignatureChange,
	keySignatures []logicx.KeySignatureChange,
	metadata logicx.Metadata,
	origin uint32,
) xmlPart {
	measures := newMeasureMap(origin, metadata, timeSignatures)
	var byMeasure [][]noteSegment
	for _, note := range sequence.Notes {
		if note.Duration == 0 {
			continue
		}
		// ponytail: sub-tick position fractions are dropped; use a higher
		// divisions value if real projects need them.
		// Both ends snap to the grid, so notes that met exactly still meet.
		position := quantize(uint64(note.Position))
		remaining := max(quantize(uint64(note.Position)+uint64(note.Duration))-position, shortestNote)
		first := true
		for remaining > 0 {
			measure, within := measures.locate(position)
			for len(byMeasure) <= measure {
				byMeasure = append(byMeasure, nil)
			}
			duration := notatable(min(remaining, measures.durations[measure]-within))
			byMeasure[measure] = append(byMeasure[measure], noteSegment{
				Start: within, Duration: duration, Pitch: note.Pitch,
				Lyrics:             note.Lyrics,
				ScoreArticulations: note.ScoreArticulations,
				ScoreFermatas:      note.ScoreFermatas,
				ScoreOrnaments:     note.ScoreOrnaments,
				ScoreArpeggios:     note.ScoreArpeggios,
				ScoreSlurs:         note.ScoreSlurs,
				TieStart:           !first, TieEnd: remaining > duration,
			})
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
		// A marker opens a section, so it gets a double barline in front of it.
		markerBarlines[measure] = measure > 0
		if !primary {
			continue
		}
		markerMeasures[measure] = append(markerMeasures[measure], xmlDirection{
			Placement: "above", Type: xmlDirectionType{Rehearsal: marker.Name}, Offset: &offset,
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
			Type:  xmlDirectionType{Metronome: &xmlMetronome{BeatUnit: "quarter", PerMinute: tempo.BPM}},
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
		assignVoices(notes)
		measure := xmlMeasure{Number: i + 1}
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
					Type:      xmlDirectionType{Metronome: &xmlMetronome{BeatUnit: "quarter", PerMinute: metadata.BPM}},
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
			measure.Barline = &xmlBarline{Location: "left", Style: "light-light"}
		}
		measure.Directions = append(measure.Directions, markerMeasures[i]...)
		measure.Directions = append(measure.Directions, tempoMeasures[i]...)
		measure.Harmonies = append(measure.Harmonies, chordMeasures[i]...)
		measure.Items = measureItems(notes)
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
// the cursor up to the barline in between.
func measureItems(notes []noteSegment) []xmlMeasureItem {
	voices := 0
	for _, segment := range notes {
		voices = max(voices, segment.Voice)
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
					items = append(items, xmlMeasureItem{Forward: &xmlMove{Duration: segment.Start - cursor}})
				} else if segment.Start < cursor {
					items = append(items, xmlMeasureItem{Backup: &xmlMove{Duration: cursor - segment.Start}})
				}
				cursor = segment.Start + segment.Duration
			}
			items = append(items, xmlMeasureItem{Note: makeXMLNote(segment, chord)})
			previous = segment
			first = false
		}
	}
	return items
}

// assignVoices spreads overlapping notes across voices. A MusicXML voice is
// monophonic apart from chords, and MuseScore refuses to import a measure whose
// voice plays two notes at once. Notes must be sorted by start, longest first.
func assignVoices(notes []noteSegment) {
	var ends []uint32 // tick at which each voice becomes free again
	for i := 0; i < len(notes); {
		j := i
		for j < len(notes) && notes[j].Start == notes[i].Start && notes[j].Duration == notes[i].Duration {
			j++
		}
		voice := 0
		for voice < len(ends) && ends[voice] > notes[i].Start {
			voice++
		}
		if voice == len(ends) {
			ends = append(ends, 0)
		}
		ends[voice] = notes[i].Start + notes[i].Duration
		for k := i; k < j; k++ {
			notes[k].Voice = voice + 1
		}
		i = j
	}
}

// makeXMLNote renders one segment, attaching its ties, notations and lyrics.
func makeXMLNote(segment noteSegment, chord bool) *xmlNote {
	// TODO(logicx): Does the chord record preserve spelling for every tone?
	// Synthesized staff notes currently choose sharps from MIDI pitch alone.
	step, alter, _ := sharpPitchClass(segment.Pitch)
	note := &xmlNote{
		Pitch:    xmlPitch{Step: step, Alter: alter, Octave: int(segment.Pitch)/12 - 1},
		Duration: segment.Duration, Voice: max(segment.Voice, 1),
	}
	if chord {
		note.Chord = &struct{}{}
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
	Barline    *xmlBarline      `xml:"barline,omitempty"`
	Attributes *xmlAttributes   `xml:"attributes,omitempty"`
	Directions []xmlDirection   `xml:"direction,omitempty"`
	Harmonies  []xmlHarmony     `xml:"harmony,omitempty"`
	Items      []xmlMeasureItem `xml:",any"`
}

// xmlBarline is a barline element. Only the left-hand section divider is
// emitted, so the style is fixed.
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
	Type      xmlDirectionType `xml:"direction-type"`
	Offset    *uint32          `xml:"offset,omitempty"`
	Sound     *xmlSound        `xml:"sound,omitempty"`
}

// xmlDirectionType is the direction-type element's content.
type xmlDirectionType struct {
	Metronome *xmlMetronome `xml:"metronome,omitempty"`
	Rehearsal string        `xml:"rehearsal,omitempty"`
	Words     string        `xml:"words,omitempty"`
}

// xmlMetronome is a printed metronome mark.
type xmlMetronome struct {
	BeatUnit  string  `xml:"beat-unit"`
	PerMinute float64 `xml:"per-minute"`
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
	Chord     *struct{}     `xml:"chord,omitempty"`
	Pitch     xmlPitch      `xml:"pitch"`
	Duration  uint32        `xml:"duration"`
	Ties      []xmlTie      `xml:"tie,omitempty"`
	Voice     int           `xml:"voice"`
	Notations *xmlNotations `xml:"notations,omitempty"`
	Lyrics    []xmlLyric    `xml:"lyric,omitempty"`
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
	Articulations *xmlArticulations `xml:"articulations,omitempty"`
	Ornaments     *xmlOrnaments     `xml:"ornaments,omitempty"`
}

// empty reports whether the notations carry nothing, so the element can be
// left out rather than written as an empty tag on every plain note.
func (n xmlNotations) empty() bool {
	return len(n.Tied) == 0 && len(n.Slurs) == 0 && len(n.Fermatas) == 0 &&
		len(n.Arpeggiates) == 0 && n.Articulations == nil && n.Ornaments == nil
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
