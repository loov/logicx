// SPDX-License-Identifier: GPL-3.0-or-later

package record

import "testing"

func TestDecode_RejectsOutOfBoundsField(t *testing.T) {
	if Decode([]byte{1}, Copy(0, make([]byte, 2))) {
		t.Fatal("Decode() accepted a truncated field")
	}
}

func TestEncode_RoundTripsAndKeepsUndecodedBytes(t *testing.T) {
	// A tag, a two-byte name and a tail of an undecoded byte and a value.
	data := []byte{0xaa, 2, 0, 'h', 'i', 0xbb, 7}
	var name string
	var value uint8
	fields := []Field{Equal(0, 0xaa), String16(1, &name, Uint8(1, &value))}
	if !Decode(data, fields...) || name != "hi" || value != 7 {
		t.Fatalf("Decode() = %q, %d", name, value)
	}
	name, value = "hey", 9
	got, err := Encode(data, fields...)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0xaa, 3, 0, 'h', 'e', 'y', 0xbb, 9}; string(got) != string(want) {
		t.Fatalf("Encode() = %v, want %v", got, want)
	}
	if data[1] != 2 {
		t.Fatal("Encode() modified its input")
	}
}
