// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"cmp"
	"slices"

	"github.com/egonelbre/logicx/internal/record"
)

// TempoChange is a sampled tempo-map value. Logic stores ramps as tempo
// samples, so BPM preserves both stepped and curved maps without interpolation.
type TempoChange struct {
	Position         uint32
	PositionFraction uint16
	BPM              float64
	// TODO(logicx): What do flags 0x40/0x80 and the records between curve
	// control points encode?
	Flags uint8
	Raw   [32]byte
}

func findTempoChanges(chunks []Chunk) []TempoChange {
	var changes []TempoChange
	for _, chunk := range chunks {
		if chunk.Type == "EvSq" {
			changes = append(changes, record.Scan(chunk.Data, 32, 16, decodeTempoChange)...)
		}
	}
	slices.SortFunc(changes, func(a, b TempoChange) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.PositionFraction, b.PositionFraction))
	})
	return changes
}

func decodeTempoChange(data []byte) (TempoChange, bool) {
	var change TempoChange
	var value uint32
	ok := record.Decode(data,
		record.Equal(0, 0x60, 0),
		record.Uint16LE(2, &change.PositionFraction),
		record.Uint32LE(4, &change.Position),
		record.Equal(12, 0x7f, 0, 0),
		record.Uint8(15, &change.Flags),
		record.Uint32LE(16, &value),
		record.Equal(23, 0x88),
		record.Copy(0, change.Raw[:]),
	)
	if !ok || value == 0 || value > 10_000_000 || change.Flags&^byte(0xc0) != 0 {
		return TempoChange{}, false
	}
	change.BPM = float64(value) / 10_000
	return change, true
}
