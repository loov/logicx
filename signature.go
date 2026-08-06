// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/egonelbre/logicx/internal/record"
)

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

func findTimeSignatureChanges(chunks []Chunk) []TimeSignatureChange {
	var changes []TimeSignatureChange
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		for offset := 0; offset+16 <= len(chunk.Data); offset += 16 {
			change, ok := decodeTimeSignatureChange(chunk.Data[offset : offset+16])
			if !ok {
				continue
			}
			if offset+64 <= len(chunk.Data) {
				change.BeatGrouping, change.GroupingRaw = decodeBeatGrouping(chunk.Data[offset+40:offset+64], change.Numerator)
				change.GroupingFlags = change.GroupingRaw[6]
				change.PrintCompositeSignature = change.GroupingFlags&0x08 != 0
			}
			changes = append(changes, change)
		}
	}
	slices.SortFunc(changes, func(a, b TimeSignatureChange) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.PositionFraction, b.PositionFraction))
	})
	return changes
}

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
		record.Copy(0, change.Raw[:]),
	)
	if !ok || change.Numerator == 0 || denominatorPower > 7 || change.Flags&^byte(0x80) != 0 {
		return TimeSignatureChange{}, false
	}
	change.Denominator = uint16(1) << denominatorPower
	change.Position = signaturePosition(change.Position)
	return change, true
}

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

func findKeySignatureChanges(chunks []Chunk) []KeySignatureChange {
	var changes []KeySignatureChange
	for _, chunk := range chunks {
		if chunk.Type == "EvSq" {
			changes = append(changes, record.Scan(chunk.Data, 32, 16, decodeKeySignatureChange)...)
		}
	}
	slices.SortFunc(changes, func(a, b KeySignatureChange) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.PositionFraction, b.PositionFraction))
	})
	return changes
}

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

func signaturePosition(position uint32) uint32 {
	if position < projectStartTick {
		return projectStartTick
	}
	return position
}
