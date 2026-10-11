// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"fmt"

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
	// value is Value as decoded, so that Save keeps the stored form of an
	// unchanged value.
	value uint16
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

// controlMaximum is the flag at byte 1 that Logic sets on a value at the top
// of its range. It was only ever seen with such values.
const (
	controlFlags   = 1
	controlMaximum = 0x20
)

// fields is the layout of a control message's first atom. Byte 12 holds the
// number, or the low seven bits of a pitch bend.
func (c *MIDIControl) fields(status *uint8, value *uint32, number *uint8) []record.Field {
	return []record.Field{
		record.Uint8(0, status),
		record.Uint16LE(2, &c.PositionFraction),
		record.Uint32LE(4, &c.SourcePosition),
		record.Uint32LE(controlValue, value),
		record.Uint8(controlNumber, number),
	}
}

// decodeMIDIControl decodes a control message record.
func decodeMIDIControl(data []byte) (MIDIControl, bool) {
	if len(data) < atomSize || !isControl(data[0]) {
		return MIDIControl{}, false
	}
	var c MIDIControl
	var status, number uint8
	var value uint32
	if !record.Decode(data, c.fields(&status, &value, &number)...) {
		return MIDIControl{}, false
	}
	c.Kind, c.Channel = MIDIControlKind(status&0xf0), status&0x0f+1
	c.Position = c.SourcePosition
	if c.Kind == PitchBend {
		c.Value = uint16(value >> controlBendShift)
	} else {
		c.Value = uint16(value >> controlValueShift)
	}
	if c.Kind == ControlChange || c.Kind == PolyPressure {
		c.Number = number
	}
	c.value, c.Raw = c.Value, bytes.Clone(data)
	return c, true
}

// maximum is the largest Value of c's kind.
func (c *MIDIControl) maximum() uint16 {
	if c.Kind == PitchBend {
		return 1<<14 - 1
	}
	return 127
}

// Save writes c's kind, channel, position, number and value into its event,
// keeping the sequence in time order. A changed value is stored as Logic
// stores an imported one. The region's Controls are not updated; see
// [ProjectData.Refresh].
func (c *MIDIControl) Save() error {
	if !isControl(uint8(c.Kind)) || c.Kind&0x0f != 0 || c.Channel < 1 || c.Channel > 16 || c.Number > 127 || c.Value > c.maximum() {
		return fmt.Errorf("logicx: control kind %#x, channel %d, number %d or value %d out of range", uint8(c.Kind), c.Channel, c.Number, c.Value)
	}
	if err := c.ref.check("control"); err != nil {
		return err
	}
	data := c.ref.event.Data
	status := uint8(c.Kind) | (c.Channel - 1)
	value := binary.LittleEndian.Uint32(data[controlValue:])
	number := data[controlNumber]
	switch {
	case c.Kind == ControlChange || c.Kind == PolyPressure:
		number = c.Number
	case c.Kind != MIDIControlKind(data[0]&0xf0):
		// A kind without a number starts from zero; some kept from the
		// file are not, so an unchanged kind keeps its byte.
		number = 0
	}
	if c.Value != c.value || c.Kind != MIDIControlKind(data[0]&0xf0) {
		switch c.Kind {
		case PitchBend:
			value, number = uint32(c.Value)<<controlBendShift, uint8(c.Value&0x7f)
		case ProgramChange:
			value = uint32(c.Value) << controlValueShift
		default:
			value = upscaleControl(uint8(c.Value))
		}
		data[controlFlags] &^= controlMaximum
		if c.Value == c.maximum() {
			data[controlFlags] |= controlMaximum
			value = 0x7fffffff
		}
	}
	if err := c.ref.save("control", c.fields(&status, &value, &number)...); err != nil {
		return err
	}
	c.value, c.Raw = c.Value, bytes.Clone(c.ref.event.Data)
	return nil
}

// Delete removes c from its region.
func (c *MIDIControl) Delete() error { return c.ref.delete("control") }

// Duplicate inserts a copy of c's record after it and returns the copy, to be
// changed and saved.
func (c *MIDIControl) Duplicate() (MIDIControl, error) {
	ref, err := c.ref.duplicate("control")
	if err != nil {
		return MIDIControl{}, err
	}
	copied := *c
	copied.ref, copied.Raw = ref, bytes.Clone(c.Raw)
	return copied, nil
}

// upscaleControl stores a 7-bit value in the top byte of 31 bits, with the
// bits below filled as MIDI 2.0 scales a value up: zero up to the centre,
// and above it the value's low six bits repeated.
func upscaleControl(v uint8) uint32 {
	scaled := uint32(v) << controlValueShift
	if v <= 64 {
		return scaled
	}
	low := uint32(v & 0x3f)
	for shift := controlValueShift - 6; shift > -6; shift -= 6 {
		if shift >= 0 {
			scaled |= low << shift
		} else {
			scaled |= low >> -shift
		}
	}
	return scaled
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
