// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"time"

	"github.com/loov/logicx/internal/record"
)

// Transport is the project's cycle, end, SMPTE start and sample rate, from its "Song"
// chunk and tempo map. Positions are on the same timeline as
// [MIDINote.Position].
type Transport struct {
	Cycle      bool
	CycleStart uint32
	CycleEnd   uint32
	// End is the project end.
	End uint32
	// SMPTEStart is the SMPTE time at which the first tempo change plays,
	// which is at bar 1 unless the project starts before it. Logic starts
	// out at one hour. It is stored as each tempo change's
	// [TempoChange.Time], so Save shifts the whole tempo map.
	SMPTEStart time.Duration
	// SMPTEBar is read only: Logic sets it to bar 1 once the SMPTE start is
	// changed, and it is 0 before.
	SMPTEBar uint32
	// SampleRate is in Hz: 44100 or 48000, doubled or quadrupled. It is 0
	// for a rate this package does not know, which Save then keeps.
	SampleRate uint32
	chunk      *Chunk
	tempo      []TempoChange
	rate       sampleRate
}

// sampleRate is the song chunk's encoding of the sample rate: a code for
// 44.1 or 48 kHz, written twice, and a multiple of it. The multiple's base-2
// logarithm is also in bits songRateBits of the byte after it, among bits
// that mean something else.
type sampleRate struct {
	family   uint16
	multiple uint8
}

// sampleRateFamilies maps the song chunk's codes to the rates they stand for.
var sampleRateFamilies = map[uint16]uint32{0xe970: 44100, 0xf8ac: 48000}

// hz returns the rate in Hz, or 0 when it is not one this package knows.
func (r sampleRate) hz() uint32 {
	base := sampleRateFamilies[r.family]
	if r.multiple != 1 && r.multiple != 2 && r.multiple != 4 {
		return 0
	}
	return base * uint32(r.multiple)
}

// sampleRateOf encodes a rate in Hz.
func sampleRateOf(hz uint32) (sampleRate, bool) {
	for family, base := range sampleRateFamilies {
		for _, multiple := range []uint8{1, 2, 4} {
			if base*uint32(multiple) == hz {
				return sampleRate{family, multiple}, true
			}
		}
	}
	return sampleRate{}, false
}

// The song chunk holds the transport twice, the second copy songCopy bytes
// after the first, and Logic keeps them equal.
const (
	songChunk      = "Song"
	songCopy       = 700
	songCycle      = 194 // 0x01 is cycle on
	songEnd        = 384
	songCycleStart = 400
	songCycleEnd   = 408
	songSMPTEBar   = 336
	songRate       = 286 // and songRate+2
	songRateMult   = 298
	songRateBits   = 0x18 // in songRateMult+1
	songMinimum    = songCycleEnd + songCopy + 4
)

// fields is the layout of the transport.
func (t *Transport) fields() []record.Field {
	return append(t.editable(), record.Uint32LE(songSMPTEBar, &t.SMPTEBar))
}

// editable is the part of the layout that Save writes.
func (t *Transport) editable() []record.Field {
	var fields []record.Field
	for _, at := range []int{0, songCopy} {
		fields = append(fields,
			record.Bit(at+songCycle, 0x01, &t.Cycle),
			record.Uint32LE(at+songEnd, &t.End),
			record.Uint32LE(at+songCycleStart, &t.CycleStart),
			record.Uint32LE(at+songCycleEnd, &t.CycleEnd),
		)
	}
	fields = append(fields,
		record.Uint16LE(songRate, &t.rate.family),
		record.Uint16LE(songRate+2, &t.rate.family),
		record.Uint8(songRateMult, &t.rate.multiple),
	)
	return fields
}

// findTransport decodes the transport, or returns nil when the project has
// no song chunk of the expected size.
func findTransport(chunks []*Chunk) *Transport {
	for _, chunk := range chunks {
		if chunk.Type != songChunk || len(chunk.Data) < songMinimum {
			continue
		}
		t := &Transport{chunk: chunk, tempo: findTempoChanges(chunks)}
		record.Decode(chunk.Data, t.fields()...)
		t.SMPTEStart = t.smpteStart()
		t.SampleRate = t.rate.hz()
		return t
	}
	return nil
}

// smpteStart is the time of the first tempo change as decoded.
func (t *Transport) smpteStart() time.Duration {
	if len(t.tempo) == 0 {
		return 0
	}
	return time.Duration(t.tempo[0].Time) * tempoTimeUnit
}

// Save writes t into both copies in the song chunk and shifts the tempo map
// when SMPTEStart changed. ProjectData.Transport and
// ProjectData.TempoChanges are not updated; see [ProjectData.Refresh].
func (t *Transport) Save() error {
	if t.chunk == nil {
		return errors.New("logicx: transport was not decoded from a project")
	}
	if t.CycleStart > t.CycleEnd {
		return fmt.Errorf("logicx: cycle from %d ends before it starts, at %d", t.CycleStart, t.CycleEnd)
	}
	rate := t.rate
	if t.SampleRate != t.rate.hz() {
		var ok bool
		if rate, ok = sampleRateOf(t.SampleRate); !ok {
			return fmt.Errorf("logicx: sample rate %d Hz is not supported", t.SampleRate)
		}
	}
	if shift := (t.SMPTEStart - t.smpteStart()) / tempoTimeUnit; shift != 0 {
		if len(t.tempo) == 0 {
			return errors.New("logicx: the SMPTE start needs a tempo map")
		}
		// Validated whole first, so that a failure leaves the map unshifted.
		for i := range t.tempo {
			if shifted := int64(t.tempo[i].Time) + int64(shift); shifted < 0 || shifted > math.MaxUint32 {
				return fmt.Errorf("logicx: SMPTE start %v out of range", t.SMPTEStart)
			}
			if err := t.tempo[i].ref.check("tempo change"); err != nil {
				return err
			}
		}
		for i := range t.tempo {
			t.tempo[i].Time = uint32(int64(t.tempo[i].Time) + int64(shift))
			if err := t.tempo[i].Save(); err != nil {
				return err
			}
		}
	}
	changed := rate != t.rate
	t.rate = rate
	data, err := record.Encode(t.chunk.Data, t.editable()...)
	if err != nil {
		return fmt.Errorf("logicx: transport: %w", err)
	}
	if changed {
		log2 := byte(bits.TrailingZeros8(rate.multiple))
		data[songRateMult+1] = data[songRateMult+1]&^songRateBits | log2<<3
	}
	t.chunk.Data = data
	return nil
}
