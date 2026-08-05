// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"

	"github.com/egonelbre/logicx/internal/record"
)

// MIDISequence is a named event sequence discovered in ProjectData. Logic's
// active track/region references are not decoded yet, so inactive history may
// also be present.
type MIDISequence struct {
	Name        string
	ChunkOffset int
	Notes       []MIDINote
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

func findMIDISequences(chunks []Chunk) []MIDISequence {
	type sequenceID struct{ group, sequence uint32 }
	names := make(map[sequenceID]string)
	var sequences []MIDISequence
	for _, chunk := range chunks {
		id := sequenceID{
			group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
			sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
		}
		if chunk.Type == "MSeq" {
			names[id] = sequenceName(chunk.Data)
			continue
		}
		name, ok := names[id]
		if chunk.Type != "EvSq" || !ok {
			continue
		}
		notes := findMIDINotes(chunk.Data)
		if len(notes) == 0 {
			continue
		}
		sequences = append(sequences, MIDISequence{
			Name: name, ChunkOffset: chunk.Offset, Notes: notes,
		})
	}
	return sequences
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
