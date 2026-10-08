// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/loov/logicx/internal/record"
)

// TempoChange is a sampled tempo-map value. Logic stores ramps as tempo
// samples, so BPM preserves both stepped and curved maps without interpolation.
type TempoChange struct {
	Position         uint32
	PositionFraction uint16
	BPM              float64
	// TODO(logicx): What do flags 0x01/0x40/0x80 and the records between curve
	// control points encode? 0x01 is set on every record of a map that may
	// have come from applying a region's tempo to the project; unconfirmed.
	Flags uint8
	Raw   [32]byte
	ref   eventRef
}

// findTempoChanges collects the tempo map from every event sequence, sorted by
// position. Logic keeps the map in a global sequence, so reading all of them
// costs little and survives layout changes.
func findTempoChanges(chunks []*Chunk) []TempoChange {
	var changes []TempoChange
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		if event.Type != eventTempo {
			return
		}
		if change, ok := decodeTempoChange(event.Data); ok {
			change.ref = eventRef{chunk, event}
			changes = append(changes, change)
		}
	})
	slices.SortFunc(changes, func(a, b TempoChange) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.PositionFraction, b.PositionFraction))
	})
	return changes
}

// fields is the layout of a tempo record. The BPM field is stored as beats
// per minute scaled by 10000, which value holds.
func (c *TempoChange) fields(value *uint32) []record.Field {
	return []record.Field{
		record.Equal(0, 0x60, 0),
		record.Uint16LE(2, &c.PositionFraction),
		record.Uint32LE(4, &c.Position),
		record.Equal(12, 0x7f, 0, 0),
		record.Uint8(15, &c.Flags),
		record.Uint32LE(16, value),
		record.Equal(23, 0x88),
	}
}

// maxTempoValue is the largest scaled BPM accepted, 1000 BPM.
const maxTempoValue = 10_000_000

// decodeTempoChange decodes a tempo record. Implausible values reject the
// record.
func decodeTempoChange(data []byte) (TempoChange, bool) {
	var change TempoChange
	var value uint32
	if !record.Decode(data, append(change.fields(&value), record.Copy(0, change.Raw[:]))...) ||
		value == 0 || value > maxTempoValue || change.Flags&^byte(0xc1) != 0 {
		return TempoChange{}, false
	}
	change.BPM = float64(value) / 10_000
	return change, true
}

// Save writes c's fields into the record it was decoded from, keeping the
// bytes this package does not decode, and moves the record when its position
// changed. ProjectData.TempoChanges is not updated; see
// [ProjectData.Refresh].
func (c *TempoChange) Save() error {
	value := math.Round(c.BPM * 10_000)
	if !(value >= 1 && value <= maxTempoValue) {
		return fmt.Errorf("logicx: tempo %v BPM out of range", c.BPM)
	}
	scaled := uint32(value)
	if err := c.ref.save("tempo change", c.fields(&scaled)...); err != nil {
		return err
	}
	copy(c.Raw[:], c.ref.event.Data)
	return nil
}

// Delete removes c's record from the project.
func (c *TempoChange) Delete() error { return c.ref.delete("tempo change") }

// Duplicate inserts a copy of c's record after it and returns the copy, to be
// changed and saved.
func (c *TempoChange) Duplicate() (TempoChange, error) {
	ref, err := c.ref.duplicate("tempo change")
	copied := *c
	copied.ref = ref
	return copied, err
}
