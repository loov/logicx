// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/egonelbre/logicx/internal/record"
)

// MIDISequence is an active MIDI region discovered in ProjectData. Position
// and Duration are its arrangement bounds; looped events are expanded.
type MIDISequence struct {
	Name           string
	ChunkOffset    int
	SequenceID     uint32
	Position       uint32
	Duration       uint32
	SourceDuration uint32
	Looped         bool
	Notes          []MIDINote
	Chords         []Chord
}

// MIDINote contains the stable fields of Logic's 32-byte AP note record.
// Raw preserves the remaining undocumented fields.
type MIDINote struct {
	Position         uint32
	PositionFraction uint16
	Pitch            uint8
	Duration         uint32
	Raw              [32]byte
}

// Marker is a Logic global marker. RTF preserves the formatted source text;
// Name is its best-effort plain-text form.
type Marker struct {
	Position uint32
	Length   uint32
	TextID   uint32
	Name     string
	RTF      string
	Raw      [48]byte
}

type sequenceSource struct {
	id             chordSequenceID
	name           string
	chunkOffset    int
	duration       uint32
	positionOffset int32
	notes          []MIDINote
	chords         []Chord
}

func findMIDISequences(chunks []Chunk) []MIDISequence {
	descriptors := make(map[chordSequenceID]Chunk)
	events := make(map[chordSequenceID]Chunk)
	for _, chunk := range chunks {
		id := chordSequenceID{
			group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
			sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
		}
		switch chunk.Type {
		case "MSeq":
			descriptors[id] = chunk
		case "EvSq":
			events[id] = chunk
		}
	}

	var ordered []sequenceSource
	sources := make(map[chordSequenceID]sequenceSource)
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		id := chordSequenceID{
			group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
			sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
		}
		descriptor, ok := descriptors[id]
		if !ok {
			continue
		}
		notes := findMIDINotes(chunk.Data)
		chords := record.Scan(chunk.Data, 32, 16, decodeChordEvent)
		if len(notes) == 0 && len(chords) == 0 {
			continue
		}
		s := sequenceSource{
			id: id, name: sequenceName(descriptor.Data), chunkOffset: chunk.Offset,
			duration: sequenceDuration(descriptor.Data), positionOffset: sequencePositionOffset(descriptor.Data),
			notes: notes, chords: chords,
		}
		sources[id] = s
		ordered = append(ordered, s)
	}

	var sequences []MIDISequence
	for id, event := range events {
		descriptor, ok := descriptors[id]
		if !ok || sequenceName(descriptor.Data) == "Global Harmonies" {
			continue
		}
		links := record.Scan(event.Data, 80, 80, decodeRegionLink)
		for _, link := range links {
			s, ok := sources[chordSequenceID{group: id.group, sequence: link.sequence}]
			if !ok {
				continue
			}
			sequence, valid := materializeRegion(s, link)
			if valid {
				sequences = append(sequences, sequence)
			}
		}
	}
	if len(sequences) != 0 {
		slices.SortFunc(sequences, func(a, b MIDISequence) int {
			return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ChunkOffset, b.ChunkOffset))
		})
		return sequences
	}

	// Older or partially recovered projects may not expose arrangement links.
	for _, s := range ordered {
		inferChordDurationsUntil(s.chords, 0)
		sequences = append(sequences, MIDISequence{
			Name: s.name, ChunkOffset: s.chunkOffset, SequenceID: s.id.sequence,
			Duration: s.duration, SourceDuration: s.duration, Notes: s.notes, Chords: s.chords,
		})
	}
	return sequences
}

const (
	// MSeq names are variable-length, so these fields are addressed from the
	// stable end of the payload.
	sequenceMetadataTail = 219
	noRegionLoop         = 0x3fffffff
)

func sequenceDuration(data []byte) uint32 {
	if len(data) < sequenceMetadataTail {
		return 0
	}
	return binary.LittleEndian.Uint32(data[len(data)-sequenceMetadataTail:])
}

func sequencePositionOffset(data []byte) int32 {
	if len(data) < 55 {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(data[len(data)-55:]))
}

type regionLink struct {
	position uint32
	duration uint32
	sequence uint32
}

func decodeRegionLink(data []byte) (regionLink, bool) {
	var link regionLink
	ok := record.Decode(data,
		record.Equal(0, 0x20, 0),
		record.Uint32LE(4, &link.position),
		record.Uint32LE(28, &link.duration),
		record.Uint32LE(32, &link.sequence),
		record.Equal(36, 0, 0, 0, 0x88),
		record.Equal(68, 0, 0, 0, 0x88),
	)
	return link, ok
}

func materializeRegion(s sequenceSource, link regionLink) (MIDISequence, bool) {
	if link.position > math.MaxUint32-projectChordPositionBias {
		return MIDISequence{}, false
	}
	position := link.position + projectChordPositionBias
	duration := s.duration
	looped := link.duration != 0 && link.duration != noRegionLoop
	if looped {
		duration = link.duration
	}
	end := uint64(position) + uint64(duration)
	if end > math.MaxUint32 {
		return MIDISequence{}, false
	}
	repeats := uint32(1)
	if looped && s.duration != 0 {
		repeats = uint32((uint64(duration) + uint64(s.duration) - 1) / uint64(s.duration))
	}
	sequence := MIDISequence{
		Name: s.name, ChunkOffset: s.chunkOffset, SequenceID: s.id.sequence,
		Position: position, Duration: duration, SourceDuration: s.duration, Looped: looped,
	}
	for repeat := uint32(0); repeat < repeats; repeat++ {
		baseShift := int64(s.positionOffset)
		repeatShift := int64(repeat) * int64(s.duration)
		for _, note := range s.notes {
			base := int64(note.Position) + baseShift
			if looped && base >= int64(position)+int64(s.duration) {
				continue
			}
			p := base + repeatShift
			if p < int64(position) || p < 0 || uint64(p) >= end || p > math.MaxUint32 {
				continue
			}
			note.Position = uint32(p)
			if uint64(note.Position)+uint64(note.Duration) > end {
				note.Duration = uint32(end - uint64(note.Position))
			}
			sequence.Notes = append(sequence.Notes, note)
		}
		for _, chord := range s.chords {
			base := int64(chord.Position) + baseShift
			if looped && base >= int64(position)+int64(s.duration) {
				continue
			}
			p := base + repeatShift
			if p < int64(position) || p < 0 || uint64(p) >= end || p > math.MaxUint32 {
				continue
			}
			chord.Position = uint32(p)
			sequence.Chords = append(sequence.Chords, chord)
		}
	}
	inferChordDurationsUntil(sequence.Chords, uint32(end))
	return sequence, true
}

func findMIDINotes(data []byte) []MIDINote {
	return record.Scan(data, 32, 16, decodeMIDINote)
}

func decodeMIDINote(data []byte) (MIDINote, bool) {
	var note MIDINote
	ok := record.Decode(data,
		record.Equal(0, 0x90),
		record.Uint16LE(2, &note.PositionFraction),
		record.Uint32LE(4, &note.Position),
		record.Equal(10, 'A', 'P'),
		record.Uint8(12, &note.Pitch),
		record.Uint32LE(28, &note.Duration),
		record.Copy(0, note.Raw[:]),
	)
	if !ok || note.Pitch > 127 {
		return MIDINote{}, false
	}
	return note, true
}

func sequenceName(data []byte) string {
	runs := printableRun.FindAll(data, -1)
	if len(runs) == 0 {
		return "MIDI Sequence"
	}
	return strings.TrimSpace(string(runs[len(runs)-1]))
}

func findMarkers(chunks []Chunk) []Marker {
	texts := make(map[uint32]string)
	for _, chunk := range chunks {
		if chunk.Type == "TxSq" {
			textID := binary.LittleEndian.Uint32(chunk.Header[10:14])
			texts[textID] = markerRTF(chunk.Data)
		}
	}

	var markers []Marker
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		markers = append(markers, record.Scan(chunk.Data, 48, 16, markerDecoder(texts))...)
	}
	return markers
}

func markerDecoder(texts map[uint32]string) func([]byte) (Marker, bool) {
	return func(data []byte) (Marker, bool) {
		var marker Marker
		if !record.Decode(data,
			record.Equal(0, 0x12, 0, 0, 0),
			record.Uint32LE(4, &marker.Position),
			record.Uint32LE(16, &marker.TextID),
			record.Equal(20, 0, 0, 0, 0x88),
			record.Uint32LE(28, &marker.Length),
			record.Copy(0, marker.Raw[:]),
		) {
			return Marker{}, false
		}
		rtf, ok := texts[marker.TextID]
		if !ok {
			return Marker{}, false
		}
		marker.Name, marker.RTF = plainRTF(rtf), rtf
		return marker, true
	}
}

func markerRTF(data []byte) string {
	start := bytes.Index(data, []byte(`{\rtf`))
	if start < 0 {
		return ""
	}
	return strings.TrimRight(string(data[start:]), "\x00")
}

var rtfControl = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?`)

func plainRTF(rtf string) string {
	runs := printableRun.FindAllString(rtf, -1)
	if len(runs) == 0 {
		return ""
	}
	// ponytail: Logic writes the marker body as the final printable RTF run;
	// use a full RTF decoder if projects with embedded/non-ASCII names appear.
	text := rtfControl.ReplaceAllString(runs[len(runs)-1], "")
	text = strings.NewReplacer(`\{`, "{", `\}`, "}", `\\`, `\`).Replace(text)
	return strings.Trim(text, "{} \t\r\n")
}
