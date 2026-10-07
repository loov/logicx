// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"math/bits"
	"slices"

	"github.com/loov/logicx/internal/record"
)

// projectStartTick is the tick position of bar 1. Logic writes signature
// events that predate it; they all describe the project's initial signature.
const projectStartTick = 40 * 960

// TimeSignatureChange is a meter change, including Logic's optional beat
// grouping. Raw and GroupingRaw preserve the undecoded fields.
//
// Position is where the change takes effect: Logic writes changes that
// predate bar 1, which all take effect there. SourcePosition is the position
// as stored, and is what Save writes.
type TimeSignatureChange struct {
	Position                uint32
	SourcePosition          uint32
	PositionFraction        uint16
	Numerator               uint8
	Denominator             uint16
	BeatGrouping            []uint8
	GroupingFlags           uint8
	PrintCompositeSignature bool
	Flags                   uint8
	Raw                     [16]byte
	GroupingRaw             [24]byte
	ref                     eventRef
}

// KeySignatureChange is a major or minor key-signature change. Fifths uses
// MusicXML's negative-for-flats, positive-for-sharps convention. Code is the
// stored form of Fifths and Minor, which Save writes from them.
//
// Position and SourcePosition are as for [TimeSignatureChange].
type KeySignatureChange struct {
	Position         uint32
	SourcePosition   uint32
	PositionFraction uint16
	Fifths           int8
	Minor            bool
	Code             uint8
	Flags            uint8
	Raw              [32]byte
	ref              eventRef
}

// findTimeSignatureChanges collects the meter map, sorted by position. A meter
// record carries a beat grouping only when it is long enough to hold one.
func findTimeSignatureChanges(chunks []*Chunk) []TimeSignatureChange {
	var changes []TimeSignatureChange
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		if event.Type != eventTimeSignature {
			return
		}
		change, ok := decodeTimeSignatureChange(event.Data)
		if !ok {
			return
		}
		change.ref = eventRef{chunk, event}
		if len(event.Data) >= 64 {
			change.BeatGrouping, change.GroupingRaw = decodeBeatGrouping(event.Data[40:64], change.Numerator)
			change.GroupingFlags = change.GroupingRaw[6]
			change.PrintCompositeSignature = change.GroupingFlags&0x08 != 0
		}
		changes = append(changes, change)
	})
	slices.SortFunc(changes, func(a, b TimeSignatureChange) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.PositionFraction, b.PositionFraction))
	})
	return changes
}

// decodeTimeSignatureChange decodes a meter record. The denominator is stored
// as a power of two.
//
// The second atom repeating the event type is checked as a consistency guard;
// the record boundary is what keeps the middle of a marker record, which is
// otherwise a plausible 1/1 meter, from decoding as one.
func decodeTimeSignatureChange(data []byte) (TimeSignatureChange, bool) {
	var change TimeSignatureChange
	var denominatorPower uint8
	ok := record.Decode(data, append(change.fields(&denominatorPower), record.Copy(0, change.Raw[:]))...)
	if !ok || change.Numerator == 0 || denominatorPower > 7 || change.Flags&^byte(0x80) != 0 {
		return TimeSignatureChange{}, false
	}
	change.Denominator = uint16(1) << denominatorPower
	change.Position = signaturePosition(change.SourcePosition)
	return change, true
}

// fields is the layout of a meter record. The denominator is stored as a
// power of two, which denominatorPower holds.
func (c *TimeSignatureChange) fields(denominatorPower *uint8) []record.Field {
	return []record.Field{
		record.Equal(0, 0x30, 0),
		record.Uint16LE(2, &c.PositionFraction),
		record.Uint32LE(4, &c.SourcePosition),
		record.Equal(8, 0, 0, 0),
		record.Uint8(11, denominatorPower),
		record.Uint8(12, &c.Numerator),
		record.Equal(13, 0, 0),
		record.Uint8(15, &c.Flags),
		record.Equal(16, 0x30, 0),
		record.Equal(23, 0x88),
	}
}

// Save writes c's fields into the record it was decoded from, keeping the
// bytes this package does not decode, and moves the record when its position
// changed. A non-nil BeatGrouping is written along with GroupingFlags and
// PrintCompositeSignature; it needs a record that already holds a grouping,
// and at most three groups. ProjectData.TimeSignatures is not updated; see
// [ProjectData.Refresh].
func (c *TimeSignatureChange) Save() error {
	if c.Numerator == 0 || c.Denominator == 0 || c.Denominator > 128 || c.Denominator&(c.Denominator-1) != 0 {
		return fmt.Errorf("logicx: time signature %d/%d is not valid", c.Numerator, c.Denominator)
	}
	power := uint8(bits.TrailingZeros16(c.Denominator))
	fields := c.fields(&power)
	if c.BeatGrouping != nil {
		if err := c.ref.check("time signature"); err != nil {
			return err
		}
		if len(c.ref.event.Data) < 64 {
			return errors.New("logicx: time signature record has no beat grouping")
		}
		var total int
		for _, group := range c.BeatGrouping {
			if group == 0 {
				return errors.New("logicx: beat grouping holds an empty group")
			}
			total += int(group)
		}
		if total != int(c.Numerator) || len(c.BeatGrouping) > 3 {
			return fmt.Errorf("logicx: beat grouping %v does not fit %d/%d", c.BeatGrouping, c.Numerator, c.Denominator)
		}
		// Groups are stored last-first, ending at the atom's continuation byte.
		var stored [3]byte
		for i, group := range c.BeatGrouping {
			stored[2-i] = group
		}
		flags := c.GroupingFlags &^ 0x08
		if c.PrintCompositeSignature {
			flags |= 0x08
		}
		fields = append(fields, record.Uint8(46, &flags), record.Copy(52, stored[:]))
	}
	if err := c.ref.save("time signature", fields...); err != nil {
		return err
	}
	copy(c.Raw[:], c.ref.event.Data)
	if c.BeatGrouping != nil {
		copy(c.GroupingRaw[:], c.ref.event.Data[40:])
		c.GroupingFlags = c.GroupingRaw[6]
	}
	return nil
}

// Delete removes c's record from the project.
func (c *TimeSignatureChange) Delete() error { return c.ref.delete("time signature") }

// Duplicate inserts a copy of c's record after it and returns the copy, to be
// changed and saved.
func (c *TimeSignatureChange) Duplicate() (TimeSignatureChange, error) {
	ref, err := c.ref.duplicate("time signature")
	copied := *c
	copied.BeatGrouping = slices.Clone(c.BeatGrouping)
	copied.ref = ref
	return copied, err
}

// decodeBeatGrouping decodes the composite-meter beat grouping that follows a
// meter record. Groups are stored last-first and must sum to the numerator;
// anything else is treated as an unrelated record and yields a nil grouping.
func decodeBeatGrouping(data []byte, numerator uint8) ([]uint8, [24]byte) {
	var raw [24]byte
	if len(data) < len(raw) {
		return nil, raw
	}
	copy(raw[:], data)
	// TODO(logicx): What does the always-observed grouping flag 0x04 mean?
	if !allZero(data[:6]) || data[6] == 0 || !allZero(data[7:12]) {
		return nil, raw
	}
	end := bytes.IndexByte(data[12:], 0x88)
	if end <= 0 {
		return nil, raw
	}
	end += 12
	start := 12
	for start < end && data[start] == 0 {
		start++
	}
	groups := slices.Clone(data[start:end])
	var total uint8
	for _, group := range groups {
		if group == 0 || group > numerator-total {
			return nil, raw
		}
		total += group
	}
	if total != numerator || !allZero(data[end+1:]) {
		return nil, raw
	}
	slices.Reverse(groups)
	return groups, raw
}

// findKeySignatureChanges collects the key map, sorted by position.
func findKeySignatureChanges(chunks []*Chunk) []KeySignatureChange {
	// Chord regions carry a key record of their own. Only the signature track
	// holds the project key map, and it is the sequence holding the meters.
	signatureTrack := make(map[chordSequenceID]bool)
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		if event.Type != eventTimeSignature {
			return
		}
		if _, ok := decodeTimeSignatureChange(event.Data); ok {
			signatureTrack[chunkSequenceID(chunk)] = true
		}
	})

	var changes []KeySignatureChange
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		if event.Type != eventKeySignature || len(signatureTrack) != 0 && !signatureTrack[chunkSequenceID(chunk)] {
			return
		}
		if change, ok := decodeKeySignatureChange(event.Data); ok {
			change.ref = eventRef{chunk, event}
			changes = append(changes, change)
		}
	})
	slices.SortFunc(changes, func(a, b KeySignatureChange) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.PositionFraction, b.PositionFraction))
	})
	return changes
}

// decodeKeySignatureChange decodes a key record. The low nibble of the
// code counts fifths from Cb, and bit 0x10 marks a minor key.
func decodeKeySignatureChange(data []byte) (KeySignatureChange, bool) {
	var change KeySignatureChange
	ok := record.Decode(data, append(change.fields(), record.Copy(0, change.Raw[:]))...)
	index := change.Code & 0x0f
	if !ok || index > 14 || change.Code&^byte(0x1f) != 0 || change.Flags&^byte(0x80) != 0 {
		return KeySignatureChange{}, false
	}
	change.Position = signaturePosition(change.SourcePosition)
	change.Fifths = int8(index) - 7
	change.Minor = change.Code&0x10 != 0
	return change, true
}

// fields is the layout of a key record.
func (c *KeySignatureChange) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0x32, 0),
		record.Uint16LE(2, &c.PositionFraction),
		record.Uint32LE(4, &c.SourcePosition),
		record.Equal(8, 0, 0, 0, 0),
		record.Uint8(12, &c.Code),
		record.Equal(13, 0, 0),
		record.Uint8(15, &c.Flags),
		record.Equal(23, 0x88),
	}
}

// Save writes c's fields into the record it was decoded from, keeping the
// bytes this package does not decode, and moves the record when its position
// changed. Code is set from Fifths and Minor. ProjectData.KeySignatures is not
// updated; see [ProjectData.Refresh].
func (c *KeySignatureChange) Save() error {
	if c.Fifths < -7 || c.Fifths > 7 {
		return fmt.Errorf("logicx: key signature with %d fifths", c.Fifths)
	}
	c.Code = uint8(c.Fifths + 7)
	if c.Minor {
		c.Code |= 0x10
	}
	if err := c.ref.save("key signature", c.fields()...); err != nil {
		return err
	}
	copy(c.Raw[:], c.ref.event.Data)
	return nil
}

// Delete removes c's record from the project.
func (c *KeySignatureChange) Delete() error { return c.ref.delete("key signature") }

// Duplicate inserts a copy of c's record after it and returns the copy, to be
// changed and saved.
func (c *KeySignatureChange) Duplicate() (KeySignatureChange, error) {
	ref, err := c.ref.duplicate("key signature")
	copied := *c
	copied.ref = ref
	return copied, err
}

// signaturePosition clamps pre-roll signature events onto bar 1.
func signaturePosition(position uint32) uint32 {
	if position < projectStartTick {
		return projectStartTick
	}
	return position
}
