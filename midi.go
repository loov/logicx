// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"
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
	var notes []MIDINote
	for offset := 0; offset+32 <= len(data); offset += 16 {
		record := data[offset : offset+32]
		if record[0] != 0x90 || !bytes.Equal(record[10:12], []byte("AP")) || record[12] > 127 {
			continue
		}
		var raw [32]byte
		copy(raw[:], record)
		notes = append(notes, MIDINote{
			PositionFraction: binary.LittleEndian.Uint16(record[2:4]),
			Position:         binary.LittleEndian.Uint32(record[4:8]),
			Pitch:            record[12],
			Duration:         binary.LittleEndian.Uint32(record[28:32]),
			Raw:              raw,
		})
	}
	return notes
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
			texts[binary.LittleEndian.Uint32(chunk.Header[10:14])] = markerRTF(chunk.Data)
		}
	}

	var markers []Marker
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		for offset := 0; offset+48 <= len(chunk.Data); offset += 16 {
			record := chunk.Data[offset : offset+48]
			textID := binary.LittleEndian.Uint32(record[16:20])
			rtf, ok := texts[textID]
			if !ok || binary.LittleEndian.Uint32(record[:4]) != 0x12 || binary.LittleEndian.Uint32(record[20:24]) != 0x88000000 {
				continue
			}
			var raw [48]byte
			copy(raw[:], record)
			markers = append(markers, Marker{
				Position: binary.LittleEndian.Uint32(record[4:8]),
				Length:   binary.LittleEndian.Uint32(record[28:32]),
				TextID:   textID, Name: plainRTF(rtf), RTF: rtf, Raw: raw,
			})
		}
	}
	return markers
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
