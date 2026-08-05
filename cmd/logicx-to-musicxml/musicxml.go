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
	if len(alternative.Project.Sequences) == 0 {
		return errors.New("no MIDI note sequences found")
	}

	origin := logicBarOneTick
	for _, sequence := range alternative.Project.Sequences {
		for _, note := range sequence.Notes {
			origin = min(origin, note.Position)
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

	sequences := mergeSequences(alternative.Project.Sequences)
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
	}
	return merged
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
		measure.Items = measureItems(notes)
		part.Measures = append(part.Measures, measure)
	}
	return part
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
	Items      []xmlMeasureItem `xml:",any"`
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
