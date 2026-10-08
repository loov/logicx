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

func TestUnknown_ListsUndescribedRuns(t *testing.T) {
	// A tag, an undescribed byte, a two-byte name, then two undescribed bytes
	// and a value counted from the name's end.
	data := []byte{0xaa, 0x01, 2, 0, 'h', 'i', 0xbb, 0xcc, 7}
	var name string
	var value uint8
	spans := Unknown(data, Equal(0, 0xaa), String16(2, &name, Uint8(2, &value)))
	if len(spans) != 2 || spans[0].Offset != 1 || string(spans[0].Data) != "\x01" ||
		spans[1].Offset != 6 || string(spans[1].Data) != "\xbb\xcc" {
		t.Fatalf("Unknown() = %+v", spans)
	}
	if spans := Unknown(data, At(6, Uint16LE(0, new(uint16)))); len(spans) != 2 || spans[1].Offset != 8 {
		t.Fatalf("Unknown() with At = %+v", spans)
	}
}
