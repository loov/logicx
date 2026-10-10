// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

// ArrangeTrack is one track of the Tracks area: a row playing a channel
// strip. A channel strip ([Track]) can be played by several, as Logic's
// Track > Other > New Track With Same Instrument makes; Logic also keeps a
// row for Stereo Out, shown by Track > Show Output Track, after the others.
type ArrangeTrack struct {
	// Row is the track's number in the Tracks area, from 1 at the top.
	Row int
	// Track is the name of the channel strip it plays, or empty when that
	// strip was not decoded.
	Track string
	// Output reports Logic's output track, which is always last.
	Output bool
	object uint32
	chunk  *Chunk
}

// Within an arrange track's chunk, besides its header's buttons (see
// [Track.Save]): its ID, and the bits Logic sets on the selected track.
const (
	arrangeTrackKind            = 0 // 1, or 3 for the output track
	arrangeTrackID              = 24
	arrangeTrackSelected        = 40 // 0x20
	arrangeTrackSelectedAlso    = 43 // 0x40
	arrangeTrackSelectedMinimum = arrangeTrackSelectedAlso + 1
)

// arrangeTrackOutput is the kind of Logic's output track.
const arrangeTrackOutput = 3

// The song chunk holds the selected track's row twice.
var songSelectedRow = []int{210, 214}

// findArrangeTrackRows decodes the arrange tracks, ordered by row. Logic ends
// the list with an empty chunk numbered 0x7fffffff, which is not a track.
func findArrangeTrackRows(chunks []*Chunk, tracks []Track) []ArrangeTrack {
	names := make(map[uint32]string)
	for _, t := range tracks {
		if t.environment != nil {
			names[chunkSequenceID(t.environment).sequence] = t.Name
		}
	}
	var rows []ArrangeTrack
	for _, chunk := range arrangeTrackChunks(chunks) {
		object := binary.LittleEndian.Uint32(chunk.Data[arrangeTrackEnvironment:])
		rows = append(rows, ArrangeTrack{
			Row: int(arrangeTrackNumber(chunk)) + 1, Track: names[object], object: object, chunk: chunk,
			Output: chunk.Data[arrangeTrackKind] == arrangeTrackOutput,
		})
	}
	return rows
}

// arrangeTrackChunks returns the arrange tracks' chunks ordered by row.
func arrangeTrackChunks(chunks []*Chunk) []*Chunk {
	var traks []*Chunk
	for _, chunk := range chunks {
		if chunk.Type == "Trak" && len(chunk.Data) >= arrangeTrackMinimum &&
			chunkSequenceID(chunk) == (chordSequenceID{arrangeTrackGroup, arrangeTrackSequence}) {
			traks = append(traks, chunk)
		}
	}
	slices.SortStableFunc(traks, func(a, b *Chunk) int { return cmp.Compare(arrangeTrackNumber(a), arrangeTrackNumber(b)) })
	return traks
}

// arrangeTrackNumber is an arrange track's row counted from zero, kept in its
// chunk header.
func arrangeTrackNumber(chunk *Chunk) uint32 {
	return binary.LittleEndian.Uint32(chunk.Header[arrangeTrackIndex:])
}

// DuplicateArrangeTrack adds a track below a playing the same channel strip,
// as Logic's New Track With Same Instrument does, and returns it. The tracks
// below move down a row, taking their regions along. The new track has a new
// ID and is neither selected nor armed. ProjectData.ArrangeTracks is not
// updated; see [ProjectData.Refresh].
func (p *ProjectData) DuplicateArrangeTrack(a *ArrangeTrack) (ArrangeTrack, error) {
	traks, at, err := p.arrangeTrack(a)
	if err != nil {
		return ArrangeTrack{}, err
	}
	if len(a.chunk.Data) < arrangeTrackSelectedMinimum {
		return ArrangeTrack{}, fmt.Errorf("logicx: arrange track %d is too short to copy", a.Row)
	}
	if a.chunk.Data[arrangeTrackKind] == arrangeTrackOutput {
		return ArrangeTrack{}, errors.New("logicx: the output track cannot be duplicated")
	}
	chunk := &Chunk{Type: a.chunk.Type, Header: a.chunk.Header, Data: bytes.Clone(a.chunk.Data)}
	chunk.Data[arrangeTrackFlags] &^= 0x01
	chunk.Data[arrangeTrackSelected] &^= 0x20
	chunk.Data[arrangeTrackSelectedAlso] &^= 0x40
	id := chunk.Data[arrangeTrackID : arrangeTrackID+16]
	rand.Read(id)
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80

	p.shiftRows(traks, a.Row+1, 1)
	binary.LittleEndian.PutUint32(chunk.Header[arrangeTrackIndex:], uint32(a.Row))
	p.Chunks = slices.Insert(p.Chunks, at+1, chunk)
	return ArrangeTrack{Row: a.Row + 1, Track: a.Track, object: a.object, chunk: chunk}, nil
}

// DeleteArrangeTrack removes a from the Tracks area; the tracks below move
// up a row, taking their regions along. It refuses while regions are on a:
// delete or move them first. The channel strip a played stays in the mixer,
// as a strip without a track, as Logic keeps one made in the mixer. Logic's
// Delete Track also removes the strip when no other track plays it; a project
// with only the strip's environment object removed makes Logic add a broken
// track in its place. ProjectData.ArrangeTracks is not updated; see
// [ProjectData.Refresh].
func (p *ProjectData) DeleteArrangeTrack(a *ArrangeTrack) error {
	traks, at, err := p.arrangeTrack(a)
	if err != nil {
		return err
	}
	for _, link := range p.arrangeLinks() {
		if linkRowOf(link) == a.Row && binary.LittleEndian.Uint32(link.Data[linkTrack:]) == a.object {
			return fmt.Errorf("logicx: arrange track %d has regions on it", a.Row)
		}
	}
	if a.chunk.Data[arrangeTrackKind] == arrangeTrackOutput {
		return errors.New("logicx: the output track cannot be deleted")
	}
	p.Chunks = slices.Delete(p.Chunks, at, at+1)
	// A track taking the deleted one's row keeps the selection on that row,
	// as in Logic, unless that is the output track or none: then the track
	// above is selected.
	next := slices.IndexFunc(traks, func(c *Chunk) bool { return arrangeTrackNumber(c) == arrangeTrackNumber(a.chunk)+1 })
	above := next < 0 || traks[next].Data[arrangeTrackKind] == arrangeTrackOutput
	p.shiftRows(traks, a.Row+1, -1)
	if song := p.songChunk(); song != nil && above {
		for _, at := range songSelectedRow {
			if row := binary.LittleEndian.Uint16(song.Data[at:]); int(row) == a.Row && row > 1 {
				binary.LittleEndian.PutUint16(song.Data[at:], row-1)
			}
		}
	}

	return nil
}

// arrangeTrack returns the arrange tracks' chunks by row and a's index in
// p.Chunks, checking that a is one of them.
func (p *ProjectData) arrangeTrack(a *ArrangeTrack) ([]*Chunk, int, error) {
	if a.chunk == nil {
		return nil, 0, errors.New("logicx: arrange track was not decoded from a project")
	}
	at := slices.Index(p.Chunks, a.chunk)
	traks := arrangeTrackChunks(p.Chunks)
	if at < 0 || !slices.Contains(traks, a.chunk) {
		return nil, 0, fmt.Errorf("logicx: arrange track %d is not in the project", a.Row)
	}
	if int(arrangeTrackNumber(a.chunk))+1 != a.Row {
		return nil, 0, fmt.Errorf("logicx: arrange track %d has moved to row %d", a.Row, arrangeTrackNumber(a.chunk)+1)
	}
	return traks, at, nil
}

// shiftRows moves the arrange tracks from row on by delta rows, with the
// regions on them and the song's selected row. Only a link naming the
// object of the track at its row is moved, so that a link this package does
// not understand is left as it was.
func (p *ProjectData) shiftRows(traks []*Chunk, row, delta int) {
	objects := make(map[int]uint32, len(traks))
	for _, chunk := range traks {
		objects[int(arrangeTrackNumber(chunk))+1] = binary.LittleEndian.Uint32(chunk.Data[arrangeTrackEnvironment:])
	}
	for _, link := range p.arrangeLinks() {
		r := linkRowOf(link)
		if object, ok := objects[r]; r >= row && ok && object == binary.LittleEndian.Uint32(link.Data[linkTrack:]) {
			binary.LittleEndian.PutUint16(link.Data[linkRow:], uint16(r+delta))
		}
	}
	for _, chunk := range traks {
		if n := int(arrangeTrackNumber(chunk)); n+1 >= row {
			binary.LittleEndian.PutUint32(chunk.Header[arrangeTrackIndex:], uint32(n+delta))
		}
	}
	for _, chunk := range p.Chunks {
		if chunk.Type == "MSeq" && chunkSequenceID(chunk) == (chordSequenceID{arrangeTrackGroup, arrangeTrackSequence}) && len(chunk.Data) >= arrangeRowsTail {
			rows := chunk.Data[len(chunk.Data)-arrangeRowsTail:]
			binary.LittleEndian.PutUint32(rows, uint32(int(binary.LittleEndian.Uint32(rows))+delta*arrangeRowUnits))
		}
	}
	if song := p.songChunk(); song != nil {
		for _, at := range songSelectedRow {
			if r := int(binary.LittleEndian.Uint16(song.Data[at:])); r >= row {
				binary.LittleEndian.PutUint16(song.Data[at:], uint16(r+delta))
			}
		}
	}
}

// The arrange sequence's descriptor counts the rows, at arrangeRowsTail from
// its end, in units of arrangeRowUnits. Logic reads a lower count than the
// rows as a row it fills with garbage. Every project seen has 60 a row,
// whatever its tracks' heights; a different unit would show as a count that
// is not a multiple of 60.
const (
	arrangeRowsTail = 269
	arrangeRowUnits = 60
)

// arrangeLinks returns the events placing regions on arrange tracks: MIDI
// region links and audio placements in the event sequence beside the arrange
// tracks.
func (p *ProjectData) arrangeLinks() []*Event {
	var links []*Event
	for _, chunk := range p.Chunks {
		if chunk.Type != "EvSq" || chunkSequenceID(chunk) != (chordSequenceID{arrangeTrackGroup, arrangeTrackSequence}) {
			continue
		}
		for _, event := range chunk.Events {
			if len(event.Data) < linkRow+2 {
				continue
			}
			if _, ok := decodeRegionLink(event.Data); ok || event.Type == eventAudioRegion && len(event.Data) >= audioPlacementMinimum {
				links = append(links, event)
			}
		}
	}
	return links
}

// linkRowOf reads the row a link places its region on.
func linkRowOf(link *Event) int { return int(binary.LittleEndian.Uint16(link.Data[linkRow:])) }

// songChunk returns the song chunk when it is long enough to hold the
// selected row, or nil.
func (p *ProjectData) songChunk() *Chunk {
	for _, chunk := range p.Chunks {
		if chunk.Type == songChunk && len(chunk.Data) >= songSelectedRow[1]+2 {
			return chunk
		}
	}
	return nil
}
