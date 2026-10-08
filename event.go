// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/loov/logicx/internal/record"
)

// atomSize is the granularity of an event sequence. Every EvSq payload is a
// whole number of these.
const atomSize = 16

// eventContinues is the bit set in byte 7 of an atom that belongs to the
// preceding event. Its value doubles as a per-event-type tag, so only the bit
// is meaningful here.
const eventContinues = 0x80

// Event types, taken from byte 0 of a record's first atom. Types this package
// does not decode are left out.
const (
	eventMarker        = 0x12
	eventLink          = 0x20
	eventTimeSignature = 0x30
	eventKeySignature  = 0x32
	eventTempo         = 0x60
	// eventScore covers the positioned score symbols, lyrics and chord events,
	// which share a type and are told apart by their discriminator at byte 12.
	eventScore = 0x70
	// eventNote is a MIDI note-on status byte: its low four bits hold the
	// MIDI channel, so a note is any type from 0x90 to 0x9f.
	eventNote = 0x90
)

// isNote reports whether an event type is a note on any MIDI channel.
func isNote(eventType byte) bool { return eventType&0xf0 == eventNote }

// Event is one record of an event sequence. Type is the record's discriminator
// and Data is the whole record, from its first atom through every atom that
// continues it. Offset is where the record began in the payload as parsed;
// edits do not update it.
//
// Records are variable-length and the length carries meaning: a 48-byte meter
// event has no beat grouping while a 64-byte one does, and a note event grows
// by one atom per attached score symbol.
type Event struct {
	Type   byte
	Offset int
	Data   []byte
}

// splitEvents divides an event sequence into records. A record begins at an
// atom whose continuation bit is clear and runs through every atom that sets
// it. It reports false when data is not a whole number of atoms or begins
// partway through a record, which would leave bytes outside every record.
//
// This boundary is what makes the decoders exact. Scanning for a record's
// leading bytes instead would also match the interior of a longer record: the
// third atom of a marker, for one, is byte-for-byte a plausible 1/1 meter.
func splitEvents(data []byte) ([]*Event, bool) {
	if len(data)%atomSize != 0 || len(data) != 0 && data[7]&eventContinues != 0 {
		return nil, false
	}
	var out []*Event
	start := 0
	for offset := atomSize; offset <= len(data); offset += atomSize {
		if offset < len(data) && data[offset+7]&eventContinues != 0 {
			continue
		}
		// Clipped, so that growing one record never writes into the next.
		out = append(out, &Event{Type: data[start], Offset: start, Data: data[start:offset:offset]})
		start = offset
	}
	return out, true
}

// sequenceEvents visits the records of every event sequence in chunks, in
// file order, along with the chunk each came from.
func sequenceEvents(chunks []*Chunk, visit func(chunk *Chunk, event *Event)) {
	for _, chunk := range chunks {
		for _, event := range chunk.Events {
			visit(chunk, event)
		}
	}
}

// eventSentinel is the record that ends every event sequence.
const eventSentinel = 0xf1

// eventRef locates a decoded record in the tree, so that a decoded value can
// write itself back, be removed or be copied.
type eventRef struct {
	chunk *Chunk
	event *Event
}

// check reports an error when the value was not decoded from a project or
// its record has been deleted. It and reorder scan the sequence, so saving
// every record of a sequence is quadratic in its length; an index from event
// to position would fix that should sequences of many thousands of events
// need saving in bulk.
func (r eventRef) check(what string) error {
	if r.event == nil || !slices.Contains(r.chunk.Events, r.event) {
		return fmt.Errorf("logicx: %s is not in a project", what)
	}
	return nil
}

// save writes fields over the record and moves it when its position changed,
// keeping the sequence in position order.
func (r eventRef) save(what string, fields ...record.Field) error {
	if err := r.check(what); err != nil {
		return err
	}
	data, err := record.Encode(r.event.Data, fields...)
	if err != nil {
		return fmt.Errorf("logicx: %s: %w", what, err)
	}
	r.event.Data, r.event.Type = data, data[0]
	r.chunk.reorder(r.event)
	return nil
}

// delete removes the record from its sequence.
func (r eventRef) delete(what string) error {
	if err := r.check(what); err != nil {
		return err
	}
	r.chunk.Events = slices.DeleteFunc(r.chunk.Events, func(e *Event) bool { return e == r.event })
	return nil
}

// duplicate inserts a copy of the record right after it and returns a
// reference to the copy.
func (r eventRef) duplicate(what string) (eventRef, error) {
	if err := r.check(what); err != nil {
		return eventRef{}, err
	}
	i := slices.Index(r.chunk.Events, r.event)
	copied := &Event{Type: r.event.Type, Offset: r.event.Offset, Data: bytes.Clone(r.event.Data)}
	r.chunk.Events = slices.Insert(r.chunk.Events, i+1, copied)
	return eventRef{r.chunk, copied}, nil
}

// eventPosition is the sort key of a record: its position and fraction, with
// the sentinel after everything.
func eventPosition(e *Event) uint64 {
	if e.Type == eventSentinel || len(e.Data) < 8 {
		return math.MaxUint64
	}
	return uint64(binary.LittleEndian.Uint32(e.Data[4:]))<<16 | uint64(binary.LittleEndian.Uint16(e.Data[2:]))
}

// reorder moves event so that the sequence stays in position order. A record
// already in order is left where it is, so that records sharing a position
// keep their relative order; a moved one goes after the records at its new
// position.
func (c *Chunk) reorder(event *Event) {
	i := slices.Index(c.Events, event)
	at := eventPosition(event)
	if (i == 0 || eventPosition(c.Events[i-1]) <= at) && (i+1 == len(c.Events) || at <= eventPosition(c.Events[i+1])) {
		return
	}
	events := slices.Delete(c.Events, i, i+1)
	j, _ := slices.BinarySearchFunc(events, at, func(e *Event, at uint64) int {
		if eventPosition(e) <= at {
			return -1
		}
		return 1
	})
	c.Events = slices.Insert(events, j, event)
}

// atomRef locates a 16-byte atom within a record, for the score symbols a
// note record carries in its trailing atoms.
type atomRef struct {
	eventRef
	offset int
}

// atom returns the atom's bytes.
func (r atomRef) atom() []byte { return r.event.Data[r.offset : r.offset+atomSize] }

// save writes fields over the atom; their offsets are within the atom.
func (r atomRef) save(what string, fields ...record.Field) error {
	if err := r.check(what); err != nil {
		return err
	}
	if r.offset+atomSize > len(r.event.Data) {
		return fmt.Errorf("logicx: %s is no longer in its record", what)
	}
	atom, err := record.Encode(r.atom(), fields...)
	if err != nil {
		return fmt.Errorf("logicx: %s: %w", what, err)
	}
	copy(r.atom(), atom)
	return nil
}

// events returns the chunk's events, or none for a missing chunk.
func (c *Chunk) events() []*Event {
	if c == nil {
		return nil
	}
	return c.Events
}
