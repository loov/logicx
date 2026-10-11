// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/loov/logicx/internal/record"
)

// UnknownRecord is a record, or a chunk payload, and the bytes of it that no
// layout in this package describes.
type UnknownRecord struct {
	// Kind is what the record decoded as, such as "tempo" or "note", or else
	// its event type ("event 0x10") or chunk type ("Song").
	Kind  string
	Chunk *Chunk
	// Event is the record within an event sequence, or nil for a chunk.
	Event *Event
	Size  int
	Spans []UnknownSpan
}

// UnknownSpan is a run of undescribed bytes, at Offset within its record.
type UnknownSpan struct {
	Offset int
	Data   []byte
}

// Unknown lists every record in p's tree with the bytes its layout leaves
// undescribed. A record without a layout — an undecoded event or chunk type,
// or one that does not match its layout — is entirely undescribed. Chunk
// headers are not included.
//
// The bytes come from the layouts themselves, so a newly decoded field shrinks
// the list without any change here.
func (p *ProjectData) Unknown() []UnknownRecord {
	var records []UnknownRecord
	add := func(kind string, chunk *Chunk, event *Event, data []byte, fields []record.Field) {
		r := UnknownRecord{Kind: kind, Chunk: chunk, Event: event, Size: len(data)}
		for _, s := range record.Unknown(data, fields...) {
			r.Spans = append(r.Spans, UnknownSpan{s.Offset, s.Data})
		}
		records = append(records, r)
	}
	automation := make(map[chordSequenceID]bool)
	for _, chunk := range p.Chunks {
		if chunk.Type == "MSeq" && sequenceName(chunk.Data) == automationSequence {
			automation[chunkSequenceID(chunk)] = true
		}
	}
	for _, chunk := range p.Chunks {
		if chunk.Events == nil {
			kind, fields := chunkLayout(chunk)
			add(kind, chunk, nil, chunk.Data, fields)
			continue
		}
		for _, event := range chunk.Events {
			kind, fields := eventLayout(event.Data, automation[chunkSequenceID(chunk)])
			// The continuation byte of each atom after the first is structure.
			for offset := atomSize; offset < len(event.Data); offset += atomSize {
				fields = append(fields, record.Copy(offset+7, make([]byte, 1)))
			}
			add(kind, chunk, event, event.Data, fields)
		}
	}
	return records
}

// eventLayout names an event record and returns its layout, trying the
// decoders this package has for its type. Automation events are told by the
// sequence holding them, as their types overlap others.
func eventLayout(data []byte, automation bool) (string, []record.Field) {
	if base := data[0] &^ maxAutomationSlot; automation && (base == eventAutomation || base == eventMIDIFXAutomation) && len(data) >= atomSize {
		var a AutomationPoint
		var eventType, parameter uint8
		var sample bool
		fields := append(a.fields(&eventType, &parameter), record.Bit(15, automationSample, &sample))
		if len(data) >= 2*atomSize && data[atomSize+7] == automationCurveAtom {
			fields = append(fields, record.Uint8(automationCurve, new(uint8)))
		}
		if data[15]&automationSample != 0 {
			return "automation sample", fields
		}
		return "automation point", fields
	}
	if isNote(data[0]) {
		if n, ok := decodeMIDINote(data); ok {
			var status, release uint8
			fields := n.fields(&status, &release)
			for offset := 32; offset+atomSize <= len(data); offset += atomSize {
				if atom := atomLayout(data[offset : offset+atomSize]); atom != nil {
					fields = append(fields, record.At(offset, atom...))
				}
			}
			return "note", fields
		}
	}
	if c, ok := decodeMIDIControl(data); ok {
		var status, number uint8
		var value uint32
		return "control", c.fields(&status, &value, &number)
	}
	switch data[0] {
	case eventTempo:
		if c, ok := decodeTempoChange(data); ok {
			var value uint32
			return "tempo", c.fields(&value)
		}
	case eventTimeSignature:
		if c, ok := decodeTimeSignatureChange(data); ok {
			var power uint8
			fields := c.fields(&power)
			if len(data) >= 64 {
				if groups, _ := decodeBeatGrouping(data[40:64], c.Numerator); groups != nil {
					fields = append(fields, beatGroupingFields()...)
				}
			}
			return "meter", fields
		}
	case eventKeySignature:
		if c, ok := decodeKeySignatureChange(data); ok {
			return "key", c.fields()
		}
	case eventMarker:
		var m Marker
		if record.Decode(data, m.fields()...) {
			return "marker", m.fields()
		}
	case eventLink:
		if l, ok := decodeChordLink(data); ok {
			return "chord link", l.fields()
		}
		if l, ok := decodeRegionLink(data); ok {
			var p regionParams
			return "region link", append(append(l.fields(), p.linkFields()...),
				record.Uint32LE(linkTrack, new(uint32)), record.Uint16LE(linkRow, new(uint16)))
		}
	case eventAudioRegion:
		var a AudioPlacement
		var position, loop uint32
		var gain uint8
		var key audioRegionKey
		fields := append(a.fields(&position, &gain, &loop, &key), record.Uint32LE(linkTrack, new(uint32)))
		if len(data) >= audioPlacementMinimum && record.Decode(data, fields...) {
			return "audio placement", fields
		}
	case eventScore:
		if l, ok := decodeLyric(data); ok {
			// The text runs from its first atom to the end of the record.
			return "lyric", append(l.fields(), record.Copy(lyricText, make([]byte, len(data)-lyricText)))
		}
		if o, ok := decodeScoreOrnament(data); ok {
			return "ornament", o.fields()
		}
		if a, ok := decodeScoreArpeggio(data); ok {
			return "arpeggio", a.fields()
		}
		if c, ok := decodeChordEvent(data); ok {
			var spelling uint8
			return "chord", c.fields(&spelling)
		}
	}
	return fmt.Sprintf("event 0x%02x", data[0]), nil
}

// atomLayout returns the layout of a note's trailing atom, or nil when it is
// none this package decodes.
func atomLayout(atom []byte) []record.Field {
	var a NoteAttributes
	if a.decodeAtom(atom) {
		return []record.Field{record.Uint8(2, new(uint8)), record.Copy(4, make([]byte, 4))}
	}
	if s, ok := decodeScoreSlurSegment(atom); ok {
		return s.fields()
	}
	if f, ok := decodeScoreFermata(atom); ok {
		return f.fields()
	}
	if a, ok := decodeScoreArticulation(atom); ok {
		return a.fields()
	}
	return nil
}

// beatGroupingFields is the layout of the beat grouping that follows a meter
// record, as decodeBeatGrouping reads it.
func beatGroupingFields() []record.Field {
	return []record.Field{
		record.Equal(40, 0, 0, 0, 0, 0, 0),
		record.Uint8(46, new(uint8)),
		record.Equal(47, 0, 0, 0, 0, 0),
		record.Copy(52, make([]byte, 3)),
		record.Equal(55, 0x88),
		record.Equal(56, 0, 0, 0, 0, 0, 0, 0, 0),
	}
}

// chunkLayout names a chunk and returns the layout of its payload.
func chunkLayout(chunk *Chunk) (string, []record.Field) {
	d := chunk.Data
	switch chunk.Type {
	case audioFileChunk:
		var f AudioFile
		var overview uint32
		if record.Decode(d, f.fields(&overview)...) {
			return "audio file", f.fields(&overview)
		}
	case audioRegionChunk:
		var r AudioRegion
		if record.Decode(d, r.fields()...) {
			return "audio region", r.fields()
		}
	case environmentChunk:
		var o EnvironmentObject
		if record.Decode(d, o.fields()...) {
			return "environment", o.fields()
		}
	case channelStripChunk:
		if isChannelStrip(chunk) && len(d) >= channelStripRecord+channelStripRecordSize {
			descriptor := channelStripRecord + 16
			fields := []record.Field{
				record.CString(channelStripName, channelStripNameSize, new(string)),
				record.Uint8(channelStripKind, new(uint8)),
				// Whether the strip is active is read from these descriptor bytes.
				record.Uint8(descriptor+2, new(uint8)),
				record.Uint8(descriptor+4, new(uint8)),
			}
			if len(d) >= channelStripMixerEnd {
				var t Track
				fields = append(append(fields, t.mixerFields(new(uint8), new(uint8))...),
					record.Uint16LE(channelStripOutput, new(uint16)), record.Uint16LE(channelStripInput, new(uint16)))
			}
			return "channel strip", fields
		}
	case pluginChunk:
		if bytes.Equal(chunk.Header[4:8], pluginVariant) && len(d) >= sendMinimum && binary.LittleEndian.Uint16(d[pluginChainOffset:]) == chainSend {
			var s Send
			return "send", append(s.fields(new(uint8), new(uint8), new(bool)),
				record.Uint16LE(pluginChainOffset, new(uint16)), record.Uint8(sendIndex, new(uint8)), record.Uint8(sendBus, new(uint8)))
		}
		if bytes.Equal(chunk.Header[4:8], pluginVariant) && len(d) >= pluginRecordSize {
			return "plug-in", []record.Field{
				record.Uint16LE(pluginChainOffset, new(uint16)),
				record.Uint16LE(pluginSlotOffset, new(uint16)),
				record.CString(pluginSetting, pluginSettingSize, new(string)),
				record.CString(pluginName, pluginNameSize, new(string)),
				record.Copy(pluginManufacturer, make([]byte, pluginRecordSize-pluginManufacturer)),
			}
		}
	case "MSeq":
		var fields []record.Field
		if len(d) >= sequenceMetadataTail {
			fields = append(fields, record.Uint32LE(len(d)-sequenceMetadataTail, new(uint32)))
		}
		if len(d) >= sequenceOffsetTail {
			fields = append(fields, record.Uint32LE(len(d)-sequenceOffsetTail, new(uint32)))
		}
		fields = append(fields, record.String16(sequenceNameLength, new(string)))
		if tail, ok := sequenceTail(d); ok {
			if len(d)-tail == sequenceParameterTail {
				var p regionParams
				fields = append(fields, p.descriptorFields(tail)...)
				fields = append(fields, record.Uint32LE(tail+sequenceTrack, new(uint32)))
			}
			if tail+sequenceCrop+4 <= len(d)-sequenceOffsetTail {
				fields = append(fields, record.Uint32LE(tail+sequenceCrop, new(uint32)))
			}
		}
		return "sequence", fields
	case songChunk:
		if len(d) >= songMinimum {
			var t Transport
			return "song", t.fields()
		}
	case "Trak":
		if len(d) >= arrangeTrackSelectedMinimum {
			var t Track
			return "arrange track", append(t.headerFields(),
				record.Bit(arrangeTrackFlags, 0x01, new(bool)),
				record.Uint8(arrangeTrackKind, new(uint8)),
				record.Uint32LE(arrangeTrackEnvironment, new(uint32)),
				record.Copy(arrangeTrackID, make([]byte, 16)),
				record.Bit(arrangeTrackSelected, 0x20, new(bool)),
				record.Bit(arrangeTrackSelectedAlso, 0x40, new(bool)))
		}
	case "TxSq":
		if start := bytes.Index(d, []byte(`{\rtf`)); start >= 0 {
			var size, at, repeated uint32
			return "marker text", append(markerTextFields(&size, &at, &repeated), record.Copy(start, make([]byte, len(d)-start)))
		}
	}
	return chunk.Type, nil
}
