// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"

	"github.com/loov/logicx/internal/record"
)

// MIDIControlKind is the kind of a MIDI channel message other than a note,
// stored as its status byte without the channel.
type MIDIControlKind uint8

const (
	PolyPressure    MIDIControlKind = 0xa0
	ControlChange   MIDIControlKind = 0xb0
	ProgramChange   MIDIControlKind = 0xc0
	ChannelPressure MIDIControlKind = 0xd0
	PitchBend       MIDIControlKind = 0xe0
)

// MIDIControl is a control change, pitch bend, pressure or program change in
// a MIDI region. Number is the controller of a control change and the note of
// poly pressure, and zero otherwise. Value runs from 0 to 127, or to 16383
// for pitch bend, where 8192 is centre.
//
// The layout was found by importing MIDI files holding one message each.
// Messages drawn in Logic carry further atoms, as automation points do,
// which Raw keeps.
type MIDIControl struct {
	Kind             MIDIControlKind
	Channel          uint8
	Position         uint32
	SourcePosition   uint32
	PositionFraction uint16
	Number           uint8
	Value            uint16
	Raw              []byte
	ref              eventRef
}

const (
	controlValue  = 8
	controlNumber = 12
	// controlBendShift places pitch bend's 14 bits at the top of the stored
	// 31-bit value; other values keep their 7 bits in its top byte, with the
	// bits below filled as MIDI 2.0 scales a value up: zero up to 64, and
	// above it the value's low six bits repeated (85 is 0x55555555).
	controlBendShift  = 17
	controlValueShift = 24
)

// isControl reports whether an event type is a control message on any MIDI
// channel.
func isControl(eventType uint8) bool {
	switch MIDIControlKind(eventType & 0xf0) {
	case PolyPressure, ControlChange, ProgramChange, ChannelPressure, PitchBend:
		return true
	}
	return false
}

// fields is the layout of a control message's first atom.
func (c *MIDIControl) fields(status *uint8, value *uint32) []record.Field {
	return []record.Field{
		record.Uint8(0, status),
		record.Uint16LE(2, &c.PositionFraction),
		record.Uint32LE(4, &c.SourcePosition),
		record.Uint32LE(controlValue, value),
		record.Uint8(controlNumber, &c.Number),
	}
}

// decodeMIDIControl decodes a control message record.
func decodeMIDIControl(data []byte) (MIDIControl, bool) {
	if len(data) < atomSize || !isControl(data[0]) {
		return MIDIControl{}, false
	}
	var c MIDIControl
	var status uint8
	var value uint32
	if !record.Decode(data, c.fields(&status, &value)...) {
		return MIDIControl{}, false
	}
	c.Kind, c.Channel = MIDIControlKind(status&0xf0), status&0x0f+1
	c.Position = c.SourcePosition
	if c.Kind == PitchBend {
		c.Value = uint16(value >> controlBendShift)
	} else {
		c.Value = uint16(value >> controlValueShift)
	}
	if c.Kind != ControlChange && c.Kind != PolyPressure {
		c.Number = 0
	}
	c.Raw = bytes.Clone(data)
	return c, true
}

// findMIDIControls decodes the control messages of an event sequence.
func findMIDIControls(chunk *Chunk) []MIDIControl {
	var controls []MIDIControl
	for _, event := range chunk.Events {
		if c, ok := decodeMIDIControl(event.Data); ok {
			c.ref = eventRef{chunk, event}
			controls = append(controls, c)
		}
	}
	return controls
}
