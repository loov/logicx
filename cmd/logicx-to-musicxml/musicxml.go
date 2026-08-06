// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/egonelbre/logicx"
)

const (
	ticksPerQuarter = uint32(960)
	logicBarOneTick = uint32(38_400)
)

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

	score := xmlScore{Version: "4.0"}
	for i, sequence := range sequences {
		id := "P" + strconv.Itoa(i+1)
		score.PartList.Parts = append(score.PartList.Parts, xmlScorePart{ID: id, Name: sequence.Name})
		var markers []logicx.Marker
		var tempos []logicx.TempoChange
		if i == 0 {
			markers = alternative.Project.Markers
			tempos = alternative.Project.TempoChanges
		}
		score.Parts = append(score.Parts, makePart(
			id, sequence, markers, tempos, alternative.Project.TimeSignatures,
			alternative.Project.KeySignatures, alternative.Metadata, origin,
		))
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

func scoreSequences(project logicx.ProjectData) []logicx.MIDISequence {
	sequences := mergeSequences(project.Sequences)
	for i := range sequences {
		if len(sequences[i].Notes) == 0 {
			sequences[i].Notes = appendChordNotes(nil, sequences[i].Chords)
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
	chords.Notes = appendChordNotes(nil, chords.Chords)
	return append(sequences, chords)
}

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

type noteKey struct {
	position uint32
	fraction uint16
	pitch    uint8
}

func appendChordNotes(notes []logicx.MIDINote, chords []logicx.Chord) []logicx.MIDINote {
	seen := make(map[noteKey]bool, len(notes))
	for _, note := range notes {
		seen[noteKey{note.Position, note.PositionFraction, note.Pitch}] = true
	}
	for i, chord := range chords {
		duration := chord.Duration
		if duration == 0 {
			// ponytail: absent chord lengths extend to the next chord or one
			// quarter; remove this fallback once the chord decoder proves length.
			duration = ticksPerQuarter
			if i+1 < len(chords) && chords[i+1].Position > chord.Position {
				duration = chords[i+1].Position - chord.Position
			}
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

type noteSegment struct {
	Start, Duration    uint32
	Pitch              uint8
	Lyrics             []logicx.Lyric
	ScoreArticulations []logicx.ScoreArticulation
	ScoreFermatas      []logicx.ScoreFermata
	ScoreOrnaments     []logicx.ScoreOrnament
	ScoreArpeggios     []logicx.ScoreArpeggio
	TieStart, TieEnd   bool
}

type measureMap struct {
	numerator   uint64
	denominator uint64
	changes     []logicx.TimeSignatureChange
	starts      []uint32
	durations   []uint32
}

func newMeasureMap(origin uint32, metadata logicx.Metadata, changes []logicx.TimeSignatureChange) *measureMap {
	changes = append([]logicx.TimeSignatureChange(nil), changes...)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Position < changes[j].Position })
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

func (m *measureMap) locate(position uint32) (int, uint32) {
	for uint64(position) >= uint64(m.starts[len(m.starts)-1])+uint64(m.durations[len(m.durations)-1]) {
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
	return measure, position - m.starts[measure]
}

func makePart(
	id string,
	sequence logicx.MIDISequence,
	markers []logicx.Marker,
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
		// ponytail: MusicXML currently rounds sub-tick positions down; use a
		// higher divisions value if real projects contain non-zero fractions.
		position := note.Position
		remaining := note.Duration
		first := true
		for remaining > 0 {
			measure, within := measures.locate(position)
			for len(byMeasure) <= measure {
				byMeasure = append(byMeasure, nil)
			}
			duration := min(remaining, measures.durations[measure]-within)
			byMeasure[measure] = append(byMeasure[measure], noteSegment{
				Start: within, Duration: duration, Pitch: note.Pitch,
				Lyrics:             note.Lyrics,
				ScoreArticulations: note.ScoreArticulations,
				ScoreFermatas:      note.ScoreFermatas,
				ScoreOrnaments:     note.ScoreOrnaments,
				ScoreArpeggios:     note.ScoreArpeggios,
				TieStart:           !first, TieEnd: remaining > duration,
			})
			note.ScoreArticulations = nil
			note.ScoreFermatas = nil
			note.ScoreOrnaments = nil
			note.ScoreArpeggios = nil
			note.Lyrics = nil
			position += duration
			remaining -= duration
			first = false
		}
	}
	markerMeasures := make(map[int][]xmlDirection)
	for _, marker := range markers {
		measure, offset := measures.locate(marker.Position)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
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
		sort.Slice(notes, func(i, j int) bool {
			if notes[i].Start != notes[j].Start {
				return notes[i].Start < notes[j].Start
			}
			return notes[i].Pitch < notes[j].Pitch
		})
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
		measure.Directions = append(measure.Directions, markerMeasures[i]...)
		measure.Directions = append(measure.Directions, tempoMeasures[i]...)
		measure.Harmonies = append(measure.Harmonies, chordMeasures[i]...)
		measure.Items = measureItems(notes)
		part.Measures = append(part.Measures, measure)
	}
	return part
}

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

func harmonyPitch(pitch, spelling uint8) (step string, alter *int, name string) {
	naturals := [...]string{"C", "", "D", "", "E", "F", "", "G", "", "A", "", "B"}
	value := int(spelling) - 2
	step = naturals[(int(pitch)-value+12)%12]
	accidental := [...]string{"bb", "b", "", "#", "##"}
	name = step + accidental[spelling]
	if value == 0 {
		return step, nil, name
	}
	return step, &value, name
}

func harmonyDegrees(chord logicx.Chord) []xmlHarmonyDegree {
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

func measureItems(notes []noteSegment) []xmlMeasureItem {
	var items []xmlMeasureItem
	var cursor, previousStart uint32
	for i, segment := range notes {
		chord := i > 0 && segment.Start == previousStart
		if !chord {
			if segment.Start > cursor {
				items = append(items, xmlMeasureItem{Forward: &xmlMove{Duration: segment.Start - cursor}})
			} else if segment.Start < cursor {
				items = append(items, xmlMeasureItem{Backup: &xmlMove{Duration: cursor - segment.Start}})
			}
			cursor = segment.Start + segment.Duration
		}
		items = append(items, xmlMeasureItem{Note: makeXMLNote(segment, chord)})
		previousStart = segment.Start
	}
	return items
}

func makeXMLNote(segment noteSegment, chord bool) *xmlNote {
	// TODO(logicx): Does the chord record preserve spelling for every tone?
	// Synthesized staff notes currently choose sharps from MIDI pitch alone.
	steps := [...]string{"C", "C", "D", "D", "E", "F", "F", "G", "G", "A", "A", "B"}
	sharps := [...]bool{false, true, false, true, false, false, true, false, true, false, true, false}
	pitchClass := segment.Pitch % 12
	note := &xmlNote{
		Pitch:    xmlPitch{Step: steps[pitchClass], Octave: int(segment.Pitch)/12 - 1},
		Duration: segment.Duration, Voice: 1,
	}
	if chord {
		note.Chord = &struct{}{}
	}
	if sharps[pitchClass] {
		alter := 1
		note.Pitch.Alter = &alter
	}
	if segment.TieStart {
		note.Ties = append(note.Ties, xmlTie{Type: "stop"})
		note.Notations.Tied = append(note.Notations.Tied, xmlTie{Type: "stop"})
	}
	if segment.TieEnd {
		note.Ties = append(note.Ties, xmlTie{Type: "start"})
		note.Notations.Tied = append(note.Notations.Tied, xmlTie{Type: "start"})
	}
	note.Notations.Articulations = makeXMLArticulations(segment.ScoreArticulations)
	for _, fermata := range segment.ScoreFermatas {
		typeName := "upright"
		if fermata.Inverted {
			typeName = "inverted"
		}
		note.Notations.Fermatas = append(note.Notations.Fermatas, xmlFermata{Type: typeName, Value: "normal"})
	}
	for _, arpeggio := range segment.ScoreArpeggios {
		note.Notations.Arpeggiates = append(note.Notations.Arpeggiates, xmlArpeggiate{Direction: string(arpeggio.Direction)})
	}
	note.Notations.Ornaments = makeXMLOrnaments(segment.ScoreOrnaments)
	for _, lyric := range segment.Lyrics {
		number := ""
		if lyric.Verse != 0 {
			number = strconv.Itoa(int(lyric.Verse))
		}
		note.Lyrics = append(note.Lyrics, xmlLyric{Number: number, Text: lyric.Text})
	}
	return note
}

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

func keyFifths(key string) int {
	return map[string]int{"CB": -7, "GB": -6, "DB": -5, "AB": -4, "EB": -3, "BB": -2, "F": -1, "C": 0, "G": 1, "D": 2, "A": 3, "E": 4, "B": 5, "F#": 6, "C#": 7}[strings.ToUpper(key)]
}

type xmlScore struct {
	XMLName  xml.Name    `xml:"score-partwise"`
	Version  string      `xml:"version,attr"`
	PartList xmlPartList `xml:"part-list"`
	Parts    []xmlPart   `xml:"part"`
}

type xmlPartList struct {
	Parts []xmlScorePart `xml:"score-part"`
}

type xmlScorePart struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"part-name"`
}

type xmlPart struct {
	ID       string       `xml:"id,attr"`
	Measures []xmlMeasure `xml:"measure"`
}

type xmlMeasure struct {
	Number     int              `xml:"number,attr"`
	Attributes *xmlAttributes   `xml:"attributes,omitempty"`
	Directions []xmlDirection   `xml:"direction,omitempty"`
	Harmonies  []xmlHarmony     `xml:"harmony,omitempty"`
	Items      []xmlMeasureItem `xml:",any"`
}

type xmlHarmony struct {
	Placement string             `xml:"placement,attr,omitempty"`
	Root      xmlHarmonyRoot     `xml:"root"`
	Kind      xmlHarmonyKind     `xml:"kind"`
	Bass      *xmlHarmonyBass    `xml:"bass,omitempty"`
	Degrees   []xmlHarmonyDegree `xml:"degree,omitempty"`
	Offset    uint32             `xml:"offset"`
}

type xmlHarmonyRoot struct {
	Step  xmlHarmonyStep `xml:"root-step"`
	Alter *int           `xml:"root-alter,omitempty"`
}

type xmlHarmonyStep struct {
	Value string  `xml:",chardata"`
	Text  *string `xml:"text,attr,omitempty"`
}

type xmlHarmonyKind struct {
	Value string `xml:",chardata"`
	Text  string `xml:"text,attr,omitempty"`
}

type xmlHarmonyBass struct {
	Step  string `xml:"bass-step"`
	Alter *int   `xml:"bass-alter,omitempty"`
}

type xmlHarmonyDegree struct {
	Value int    `xml:"degree-value"`
	Alter int    `xml:"degree-alter"`
	Type  string `xml:"degree-type"`
}

type xmlAttributes struct {
	Divisions uint32   `xml:"divisions,omitempty"`
	Key       *xmlKey  `xml:"key,omitempty"`
	Time      *xmlTime `xml:"time,omitempty"`
}

type xmlKey struct {
	Fifths int    `xml:"fifths"`
	Mode   string `xml:"mode,omitempty"`
}

type xmlTime struct {
	Beats    string `xml:"beats"`
	BeatType uint64 `xml:"beat-type"`
}

type xmlDirection struct {
	Placement string           `xml:"placement,attr,omitempty"`
	Type      xmlDirectionType `xml:"direction-type"`
	Offset    *uint32          `xml:"offset,omitempty"`
	Sound     *xmlSound        `xml:"sound,omitempty"`
}

type xmlDirectionType struct {
	Metronome *xmlMetronome `xml:"metronome,omitempty"`
	Rehearsal string        `xml:"rehearsal,omitempty"`
	Words     string        `xml:"words,omitempty"`
}

type xmlMetronome struct {
	BeatUnit  string  `xml:"beat-unit"`
	PerMinute float64 `xml:"per-minute"`
}

type xmlSound struct {
	Tempo float64 `xml:"tempo,attr"`
}

type xmlMeasureItem struct {
	Forward *xmlMove `xml:"forward,omitempty"`
	Backup  *xmlMove `xml:"backup,omitempty"`
	Note    *xmlNote `xml:"note,omitempty"`
}

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

type xmlMove struct {
	Duration uint32 `xml:"duration"`
}

type xmlNote struct {
	Chord     *struct{}    `xml:"chord,omitempty"`
	Pitch     xmlPitch     `xml:"pitch"`
	Duration  uint32       `xml:"duration"`
	Ties      []xmlTie     `xml:"tie,omitempty"`
	Voice     int          `xml:"voice"`
	Notations xmlNotations `xml:"notations,omitempty"`
	Lyrics    []xmlLyric   `xml:"lyric,omitempty"`
}

type xmlPitch struct {
	Step   string `xml:"step"`
	Alter  *int   `xml:"alter,omitempty"`
	Octave int    `xml:"octave"`
}

type xmlTie struct {
	Type string `xml:"type,attr"`
}

type xmlNotations struct {
	Tied          []xmlTie          `xml:"tied,omitempty"`
	Fermatas      []xmlFermata      `xml:"fermata,omitempty"`
	Arpeggiates   []xmlArpeggiate   `xml:"arpeggiate,omitempty"`
	Articulations *xmlArticulations `xml:"articulations,omitempty"`
	Ornaments     *xmlOrnaments     `xml:"ornaments,omitempty"`
}

type xmlFermata struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

type xmlArpeggiate struct {
	Direction string `xml:"direction,attr,omitempty"`
}

type xmlArticulations struct {
	Accent        *struct{}        `xml:"accent,omitempty"`
	StrongAccent  *xmlStrongAccent `xml:"strong-accent,omitempty"`
	Staccato      *struct{}        `xml:"staccato,omitempty"`
	Tenuto        *struct{}        `xml:"tenuto,omitempty"`
	Staccatissimo *struct{}        `xml:"staccatissimo,omitempty"`
}

type xmlStrongAccent struct {
	Type string `xml:"type,attr,omitempty"`
}

type xmlOrnaments struct {
	TrillMark            *struct{} `xml:"trill-mark,omitempty"`
	Turn                 *struct{} `xml:"turn,omitempty"`
	InvertedTurn         *struct{} `xml:"inverted-turn,omitempty"`
	InvertedVerticalTurn *struct{} `xml:"inverted-vertical-turn,omitempty"`
	Mordent              *struct{} `xml:"mordent,omitempty"`
	InvertedMordent      *struct{} `xml:"inverted-mordent,omitempty"`
	Tremolo              *int      `xml:"tremolo,omitempty"`
}

type xmlLyric struct {
	Number string `xml:"number,attr,omitempty"`
	Text   string `xml:"text"`
}
