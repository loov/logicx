// SPDX-License-Identifier: GPL-3.0-or-later

// Package record decodes fixed-layout binary records from byte slices.
package record

import (
	"bytes"
	"encoding/binary"
)

// Field matches or captures one field from a record.
type Field func([]byte) bool

// Decode applies fields in order and reports whether they all matched.
func Decode(data []byte, fields ...Field) bool {
	for _, field := range fields {
		if !field(data) {
			return false
		}
	}
	return true
}

// Scan decodes fixed-size records at stride-byte offsets.
func Scan[T any](data []byte, size, stride int, decode func([]byte) (T, bool)) []T {
	if size <= 0 || stride <= 0 || size > len(data) {
		return nil
	}
	var found []T
	for offset := 0; offset <= len(data)-size; offset += stride {
		value, ok := decode(data[offset : offset+size])
		if ok {
			found = append(found, value)
		}
	}
	return found
}

// Equal matches bytes at offset.
func Equal(offset int, want ...byte) Field {
	return func(data []byte) bool {
		got, ok := at(data, offset, len(want))
		return ok && bytes.Equal(got, want)
	}
}

// Copy captures len(value) bytes at offset.
func Copy(offset int, value []byte) Field {
	return func(data []byte) bool {
		source, ok := at(data, offset, len(value))
		if !ok {
			return false
		}
		copy(value, source)
		return true
	}
}

// Uint8 captures one byte at offset.
func Uint8(offset int, value *uint8) Field {
	return func(data []byte) bool {
		field, ok := at(data, offset, 1)
		if !ok {
			return false
		}
		*value = field[0]
		return true
	}
}

// Uint16LE captures a little-endian uint16 at offset.
func Uint16LE(offset int, value *uint16) Field {
	return func(data []byte) bool {
		field, ok := at(data, offset, 2)
		if !ok {
			return false
		}
		*value = binary.LittleEndian.Uint16(field)
		return true
	}
}

// Uint32LE captures a little-endian uint32 at offset.
func Uint32LE(offset int, value *uint32) Field {
	return func(data []byte) bool {
		field, ok := at(data, offset, 4)
		if !ok {
			return false
		}
		*value = binary.LittleEndian.Uint32(field)
		return true
	}
}

// at returns the size bytes at offset, reporting false when out of range.
func at(data []byte, offset, size int) ([]byte, bool) {
	if offset < 0 || size < 0 || size > len(data) || offset > len(data)-size {
		return nil, false
	}
	return data[offset : offset+size], true
}
