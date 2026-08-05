// SPDX-License-Identifier: GPL-3.0-or-later

package record

import "testing"

func TestScan_DecodesMatchingRecordsAndIgnoresTruncatedTail(t *testing.T) {
	data := []byte{
		0xaa, 1, 0, 0, 0,
		0x00, 2, 0, 0, 0,
		0xaa, 3, 0, 0, 0,
		0xaa,
	}
	got := Scan(data, 5, 5, func(data []byte) (uint32, bool) {
		var value uint32
		ok := Decode(data, Equal(0, 0xaa), Uint32LE(1, &value))
		return value, ok
	})
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("Scan() = %v, want [1 3]", got)
	}
}

func TestDecode_RejectsOutOfBoundsField(t *testing.T) {
	if Decode([]byte{1}, Copy(0, make([]byte, 2))) {
		t.Fatal("Decode() accepted a truncated field")
	}
}
