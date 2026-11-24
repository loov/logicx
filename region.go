// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"slices"
)

// Within an MSeq payload: a tag that is unique among the project's
// sequences, which the event sequence's header repeats, and, after the name,
// the sequence's own number. Every project seen keeps the tag below 256, but
// it is stored in four bytes.
const (
	sequenceTag = 8
	sequenceOwn = 172 // from the end of the name
	eventsTag   = 14  // in the EvSq chunk header
)

// CopyRegion adds a region playing a copy of s's source, as Logic's copy and
// paste does: the copy is no alias, and editing it leaves s alone. It is
// placed where s is, on the same track; change its Position, Row or Track and
// Save it to move it. Notes are copied too: change, delete or duplicate the
// returned sequence's notes as any others. ProjectData.Sequences is not
// updated; see [ProjectData.Refresh].
func (p *ProjectData) CopyRegion(s *MIDISequence) (MIDISequence, error) {
	if s.link.event == nil || s.descriptor == nil {
		return MIDISequence{}, errors.New("logicx: region is not placed in a project")
	}
	if err := s.link.check("region"); err != nil {
		return MIDISequence{}, err
	}
	// An alias's own source holds no events; the copy takes its placement
	// and a copy of the events it plays.
	own := chunkSequenceID(s.descriptor)
	events := own
	if s.Alias {
		events.sequence = binary.LittleEndian.Uint32(s.link.event.Data[8:])
	}
	id, err := p.copySequence(own, events)
	if err != nil {
		return MIDISequence{}, fmt.Errorf("logicx: region %q: %w", s.Name, err)
	}
	link, err := s.link.duplicate("region")
	if err != nil {
		return MIDISequence{}, err
	}
	// A plain region names its own source twice; an alias names its target
	// in the first, and the copy plays the copied source rather than it.
	binary.LittleEndian.PutUint32(link.event.Data[8:], id)
	binary.LittleEndian.PutUint32(link.event.Data[32:], id)

	for _, copied := range findMIDISequences(p.Chunks) {
		if copied.link.event == link.event {
			one := []MIDISequence{copied}
			assignRegionTracks(one, nil, p.Tracks, p.ArrangeTracks)
			return one[0], nil
		}
	}
	return MIDISequence{}, fmt.Errorf("logicx: copy of region %q did not decode", s.Name)
}

// DeleteRegion removes s from the arrangement, and its source with it when
// no other region places that source. ProjectData.Sequences is not updated;
// see [ProjectData.Refresh].
func (p *ProjectData) DeleteRegion(s *MIDISequence) error {
	if s.link.event == nil || s.descriptor == nil {
		return errors.New("logicx: region is not placed in a project")
	}
	if err := s.link.delete("region"); err != nil {
		return err
	}
	if s.shared {
		return nil
	}
	id := chunkSequenceID(s.descriptor)
	p.Chunks = slices.DeleteFunc(p.Chunks, func(c *Chunk) bool { return chunkSequenceID(c) == id })
	return nil
}

// copySequence copies the chunks numbered id — a source's descriptor and the
// track chunk beside it — and the event sequence numbered events to a new
// sequence number in the same group, after the last of them, and returns the
// number, the first free one after id. The copy gets a tag no other
// sequence has.
func (p *ProjectData) copySequence(id, events chordSequenceID) (uint32, error) {
	if id.group != arrangeTrackGroup {
		return 0, fmt.Errorf("sequence group %d is not the arrangement's", id.group)
	}
	last := -1
	tags := make(map[uint32]bool)
	// Sequence numbers are unique across groups: a sequence in another
	// group keeps its track chunk in this one.
	numbers := make(map[uint32]bool)
	for i, c := range p.Chunks {
		cid := chunkSequenceID(c)
		numbers[cid.sequence] = true
		if c.Type == "MSeq" && len(c.Data) >= sequenceTag+4 {
			tags[binary.LittleEndian.Uint32(c.Data[sequenceTag:])] = true
		}
		if cid == id || cid == events {
			last = max(last, i)
		}
	}
	if last < 0 {
		return 0, errors.New("source sequence not found")
	}
	// The first free number after the source, as Logic fills gaps.
	next := id.sequence + 4
	for numbers[next] {
		next += 4
	}
	tag, err := unusedTag(tags)
	if err != nil {
		return 0, err
	}

	var copies []*Chunk
	for _, c := range p.Chunks {
		if cid := chunkSequenceID(c); c.Type == "EvSq" && cid != events || c.Type != "EvSq" && cid != id {
			continue
		}
		copied := &Chunk{Type: c.Type, Header: c.Header, Data: bytes.Clone(c.Data)}
		for _, e := range c.Events {
			copied.Events = append(copied.Events, &Event{Type: e.Type, Offset: e.Offset, Data: bytes.Clone(e.Data)})
		}
		binary.LittleEndian.PutUint32(copied.Header[10:], next)
		switch c.Type {
		case "MSeq":
			tail, ok := sequenceTail(copied.Data)
			if !ok || tail+sequenceOwn+4 > len(copied.Data) || len(copied.Data) < sequenceTag+4 {
				return 0, errors.New("source descriptor is too short to copy")
			}
			binary.LittleEndian.PutUint32(copied.Data[sequenceTag:], tag)
			binary.LittleEndian.PutUint32(copied.Data[tail+sequenceOwn:], next)
		case "EvSq":
			binary.LittleEndian.PutUint32(copied.Header[eventsTag:], tag)
		}
		copies = append(copies, copied)
	}
	p.Chunks = slices.Insert(p.Chunks, last+1, copies...)
	if err := p.registerObject(chordSequenceID{id.group, next}); err != nil {
		return 0, err
	}
	return next, nil
}

// unusedTag picks a sequence tag no sequence has, below 256 while one is
// free, at random as Logic's appear to be.
func unusedTag(used map[uint32]bool) (uint32, error) {
	var free []uint32
	for tag := uint32(1); tag < 256; tag++ {
		if !used[tag] {
			free = append(free, tag)
		}
	}
	if len(free) == 0 {
		for tag := uint32(256); ; tag++ {
			if !used[tag] {
				return tag, nil
			}
		}
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(free))))
	if err != nil {
		return 0, err
	}
	return free[n.Int64()], nil
}
