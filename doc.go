// SPDX-License-Identifier: GPL-3.0-or-later

// Package logicx reads Logic Pro .logicx project bundles.
//
// ProjectData is an undocumented binary format. The parser may need updates
// when Logic changes the format.
//
// # Writing
//
// [ProjectData.Chunks] is the whole file as a tree: chunks, and for event
// sequences the events within them. [ProjectData.MarshalBinary] writes the
// tree back, recomputing only its size fields, so an unmodified parse writes
// back byte for byte. Decoded values point at the node they came from, and
// those with a Save method write their fields back into it, leaving every
// byte this package does not decode untouched. Values whose positions are
// computed — placed or looped notes, project chords, signatures moved onto
// bar 1 — have no Save.
//
// # Container
//
// A ProjectData file is a 24-byte header followed by chunks. Each chunk is a
// 36-byte header — a reversed four-character type, a group and sequence number
// identifying which sequence it belongs to, and a payload size — followed by
// its payload. Every chunk is preserved in [ProjectData.Chunks], including the
// many types this package does not decode; see [Chunk] for how the payload is
// held.
//
// Three chunk types carry most of what this package reads. "AuCO" holds one
// channel strip per chunk, "AuCU" one plug-in instance, and "EvSq" an event
// sequence.
//
// # Channel strips
//
// A plug-in names its channel strip in its own chunk header, so a strip and
// its chain are related explicitly rather than by their order in the file. The
// plug-in's record gives the chain it belongs to — the instrument and audio
// inserts in one, the MIDI effects in another — and its slot within that
// chain. Empty slots have no chunk at all, so slot numbers have gaps.
//
// Plug-ins that ship with Logic carry Emagic's manufacturer code and no Audio
// Unit type or subtype, and are identified by name; third-party plug-ins carry
// the full component description. See [AudioUnit].
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
