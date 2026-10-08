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
	Chunks         []*Chunk
	AudioUnits     []AudioUnit
	Tracks         []Track
	Sequences      []MIDISequence
	Markers        []Marker
	TempoChanges   []TempoChange
	TimeSignatures []TimeSignatureChange
	KeySignatures  []KeySignatureChange
	ProjectChords  []Chord
	AudioFiles     []AudioFile
	AudioRegions   []AudioRegion
	Environment    []EnvironmentObject
}

// chunkHeaderSize is the size of the header preceding every chunk's payload.
const chunkHeaderSize = 36

// Chunk is one lossless ProjectData record. Its semantics are undocumented;
// Header and the payload preserve fields not decoded by this package.
//
// The payload is held in exactly one of Data and Events. An event sequence
// ("EvSq") is split into Events, so that records can be changed and resized
// on their own; every other chunk keeps its payload as Data. Offset is where
// the chunk began in the file as parsed; edits do not update it.
type Chunk struct {
	Type   string
	Offset int
	Header [36]byte
	Data   []byte
	Events []*Event
}

// Payload returns the chunk's payload bytes, joining Events when the payload
// is held as events.
func (c *Chunk) Payload() []byte {
	if c.Events == nil {
		return c.Data
	}
	var b []byte
	for _, event := range c.Events {
		b = append(b, event.Data...)
	}
	return b
}

// ParseProjectData parses bytes without opening or modifying any file.
func ParseProjectData(data []byte) (ProjectData, error) {
	chunks, err := parseChunks(data)
	if err != nil {
		return ProjectData{}, err
	}
	p := ProjectData{Header: [24]byte(data[:24]), Chunks: chunks}
	p.Refresh()
	return p, nil
}

// Refresh decodes p's values again from its Chunks. Save, Delete and
// Duplicate change the chunks rather than the values held by p, and values
// derived from several records — placed notes, chord durations, slurs — can
// only be recomputed from the whole tree.
func (p *ProjectData) Refresh() {
	chunks := p.Chunks
	*p = ProjectData{
		Header: p.Header, Chunks: chunks, AudioUnits: findAudioUnits(chunks),
		Tracks: findTracks(chunks), Sequences: findMIDISequences(chunks), Markers: findMarkers(chunks),
		TempoChanges:   findTempoChanges(chunks),
		TimeSignatures: findTimeSignatureChanges(chunks), KeySignatures: findKeySignatureChanges(chunks),
		ProjectChords: findProjectChords(chunks),
		AudioFiles:    findAudioFiles(chunks), AudioRegions: findAudioRegions(chunks),
		Environment: findEnvironment(chunks),
	}
	assignAudioUnits(p.Tracks, p.AudioUnits)
}

// parseChunks splits ProjectData into its chunk records. Every chunk is
// retained, including types this package does not decode.
func parseChunks(data []byte) ([]*Chunk, error) {
	if len(data) < 24 || !bytes.Equal(data[:4], []byte{0x23, 0x47, 0xc0, 0xab}) {
		return nil, errors.New("logicx: invalid ProjectData header")
	}
	// The header counts the bytes after it, so a truncated file shows here.
	if size := binary.LittleEndian.Uint64(data[16:24]); size != uint64(len(data)-24) {
		return nil, fmt.Errorf("logicx: ProjectData header says %d bytes follow, but %d do", size, len(data)-24)
	}
	var chunks []*Chunk
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
		chunk := &Chunk{
			Type: reverse4(header[:4]), Offset: offset, Header: rawHeader,
			Data: bytes.Clone(data[offset+chunkHeaderSize : end]),
		}
		// A payload that does not split exactly stays opaque, so that no byte
		// falls outside the tree.
		if chunk.Type == "EvSq" {
			if events, ok := splitEvents(chunk.Data); ok {
				chunk.Data, chunk.Events = nil, events
			}
		}
		chunks = append(chunks, chunk)
		offset = end
	}
	return chunks, nil
}

// AppendBinary appends the ProjectData file for p's Header and Chunks to b.
// Only the size fields are recomputed — the file header's byte count of
// everything after it, and each chunk header's payload size — so an
// unmodified parse writes back byte for byte. Decoded records are written
// through their Save methods, which change the chunks they came from.
func (p *ProjectData) AppendBinary(b []byte) ([]byte, error) {
	start := len(b)
	b = append(b, p.Header[:]...)
	for _, c := range p.Chunks {
		at := len(b)
		b = append(b, c.Header[:]...)
		if c.Events == nil {
			b = append(b, c.Data...)
		}
		for _, event := range c.Events {
			b = append(b, event.Data...)
		}
		binary.LittleEndian.PutUint64(b[at+28:at+36], uint64(len(b)-at-chunkHeaderSize))
	}
	binary.LittleEndian.PutUint64(b[start+16:start+24], uint64(len(b)-start-24))
	return b, nil
}

// MarshalBinary returns the ProjectData file for p; see [ProjectData.AppendBinary].
func (p *ProjectData) MarshalBinary() ([]byte, error) { return p.AppendBinary(nil) }

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
