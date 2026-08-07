// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/egonelbre/logicx"
)

func TestWriteMIDI_KeepsLogicTimingUnquantized(t *testing.T) {
	alternative := logicx.Alternative{
		Metadata: logicx.Metadata{BPM: 120, TimeSignature: [2]uint64{3, 4}},
		Project: logicx.ProjectData{
			Sequences: []logicx.MIDISequence{{Name: "Lead", Notes: []logicx.MIDINote{
				// Off the grid on purpose: the importer quantizes, not us.
				{Position: logicBarOneTick + 947, Pitch: 60, Duration: 313},
			}}},
			Markers: []logicx.Marker{{Position: logicBarOneTick, Name: "intro"}},
		},
	}
	var output bytes.Buffer
	if err := writeMIDI(&output, alternative); err != nil {
		t.Fatal(err)
	}
	file := output.Bytes()

	if string(file[:4]) != "MThd" || binary.BigEndian.Uint32(file[4:]) != 6 {
		t.Fatalf("header = %x", file[:8])
	}
	format := binary.BigEndian.Uint16(file[8:])
	tracks := binary.BigEndian.Uint16(file[10:])
	division := binary.BigEndian.Uint16(file[12:])
	if format != 1 || tracks != 2 || division != uint16(ticksPerQuarter) {
		t.Fatalf("format %d, %d tracks, division %d", format, tracks, division)
	}

	// Chunk lengths must be exact, or every reader loses sync.
	events := map[string]uint32{}
	for offset := 14; offset < len(file); {
		if string(file[offset:offset+4]) != "MTrk" {
			t.Fatalf("chunk at %d = %q", offset, file[offset:offset+4])
		}
		length := binary.BigEndian.Uint32(file[offset+4:])
		body := file[offset+8 : offset+8+int(length)]
		offset += 8 + int(length)
		for i, tick := 0, uint32(0); i < len(body); {
			delta, next := readVarint(t, body, i)
			tick += delta
			i = next
			switch status := body[i]; {
			case status == 0xff:
				kind := body[i+1]
				size, after := readVarint(t, body, i+2)
				events[string([]byte{kind})+"@"+itoa(tick)] = uint32(size)
				i = after + int(size)
			case status&0xf0 == 0x90 || status&0xf0 == 0x80:
				events[string([]byte{status & 0xf0})+"@"+itoa(tick)] = uint32(body[i+1])
				i += 3
			default:
				t.Fatalf("unexpected status %#x", status)
			}
		}
		if offset > len(file) {
			t.Fatal("chunk runs past the end of the file")
		}
	}

	// Bar one is tick zero, and the note keeps Logic's own timing.
	if _, ok := events["\x06@0"]; !ok {
		t.Errorf("marker missing at tick 0: %v", events)
	}
	if pitch, ok := events["\x90@947"]; !ok || pitch != 60 {
		t.Errorf("note-on missing at tick 947: %v", events)
	}
	if _, ok := events["\x80@1260"]; !ok {
		t.Errorf("note-off missing at tick 1260: %v", events)
	}
	// 3/4 is stored as numerator 3 and denominator 4 written as a power of two.
	if _, ok := events["\x58@0"]; !ok {
		t.Errorf("time signature missing: %v", events)
	}
}

func TestMeterMeta_FallsBackWhenTheDenominatorIsNotAPowerOfTwo(t *testing.T) {
	for _, test := range []struct {
		numerator   uint8
		denominator uint16
		want        [2]byte
	}{
		{4, 4, [2]byte{4, 2}},
		{6, 8, [2]byte{6, 3}},
		{5, 16, [2]byte{5, 4}},
		{3, 6, [2]byte{4, 2}}, // not a power of two, so 4/4
		{0, 0, [2]byte{4, 2}},
	} {
		got := meterMeta(test.numerator, test.denominator)
		if got[0] != test.want[0] || got[1] != test.want[1] {
			t.Errorf("%d/%d = %v, want %v", test.numerator, test.denominator, got[:2], test.want)
		}
	}
}

// readVarint decodes one variable-length quantity, failing the test on a
// value that never terminates.
func readVarint(t *testing.T, data []byte, at int) (uint32, int) {
	t.Helper()
	var value uint32
	for range 4 {
		b := data[at]
		at++
		value = value<<7 | uint32(b&0x7f)
		if b&0x80 == 0 {
			return value, at
		}
	}
	t.Fatal("variable-length quantity runs past four bytes")
	return 0, at
}

// itoa keeps the event keys above readable.
func itoa(value uint32) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for ; value > 0; value /= 10 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
	}
	return string(digits)
}
