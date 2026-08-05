// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

// Chord is a timed harmony recovered from a region or the project chord lane.
// Pitches contains MIDI note numbers; Raw preserves its undocumented source
// record when one is available.
type Chord struct {
	Position         uint32
	PositionFraction uint16
	Duration         uint32
	Name             string
	Pitches          []uint8
	Raw              []byte
}
