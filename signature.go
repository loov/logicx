// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/egonelbre/logicx/internal/record"
)

// projectStartTick is the tick position of bar 1. Logic writes signature
// events that predate it; they all describe the project's initial signature.
const projectStartTick = 40 * 960

// TimeSignatureChange is a meter change, including Logic's optional beat
// grouping. Raw and GroupingRaw preserve the undecoded fields.
type TimeSignatureChange struct {
	Position                uint32
	PositionFraction        uint16
	Numerator               uint8
	Denominator             uint16
	BeatGrouping            []uint8
	GroupingFlags           uint8
	PrintCompositeSignature bool
	Flags                   uint8
	Raw                     [16]byte
	GroupingRaw             [24]byte
}

// KeySignatureChange is a major or minor key-signature change. Fifths uses
// MusicXML's negative-for-flats, positive-for-sharps convention.
type KeySignatureChange struct {
	Position         uint32
	PositionFraction uint16
	Fifths           int8
	Minor            bool
	Code             uint8
	Flags            uint8
	Raw              [32]byte
}

// findTimeSignatureChanges collects the meter map, sorted by position. A meter
// record carries a beat grouping only when it is long enough to hold one.
func findTimeSignatureChanges(chunks []Chunk) []TimeSignatureChange {
	var changes []TimeSignatureChange
	sequenceEvents(chunks, func(_ Chunk, event Event) {
		if event.Type != eventTimeSignature {
			return
		}
		change, ok := decodeTimeSignatureChange(event.Data)
		if !ok {
			return
		}
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
	ok := record.Decode(data,
		record.Equal(0, 0x30, 0),
		record.Uint16LE(2, &change.PositionFraction),
		record.Uint32LE(4, &change.Position),
		record.Equal(8, 0, 0, 0),
		record.Uint8(11, &denominatorPower),
		record.Uint8(12, &change.Numerator),
		record.Equal(13, 0, 0),
		record.Uint8(15, &change.Flags),
		record.Equal(16, 0x30, 0),
		record.Equal(23, 0x88),
		record.Copy(0, change.Raw[:]),
	)
	if !ok || change.Numerator == 0 || denominatorPower > 7 || change.Flags&^byte(0x80) != 0 {
		return TimeSignatureChange{}, false
	}
	change.Denominator = uint16(1) << denominatorPower
	change.Position = signaturePosition(change.Position)
	return change, true
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
func findKeySignatureChanges(chunks []Chunk) []KeySignatureChange {
	var changes []KeySignatureChange
	sequenceEvents(chunks, func(_ Chunk, event Event) {
		if event.Type != eventKeySignature {
			return
		}
		if change, ok := decodeKeySignatureChange(event.Data); ok {
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
	ok := record.Decode(data,
		record.Equal(0, 0x32, 0),
		record.Uint16LE(2, &change.PositionFraction),
		record.Uint32LE(4, &change.Position),
		record.Equal(8, 0, 0, 0, 0),
		record.Uint8(12, &change.Code),
		record.Equal(13, 0, 0),
		record.Uint8(15, &change.Flags),
		record.Equal(23, 0x88),
		record.Copy(0, change.Raw[:]),
	)
	index := change.Code & 0x0f
	if !ok || index > 14 || change.Code&^byte(0x1f) != 0 || change.Flags&^byte(0x80) != 0 {
		return KeySignatureChange{}, false
	}
	change.Position = signaturePosition(change.Position)
	change.Fifths = int8(index) - 7
	change.Minor = change.Code&0x10 != 0
	return change, true
}

// signaturePosition clamps pre-roll signature events onto bar 1.
func signaturePosition(position uint32) uint32 {
	if position < projectStartTick {
		return projectStartTick
	}
	return position
}
