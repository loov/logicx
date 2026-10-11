// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io"

	"github.com/loov/logicx"
	"github.com/loov/logicx/internal/smf"
)

// midiVelocity is the velocity every exported note carries. Logic's note
// records hold a velocity but this package does not decode it yet.
const midiVelocity = 80

// writeMIDI renders one project alternative as a format 1 Standard MIDI File.
//
// Timing stays exactly as Logic recorded it, unquantized. That is the point of
// the file: notation programs quantize and detect tuplets when they import
// MIDI, and they do it better than a grid snap here can.
func writeMIDI(w io.Writer, alternative logicx.Alternative) error {
	// Realized chords, unquantized: this file exists for a program that would
	// rather quantize the raw performance itself.
	sequences := scoreSequences(alternative.Project, options{realizeChords: true})
	origin := scoreOrigin(alternative, sequences)

	tracks := [][]byte{smf.ConductorTrack(alternative, origin)}
	for i, sequence := range sequences {
		tracks = append(tracks, noteTrack(sequence.MIDISequence, origin, uint8(i%16)))
	}
	return smf.Write(w, tracks)
}

// noteTrack holds one part's notes and controllers, named so the importer can
// label the staff.
func noteTrack(sequence logicx.MIDISequence, origin uint32, channel uint8) []byte {
	events := []smf.Event{smf.Meta(0, 0x03, []byte(sequence.Name))}
	for _, note := range sequence.Notes {
		if note.Duration == 0 || note.Pitch > 127 {
			continue
		}
		start := smf.Relative(note.Position, origin)
		end := start + note.Duration
		// A note-off sorts before a note-on at the same tick, so a repeated
		// pitch is released before it sounds again.
		events = append(events,
			smf.Event{Tick: start, Order: 1, Data: []byte{0x90 | channel, note.Pitch, midiVelocity}},
			smf.Event{Tick: end, Order: 0, Data: []byte{0x80 | channel, note.Pitch, 0}})
	}
	for _, control := range sequence.Controls {
		if data := controlMessage(control, channel); data != nil {
			// Controllers sort before a note-on at the same tick, so a pedal
			// pressed with a note holds it.
			events = append(events, smf.Event{Tick: smf.Relative(control.Position, origin), Order: 0, Data: data})
		}
	}
	return smf.Assemble(events)
}

// controlMessage is the MIDI message of a control on channel.
func controlMessage(c logicx.MIDIControl, channel uint8) []byte {
	switch c.Kind {
	case logicx.ControlChange, logicx.PolyPressure:
		return []byte{uint8(c.Kind) | channel, c.Number & 0x7f, uint8(min(c.Value, 127))}
	case logicx.ProgramChange, logicx.ChannelPressure:
		return []byte{uint8(c.Kind) | channel, uint8(min(c.Value, 127))}
	case logicx.PitchBend:
		v := min(c.Value, 1<<14-1)
		return []byte{uint8(c.Kind) | channel, uint8(v & 0x7f), uint8(v >> 7)}
	}
	return nil
}
