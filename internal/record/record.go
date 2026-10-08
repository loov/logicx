// SPDX-License-Identifier: GPL-3.0-or-later

// Package record decodes and encodes fixed-layout binary records.
//
// A record's layout is declared once, as a list of fields holding pointers to
// the values they capture, and the same list serves both directions: Decode
// reads the fields out of a record and Encode writes them back. Encoding
// writes over an existing record rather than building one from scratch, so the
// bytes no field describes survive a round trip untouched.
package record

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"unicode/utf16"
)

// Field matches or captures one field of a record.
type Field interface {
	// decode reads the field from data, reporting false when data does not
	// hold it.
	decode(data []byte) bool
	// encode writes the field into data and returns the result, which is a
	// new slice when the field changes the record's length.
	encode(data []byte) ([]byte, error)
	// cover marks the bytes of data the field describes.
	cover(data []byte, mark func(start, end int))
}

// Decode applies fields in order and reports whether they all matched.
func Decode(data []byte, fields ...Field) bool {
	for _, field := range fields {
		if !field.decode(data) {
			return false
		}
	}
	return true
}

// Encode writes fields in order over a copy of data and returns it. data is
// left untouched, even when a field fails.
func Encode(data []byte, fields ...Field) ([]byte, error) {
	out := bytes.Clone(data)
	for _, field := range fields {
		var err error
		if out, err = field.encode(out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fixed is a field of size bytes at offset.
type fixed struct {
	offset, size int
	get          func([]byte) bool
	put          func([]byte) error
}

func (f fixed) decode(data []byte) bool {
	field, ok := at(data, f.offset, f.size)
	return ok && f.get(field)
}

func (f fixed) cover(data []byte, mark func(start, end int)) {
	if _, ok := at(data, f.offset, f.size); ok {
		mark(f.offset, f.offset+f.size)
	}
}

func (f fixed) encode(data []byte) ([]byte, error) {
	field, ok := at(data, f.offset, f.size)
	if !ok {
		return nil, fmt.Errorf("record: %d-byte field at offset %d past the end of %d bytes", f.size, f.offset, len(data))
	}
	return data, f.put(field)
}

// Equal matches bytes at offset, and writes them when encoding.
func Equal(offset int, want ...byte) Field {
	return fixed{offset, len(want),
		func(b []byte) bool { return bytes.Equal(b, want) },
		func(b []byte) error { copy(b, want); return nil },
	}
}

// Copy captures len(value) bytes at offset.
func Copy(offset int, value []byte) Field {
	return fixed{offset, len(value),
		func(b []byte) bool { copy(value, b); return true },
		func(b []byte) error { copy(b, value); return nil },
	}
}

// Uint8 captures one byte at offset.
func Uint8(offset int, value *uint8) Field {
	return fixed{offset, 1,
		func(b []byte) bool { *value = b[0]; return true },
		func(b []byte) error { b[0] = *value; return nil },
	}
}

// Uint16LE captures a little-endian uint16 at offset.
func Uint16LE(offset int, value *uint16) Field {
	return fixed{offset, 2,
		func(b []byte) bool { *value = binary.LittleEndian.Uint16(b); return true },
		func(b []byte) error { binary.LittleEndian.PutUint16(b, *value); return nil },
	}
}

// Uint32LE captures a little-endian uint32 at offset.
func Uint32LE(offset int, value *uint32) Field {
	return fixed{offset, 4,
		func(b []byte) bool { *value = binary.LittleEndian.Uint32(b); return true },
		func(b []byte) error { binary.LittleEndian.PutUint32(b, *value); return nil },
	}
}

// Code4 captures a four-character code stored little-endian, so "WAVE" is
// stored as "EVAW".
func Code4(offset int, value *string) Field {
	return fixed{offset, 4,
		func(b []byte) bool { *value = string([]byte{b[3], b[2], b[1], b[0]}); return true },
		func(b []byte) error {
			v := *value
			if len(v) != 4 {
				return fmt.Errorf("record: code %q is not four bytes", v)
			}
			copy(b, []byte{v[3], v[2], v[1], v[0]})
			return nil
		},
	}
}

// CString captures a NUL-padded string in size bytes at offset. A string of
// exactly size bytes fills the field with no NUL.
func CString(offset, size int, value *string) Field {
	return fixed{offset, size,
		func(b []byte) bool { s, _, _ := bytes.Cut(b, []byte{0}); *value = string(s); return true },
		func(b []byte) error {
			if len(*value) > size {
				return fmt.Errorf("record: string longer than %d bytes", size)
			}
			clear(b)
			copy(b, *value)
			return nil
		},
	}
}

// Len matches a record exactly size bytes long. Encoding fails for any other
// length rather than writing a record of the wrong size.
func Len(size int) Field { return length(size) }

type length int

func (n length) decode(data []byte) bool { return len(data) == int(n) }

func (n length) cover([]byte, func(start, end int)) {}

func (n length) encode(data []byte) ([]byte, error) {
	if len(data) != int(n) {
		return nil, fmt.Errorf("record: %d bytes, want %d", len(data), int(n))
	}
	return data, nil
}

// String16 captures a string at offset whose byte length is the little-endian
// uint16 preceding it. Everything after the string moves with its length, so
// tail is applied to the bytes that follow it, offsets counted from there.
func String16(offset int, value *string, tail ...Field) Field {
	return prefixed{offset: offset, unit: 1, value: value, tail: tail}
}

// UTF16 is String16 for a string stored as little-endian UTF-16, whose length
// prefix counts code units.
func UTF16(offset int, value *string, tail ...Field) Field {
	return prefixed{offset: offset, unit: 2, value: value, tail: tail}
}

// prefixed is a length-prefixed string of unit-byte code units.
type prefixed struct {
	offset int
	unit   int
	value  *string
	tail   []Field
}

// span returns where the string's bytes end, from the length prefix in data.
func (f prefixed) span(data []byte) (int, bool) {
	prefix, ok := at(data, f.offset, 2)
	if !ok {
		return 0, false
	}
	end := f.offset + 2 + f.unit*int(binary.LittleEndian.Uint16(prefix))
	return end, end <= len(data)
}

func (f prefixed) decode(data []byte) bool {
	end, ok := f.span(data)
	if !ok {
		return false
	}
	body := data[f.offset+2 : end]
	if f.unit == 1 {
		*f.value = string(body)
	} else {
		units := make([]uint16, len(body)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(body[2*i:])
		}
		*f.value = string(utf16.Decode(units))
	}
	return Decode(data[end:], f.tail...)
}

func (f prefixed) cover(data []byte, mark func(start, end int)) {
	end, ok := f.span(data)
	if !ok {
		return
	}
	mark(f.offset, end)
	coverAt(data, end, f.tail, mark)
}

func (f prefixed) encode(data []byte) ([]byte, error) {
	end, ok := f.span(data)
	if !ok {
		return nil, fmt.Errorf("record: string at offset %d past the end of %d bytes", f.offset, len(data))
	}
	body := []byte(*f.value)
	if f.unit == 2 {
		body = body[:0:0]
		for _, c := range utf16.Encode([]rune(*f.value)) {
			body = binary.LittleEndian.AppendUint16(body, c)
		}
	}
	if len(body)/f.unit > 0xffff {
		return nil, fmt.Errorf("record: string of %d bytes too long", len(body))
	}
	tail, err := Encode(data[end:], f.tail...)
	if err != nil {
		return nil, err
	}
	out := slices.Clip(data[:f.offset])
	out = binary.LittleEndian.AppendUint16(out, uint16(len(body)/f.unit))
	out = append(out, body...)
	return append(out, tail...), nil
}

// At applies fields to the bytes from offset on, their offsets counted from
// there: for a record that repeats a layout, such as trailing atoms.
func At(offset int, fields ...Field) Field { return within{offset, fields} }

type within struct {
	offset int
	fields []Field
}

func (w within) decode(data []byte) bool {
	return w.offset >= 0 && w.offset <= len(data) && Decode(data[w.offset:], w.fields...)
}

func (w within) encode(data []byte) ([]byte, error) {
	if w.offset < 0 || w.offset > len(data) {
		return nil, fmt.Errorf("record: offset %d past the end of %d bytes", w.offset, len(data))
	}
	tail, err := Encode(data[w.offset:], w.fields...)
	if err != nil {
		return nil, err
	}
	return append(slices.Clip(data[:w.offset]), tail...), nil
}

func (w within) cover(data []byte, mark func(start, end int)) {
	coverAt(data, w.offset, w.fields, mark)
}

// coverAt covers fields applied to the bytes from offset on.
func coverAt(data []byte, offset int, fields []Field, mark func(start, end int)) {
	if offset < 0 || offset > len(data) {
		return
	}
	for _, field := range fields {
		field.cover(data[offset:], func(start, end int) { mark(offset+start, offset+end) })
	}
}

// Span is a run of bytes within a record.
type Span struct {
	Offset int
	Data   []byte
}

// Unknown returns the runs of data that no field describes, in order. Fields
// that do not fit data describe nothing.
func Unknown(data []byte, fields ...Field) []Span {
	known := make([]bool, len(data))
	coverAt(data, 0, fields, func(start, end int) {
		for i := max(start, 0); i < min(end, len(data)); i++ {
			known[i] = true
		}
	})
	var spans []Span
	for i := 0; i < len(data); {
		if known[i] {
			i++
			continue
		}
		start := i
		for i < len(data) && !known[i] {
			i++
		}
		spans = append(spans, Span{start, data[start:i]})
	}
	return spans
}

// at returns the size bytes at offset, reporting false when out of range.
func at(data []byte, offset, size int) ([]byte, bool) {
	if offset < 0 || size < 0 || size > len(data) || offset > len(data)-size {
		return nil, false
	}
	return data[offset : offset+size], true
}
