// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

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
	eventNote  = 0x90
)

// Event is one record of an event sequence. Type is the record's discriminator
// and Data is the whole record, from its first atom through every atom that
// continues it.
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
// it.
//
// This boundary is what makes the decoders exact. Scanning for a record's
// leading bytes instead would also match the interior of a longer record: the
// third atom of a marker, for one, is byte-for-byte a plausible 1/1 meter.
func splitEvents(data []byte) []Event {
	var out []Event
	start := -1
	end := len(data) / atomSize * atomSize
	for offset := 0; offset < end; offset += atomSize {
		if data[offset+7]&eventContinues != 0 {
			continue
		}
		if start >= 0 {
			out = append(out, Event{Type: data[start], Offset: start, Data: data[start:offset]})
		}
		start = offset
	}
	if start >= 0 {
		out = append(out, Event{Type: data[start], Offset: start, Data: data[start:end]})
	}
	return out
}

// sequenceEvents returns the records of every event sequence in chunks, in
// file order, along with the chunk each came from.
func sequenceEvents(chunks []Chunk, visit func(chunk Chunk, event Event)) {
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		for _, event := range splitEvents(chunk.Data) {
			visit(chunk, event)
		}
	}
}
