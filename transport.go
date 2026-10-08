// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"errors"
	"fmt"

	"github.com/loov/logicx/internal/record"
)

// Transport is the project's cycle, end and SMPTE offset, from its "Song"
// chunk. Positions are on the same timeline as [MIDINote.Position].
type Transport struct {
	Cycle      bool
	CycleStart uint32
	CycleEnd   uint32
	// End is the project end.
	End uint32
	// SMPTEBar is the position that plays at SMPTEOffset, a time in samples
	// at the project's sample rate. Logic starts with no bar, 0, and an
	// offset of one hour. Both are read only: Logic rebuilds the offset from
	// a setting not yet found, and discards one written here.
	SMPTEBar    uint32
	SMPTEOffset uint32
	chunk       *Chunk
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
	songSMPTE      = 802
	songMinimum    = songCycleEnd + songCopy + 4
)

// fields is the layout of the transport.
func (t *Transport) fields() []record.Field {
	return append(t.editable(), record.Uint32LE(songSMPTEBar, &t.SMPTEBar), record.Uint32LE(songSMPTE, &t.SMPTEOffset))
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
	return fields
}

// findTransport decodes the transport, or returns nil when the project has
// no song chunk of the expected size.
func findTransport(chunks []*Chunk) *Transport {
	for _, chunk := range chunks {
		if chunk.Type != songChunk || len(chunk.Data) < songMinimum {
			continue
		}
		t := &Transport{chunk: chunk}
		record.Decode(chunk.Data, t.fields()...)
		return t
	}
	return nil
}

// Save writes t, except its SMPTE fields, into both copies in the song chunk. ProjectData.Transport is
// not updated; see [ProjectData.Refresh].
func (t *Transport) Save() error {
	if t.chunk == nil {
		return errors.New("logicx: transport was not decoded from a project")
	}
	if t.CycleStart > t.CycleEnd {
		return fmt.Errorf("logicx: cycle from %d ends before it starts, at %d", t.CycleStart, t.CycleEnd)
	}
	data, err := record.Encode(t.chunk.Data, t.editable()...)
	if err != nil {
		return fmt.Errorf("logicx: transport: %w", err)
	}
	t.chunk.Data = data
	return nil
}
