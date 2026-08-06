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
	numerator, denominator := alternative.Metadata.TimeSignature[0], alternative.Metadata.TimeSignature[1]
	if numerator == 0 || denominator == 0 {
		numerator, denominator = 4, 4
	}
	measureTicks := ticksPerQuarter * 4 * uint32(numerator) / uint32(denominator)

	score := xmlScore{Version: "4.0"}
	for i, sequence := range sequences {
		id := "P" + strconv.Itoa(i+1)
		score.PartList.Parts = append(score.PartList.Parts, xmlScorePart{ID: id, Name: sequence.Name})
		var markers []logicx.Marker
		if i == 0 {
			markers = alternative.Project.Markers
		}
		score.Parts = append(score.Parts, makePart(id, sequence, markers, alternative.Metadata, origin, measureTicks))
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
	Start, Duration  uint32
	Pitch            uint8
	TieStart, TieEnd bool
}

func makePart(id string, sequence logicx.MIDISequence, markers []logicx.Marker, metadata logicx.Metadata, origin, measureTicks uint32) xmlPart {
	var byMeasure [][]noteSegment
	for _, note := range sequence.Notes {
		if note.Duration == 0 {
			continue
		}
		// ponytail: MusicXML currently rounds sub-tick positions down; use a
		// higher divisions value if real projects contain non-zero fractions.
		start := note.Position - origin
		remaining := note.Duration
		first := true
		for remaining > 0 {
			measure := int(start / measureTicks)
			for len(byMeasure) <= measure {
				byMeasure = append(byMeasure, nil)
			}
			within := start % measureTicks
			duration := min(remaining, measureTicks-within)
			byMeasure[measure] = append(byMeasure[measure], noteSegment{
				Start: within, Duration: duration, Pitch: note.Pitch,
				TieStart: !first, TieEnd: remaining > duration,
			})
			start += duration
			remaining -= duration
			first = false
		}
	}
	markerMeasures := make(map[int][]xmlDirection)
	for _, marker := range markers {
		position := marker.Position - origin
		measure := int(position / measureTicks)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		offset := position % measureTicks
		markerMeasures[measure] = append(markerMeasures[measure], xmlDirection{
			Placement: "above", Type: xmlDirectionType{Rehearsal: marker.Name}, Offset: &offset,
		})
	}
	chordMeasures := make(map[int][]xmlHarmony)
	for _, chord := range sequence.Chords {
		if chord.Name == "" {
			continue
		}
		position := chord.Position - origin
		measure := int(position / measureTicks)
		for len(byMeasure) <= measure {
			byMeasure = append(byMeasure, nil)
		}
		offset := position % measureTicks
		chordMeasures[measure] = append(chordMeasures[measure], makeHarmony(chord, offset))
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
			measure.Attributes = &xmlAttributes{
				Divisions: ticksPerQuarter, Key: xmlKey{Fifths: keyFifths(metadata.Key), Mode: mode},
				Time: xmlTime{Beats: metadata.TimeSignature[0], BeatType: metadata.TimeSignature[1]},
			}
			if metadata.BPM > 0 {
				measure.Directions = append(measure.Directions, xmlDirection{
					Placement: "above",
					Type:      xmlDirectionType{Metronome: &xmlMetronome{BeatUnit: "quarter", PerMinute: metadata.BPM}},
					Sound:     &xmlSound{Tempo: metadata.BPM},
				})
			}
		}
		measure.Directions = append(measure.Directions, markerMeasures[i]...)
		measure.Harmonies = append(measure.Harmonies, chordMeasures[i]...)
		measure.Items = measureItems(notes)
		part.Measures = append(part.Measures, measure)
	}
	return part
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
	return note
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
	Divisions uint32  `xml:"divisions"`
	Key       xmlKey  `xml:"key"`
	Time      xmlTime `xml:"time"`
}

type xmlKey struct {
	Fifths int    `xml:"fifths"`
	Mode   string `xml:"mode,omitempty"`
}

type xmlTime struct {
	Beats    uint64 `xml:"beats"`
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
	Tied []xmlTie `xml:"tied,omitempty"`
}
