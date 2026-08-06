// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
)

// ProjectData contains both lossless chunks and decoded records.
type ProjectData struct {
	Header         [24]byte
	Chunks         []Chunk
	AudioUnits     []AudioUnit
	Tracks         []Track
	Sequences      []MIDISequence
	Markers        []Marker
	TempoChanges   []TempoChange
	TimeSignatures []TimeSignatureChange
	KeySignatures  []KeySignatureChange
	ProjectChords  []Chord
}

// chunkHeaderSize is the size of the header preceding every chunk's payload.
const chunkHeaderSize = 36

// Chunk is one lossless ProjectData record. Its semantics are undocumented;
// Header and Data preserve fields not decoded by this package.
type Chunk struct {
	Type   string
	Offset int
	Header [36]byte
	Data   []byte
}

// ParseProjectData parses bytes without opening or modifying any file.
func ParseProjectData(data []byte) (ProjectData, error) {
	chunks, err := parseChunks(data)
	if err != nil {
		return ProjectData{}, err
	}
	var header [24]byte
	copy(header[:], data[:24])
	p := ProjectData{
		Header: header, Chunks: chunks, AudioUnits: findAudioUnits(chunks),
		Tracks: findTracks(chunks), Sequences: findMIDISequences(chunks), Markers: findMarkers(chunks),
		TempoChanges:   findTempoChanges(chunks),
		TimeSignatures: findTimeSignatureChanges(chunks), KeySignatures: findKeySignatureChanges(chunks),
		ProjectChords: findProjectChords(chunks),
	}
	assignAudioUnits(p.Tracks, p.AudioUnits)
	return p, nil
}

// parseChunks splits ProjectData into its chunk records. Every chunk is
// retained, including types this package does not decode.
func parseChunks(data []byte) ([]Chunk, error) {
	if len(data) < 24 || !bytes.Equal(data[:4], []byte{0x23, 0x47, 0xc0, 0xab}) {
		return nil, errors.New("logicx: invalid ProjectData header")
	}
	var chunks []Chunk
	for offset := 24; offset < len(data); {
		if len(data)-offset < 36 {
			return nil, fmt.Errorf("logicx: truncated chunk header at offset %d", offset)
		}
		header := data[offset : offset+chunkHeaderSize]
		size := binary.LittleEndian.Uint64(header[28:36])
		if size > uint64(len(data)-offset-chunkHeaderSize) {
			return nil, fmt.Errorf("logicx: truncated chunk at offset %d", offset)
		}
		var rawHeader [chunkHeaderSize]byte
		copy(rawHeader[:], header)
		end := offset + chunkHeaderSize + int(size)
		chunks = append(chunks, Chunk{
			Type: reverse4(header[:4]), Offset: offset, Header: rawHeader,
			Data: bytes.Clone(data[offset+chunkHeaderSize : end]),
		})
		offset = end
	}
	return chunks, nil
}

// printableRun matches a run of printable ASCII long enough to be a name.
var printableRun = regexp.MustCompile(`[ -~]{4,}`)

// reverse4 reads a four-character code stored little-endian.
func reverse4(b []byte) string { return string([]byte{b[3], b[2], b[1], b[0]}) }

// printable4 reports whether b is a four-byte printable code.
func printable4(b []byte) bool { return len(b) == 4 && printable(b) }

// printable reports whether every byte is printable ASCII.
func printable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// allZero reports whether every byte is zero.
func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
