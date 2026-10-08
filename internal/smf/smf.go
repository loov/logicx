// SPDX-License-Identifier: GPL-3.0-or-later

// Package smf writes Standard MIDI Files from Logic projects.
package smf

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"io"
	"math"
	"slices"

	"github.com/loov/logicx"
)

// TicksPerQuarter is Logic's tick resolution, which the files keep.
const TicksPerQuarter = 960

// Write writes a Standard MIDI File holding tracks, format 0 for one track and
// format 1 for more.
func Write(w io.Writer, tracks [][]byte) error {
	var header [6]byte
	if len(tracks) > 1 {
		binary.BigEndian.PutUint16(header[0:], 1)
	}
	binary.BigEndian.PutUint16(header[2:], uint16(len(tracks)))
	binary.BigEndian.PutUint16(header[4:], TicksPerQuarter)
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

// ConductorTrack holds the maps every part shares: tempo, meter, key and
// markers. Without them an importer would quantize against the wrong bar grid.
// Origin is the project tick that becomes MIDI tick zero.
func ConductorTrack(alternative logicx.Alternative, origin uint32) []byte {
	var events []Event
	metadata := alternative.Metadata
	project := alternative.Project

	if len(project.TempoChanges) == 0 && metadata.BPM > 0 {
		events = append(events, Meta(0, 0x51, microsecondsPerQuarter(metadata.BPM)))
	}
	for _, tempo := range project.TempoChanges {
		events = append(events, Meta(Relative(tempo.Position, origin), 0x51, microsecondsPerQuarter(tempo.BPM)))
	}
	if len(project.TimeSignatures) == 0 {
		events = append(events, Meta(0, 0x58, meterMeta(uint8(metadata.TimeSignature[0]), uint16(metadata.TimeSignature[1]))))
	}
	for _, signature := range project.TimeSignatures {
		events = append(events, Meta(Relative(signature.Position, origin), 0x58,
			meterMeta(signature.Numerator, signature.Denominator)))
	}
	for _, signature := range project.KeySignatures {
		minor := byte(0)
		if signature.Minor {
			minor = 1
		}
		events = append(events, Meta(Relative(signature.Position, origin), 0x59,
			[]byte{byte(signature.Fifths), minor}))
	}
	for _, marker := range project.Markers {
		events = append(events, Meta(Relative(marker.Position, origin), 0x06, []byte(marker.Name)))
	}
	return Assemble(events)
}

// Event is one event awaiting its delta time. Order breaks ties at the same
// tick, lowest first.
type Event struct {
	Tick  uint32
	Order int
	Data  []byte
}

// Meta builds a meta event record.
func Meta(tick uint32, kind byte, payload []byte) Event {
	var data bytes.Buffer
	data.Write([]byte{0xff, kind})
	putVarint(&data, uint32(len(payload)))
	data.Write(payload)
	return Event{Tick: tick, Data: data.Bytes()}
}

// Assemble sorts events by time and delta-encodes them into a track payload.
func Assemble(events []Event) []byte {
	slices.SortStableFunc(events, func(a, b Event) int {
		return cmp.Or(cmp.Compare(a.Tick, b.Tick), cmp.Compare(a.Order, b.Order))
	})
	var track bytes.Buffer
	var previous uint32
	for _, event := range events {
		putVarint(&track, event.Tick-previous)
		track.Write(event.Data)
		previous = event.Tick
	}
	putVarint(&track, 0)
	track.Write([]byte{0xff, 0x2f, 0x00}) // end of track
	return track.Bytes()
}

// Relative moves a project tick onto the MIDI timeline, clamping anything
// before origin to zero.
func Relative(position, origin uint32) uint32 {
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
