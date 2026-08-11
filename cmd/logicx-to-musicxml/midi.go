// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"io"
	"math"
	"slices"

	"github.com/loov/logicx"
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

	tracks := [][]byte{conductorTrack(alternative, origin)}
	for i, sequence := range sequences {
		tracks = append(tracks, noteTrack(sequence.MIDISequence, origin, uint8(i%16)))
	}

	var header [6]byte
	binary.BigEndian.PutUint16(header[0:], 1) // format 1: one track per part
	binary.BigEndian.PutUint16(header[2:], uint16(len(tracks)))
	binary.BigEndian.PutUint16(header[4:], uint16(ticksPerQuarter))
	if err := writeChunk(w, "MThd", header[:]); err != nil {
		return err
	}
	for _, track := range tracks {
		if err := writeChunk(w, "MTrk", track); err != nil {
			return err
		}
	}
	return nil
}

// conductorTrack holds the maps every part shares: tempo, meter, key and
// markers. Without them an importer would quantize against the wrong bar grid.
func conductorTrack(alternative logicx.Alternative, origin uint32) []byte {
	var events []midiEvent
	metadata := alternative.Metadata
	project := alternative.Project

	if len(project.TempoChanges) == 0 && metadata.BPM > 0 {
		events = append(events, metaEvent(0, 0x51, microsecondsPerQuarter(metadata.BPM)))
	}
	for _, tempo := range project.TempoChanges {
		events = append(events, metaEvent(relative(tempo.Position, origin), 0x51, microsecondsPerQuarter(tempo.BPM)))
	}
	if len(project.TimeSignatures) == 0 {
		events = append(events, metaEvent(0, 0x58, meterMeta(uint8(metadata.TimeSignature[0]), uint16(metadata.TimeSignature[1]))))
	}
	for _, signature := range project.TimeSignatures {
		events = append(events, metaEvent(relative(signature.Position, origin), 0x58,
			meterMeta(signature.Numerator, signature.Denominator)))
	}
	for _, signature := range project.KeySignatures {
		minor := byte(0)
		if signature.Minor {
			minor = 1
		}
		events = append(events, metaEvent(relative(signature.Position, origin), 0x59,
			[]byte{byte(signature.Fifths), minor}))
	}
	for _, marker := range project.Markers {
		events = append(events, metaEvent(relative(marker.Position, origin), 0x06, []byte(marker.Name)))
	}
	return assemble(events)
}

// noteTrack holds one part's notes, named so the importer can label the staff.
func noteTrack(sequence logicx.MIDISequence, origin uint32, channel uint8) []byte {
	events := []midiEvent{metaEvent(0, 0x03, []byte(sequence.Name))}
	for _, note := range sequence.Notes {
		if note.Duration == 0 || note.Pitch > 127 {
			continue
		}
		start := relative(note.Position, origin)
		end := start + note.Duration
		// A note-off sorts before a note-on at the same tick, so a repeated
		// pitch is released before it sounds again.
		events = append(events,
			midiEvent{tick: start, order: 1, data: []byte{0x90 | channel, note.Pitch, midiVelocity}},
			midiEvent{tick: end, order: 0, data: []byte{0x80 | channel, note.Pitch, 0}})
	}
	return assemble(events)
}

// midiEvent is one event awaiting its delta time. Order breaks ties at the same
// tick, lowest first.
type midiEvent struct {
	tick  uint32
	order int
	data  []byte
}

// metaEvent builds a meta event record.
func metaEvent(tick uint32, kind byte, payload []byte) midiEvent {
	var data bytes.Buffer
	data.Write([]byte{0xff, kind})
	putVarint(&data, uint32(len(payload)))
	data.Write(payload)
	return midiEvent{tick: tick, data: data.Bytes()}
}

// assemble sorts events by time and delta-encodes them into a track payload.
func assemble(events []midiEvent) []byte {
	slices.SortStableFunc(events, func(a, b midiEvent) int {
		return cmp.Or(cmp.Compare(a.tick, b.tick), cmp.Compare(a.order, b.order))
	})
	var track bytes.Buffer
	var previous uint32
	for _, event := range events {
		putVarint(&track, event.tick-previous)
		track.Write(event.data)
		previous = event.tick
	}
	putVarint(&track, 0)
	track.Write([]byte{0xff, 0x2f, 0x00}) // end of track
	return track.Bytes()
}

// relative moves a project tick onto the MIDI timeline, where bar one is zero.
func relative(position, origin uint32) uint32 {
	if position < origin {
		return 0
	}
	return position - origin
}

// microsecondsPerQuarter encodes a tempo as the three bytes a tempo meta event
// carries.
func microsecondsPerQuarter(bpm float64) []byte {
	value := uint32(60_000_000)
	if bpm > 0 {
		value = uint32(min(math.Round(60_000_000/bpm), math.MaxUint32>>8))
	}
	return []byte{byte(value >> 16), byte(value >> 8), byte(value)}
}

// meterMeta encodes a meter, whose denominator a meta event stores as a power
// of two. Meters that cannot be written that way fall back to 4/4.
func meterMeta(numerator uint8, denominator uint16) []byte {
	power := byte(0)
	for value := denominator; value > 1; value >>= 1 {
		power++
	}
	if numerator == 0 || denominator == 0 || denominator != 1<<power {
		numerator, power = 4, 2
	}
	// 24 MIDI clocks per beat, 8 32nd notes per quarter: the usual values.
	return []byte{numerator, power, 24, 8}
}

// putVarint writes MIDI's variable-length quantity, seven bits per byte with
// the high bit set on every byte but the last.
func putVarint(w *bytes.Buffer, value uint32) {
	var out [5]byte
	n := 0
	out[n] = byte(value & 0x7f)
	for value >>= 7; value > 0; value >>= 7 {
		n++
		out[n] = byte(value&0x7f) | 0x80
	}
	for ; n >= 0; n-- {
		w.WriteByte(out[n])
	}
}

// writeChunk writes one MThd or MTrk chunk.
func writeChunk(w io.Writer, id string, payload []byte) error {
	var head [8]byte
	copy(head[:4], id)
	binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
