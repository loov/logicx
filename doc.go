// SPDX-License-Identifier: GPL-3.0-or-later

// Package logicx reads Logic Pro .logicx project bundles.
//
// ProjectData is an undocumented binary format. The parser is read-only and
// may need updates when Logic changes the format.
//
// # Container
//
// A ProjectData file is a 24-byte header followed by chunks. Each chunk is a
// 36-byte header — a reversed four-character type, a group and sequence number
// identifying which sequence it belongs to, and a payload size — followed by
// its payload. Every chunk is preserved in [ProjectData.Chunks], including the
// many types this package does not decode.
//
// Two chunk types carry most of what this package reads. "AuCO" holds one
// channel strip per chunk, and "EvSq" holds an event sequence.
//
// # Event sequences
//
// An "EvSq" payload is an array of 16-byte atoms. A record begins at an atom
// whose byte 7 has the high bit clear and runs through every following atom
// that sets it; byte 0 of the first atom is the record type.
//
// Record lengths carry meaning: a 48-byte meter record has no beat grouping
// while a 64-byte one does, and a note record grows by one atom for each
// articulation, fermata or slur marker attached to it.
//
// Honouring these boundaries is what keeps decoding exact. Searching for a
// record's leading bytes instead also matches the interior of longer records —
// the third atom of a marker is byte-for-byte a plausible 1/1 meter at an
// absurd position, which is enough to stretch a derived bar grid to hundreds
// of thousands of measures.
package logicx
