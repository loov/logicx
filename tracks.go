// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"slices"
	"strings"
)

// AudioUnit is one plug-in instance in a channel strip. Slot is its position
// within its own chain: on the audio chain the instrument sits at slot zero
// and the inserts are numbered from one, while MIDI effects number from zero.
// Slots left empty have no record at all, so the numbering has gaps.
//
// Logic's built-in plug-ins carry Emagic's manufacturer code and leave Type
// and Subtype empty, so Name identifies them. Third-party plug-ins fill in the
// full Audio Unit component description.
type AudioUnit struct {
	Name         string
	Type         string
	Subtype      string
	Manufacturer string
	Setting      string
	Slot         uint16
	Offset       int
	strip        uint16
	midi         bool
}

// Builtin reports whether the plug-in ships with Logic rather than being an
// installed Audio Unit component.
func (a AudioUnit) Builtin() bool { return a.Manufacturer == emagic && a.Type == "" }

// Fingerprint returns the Audio Unit type, subtype, and manufacturer tuple.
func (a AudioUnit) Fingerprint() string {
	return a.Type + "/" + a.Subtype + "/" + a.Manufacturer
}

// TrackKind classifies a Logic channel strip.
type TrackKind string

const (
	// TrackKindAudio is an audio channel strip.
	TrackKindAudio TrackKind = "audio"
	// TrackKindInstrument is a software instrument channel strip.
	TrackKindInstrument TrackKind = "instrument"
	// TrackKindMaster is the master channel strip.
	TrackKindMaster TrackKind = "master"
	// TrackKindOutput is a physical output channel strip.
	TrackKindOutput TrackKind = "output"
	// TrackKindBus is a bus channel strip.
	TrackKindBus TrackKind = "bus"
	// TrackKindAux is an auxiliary channel strip.
	TrackKindAux TrackKind = "aux"
	// TrackKindInput is a physical input channel strip.
	TrackKindInput TrackKind = "input"
	// TrackKindUnknown is a channel strip whose descriptor is not recognized.
	TrackKindUnknown TrackKind = "unknown"
)

// Track contains a decoded channel strip and its plug-in chain.
type Track struct {
	Name       string
	Kind       TrackKind
	Offset     int
	Active     bool
	Instrument *AudioUnit
	MIDIFX     []AudioUnit
	AudioFX    []AudioUnit
	strip      uint16
}

// Plug-in instances are stored one per chunk, sharing a chunk type with other
// audio configuration data. The header variant selects them and the header's
// trailing field names the channel strip they belong to, so no plug-in has to
// be located by searching or matched to a strip by proximity.
const (
	pluginChunk        = "AuCU"
	pluginChainOffset  = 4
	pluginSlotOffset   = 6
	pluginSetting      = 14
	pluginSettingSize  = 64
	pluginName         = 120
	pluginNameSize     = 12
	pluginManufacturer = 132
	pluginType         = 136
	pluginSubtype      = 140
	pluginRecordSize   = 144
)

// pluginVariant marks an AuCU chunk as a plug-in instance.
var pluginVariant = []byte{0x05, 0x00, 0x0e, 0x00}

// emagic is the manufacturer code on every plug-in that ships with Logic.
const emagic = "EMAG"

// Chains within a channel strip. Logic keeps the instrument and the audio
// inserts in one chain and the MIDI effects in another. The remaining chains
// hold the strip's setting name and its interface state rather than plug-ins.
const (
	chainAudio  = 1
	chainMIDIFX = 2
)

// findAudioUnits decodes one plug-in instance per chunk, in file order.
func findAudioUnits(chunks []Chunk) []AudioUnit {
	var found []AudioUnit
	for _, chunk := range chunks {
		if chunk.Type != pluginChunk || !bytes.Equal(chunk.Header[4:8], pluginVariant) ||
			len(chunk.Data) < pluginRecordSize {
			continue
		}
		chain := binary.LittleEndian.Uint16(chunk.Data[pluginChainOffset:])
		if chain != chainAudio && chain != chainMIDIFX {
			continue
		}
		unit := AudioUnit{
			Name:         cString(chunk.Data[pluginName : pluginName+pluginNameSize]),
			Type:         componentCode(chunk.Data[pluginType:pluginSubtype]),
			Subtype:      componentCode(chunk.Data[pluginSubtype:pluginRecordSize]),
			Manufacturer: componentCode(chunk.Data[pluginManufacturer:pluginType]),
			Setting:      cString(chunk.Data[pluginSetting : pluginSetting+pluginSettingSize]),
			Slot:         binary.LittleEndian.Uint16(chunk.Data[pluginSlotOffset:]),
			Offset:       chunk.Offset,
			strip:        binary.LittleEndian.Uint16(chunk.Header[14:]),
		}
		if unit.Name == "" && unit.Manufacturer == "" {
			continue
		}
		unit.midi = chain == chainMIDIFX
		found = append(found, unit)
	}
	return found
}

// cString reads a NUL-padded name, rejecting anything not printable.
func cString(b []byte) string {
	if zero := bytes.IndexByte(b, 0); zero >= 0 {
		b = b[:zero]
	}
	if !printable(b) {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// componentCode reads a four-character code, which is empty when unset.
func componentCode(b []byte) string {
	if allZero(b) {
		return ""
	}
	if !printable4(b) {
		return ""
	}
	return reverse4(b)
}

// Channel strips are stored one per chunk. The chunk type is shared with other
// audio configuration data, so the header variant selects strips, and the
// record then sits at a fixed offset rather than needing to be searched for.
const (
	channelStripChunk       = "AuCO"
	channelStripRecord      = 60
	channelStripRecordSize  = 24
	channelStripHeaderStart = 4
)

// channelStripVariant is the header field that marks an AuCO chunk as a
// channel strip rather than another kind of audio configuration.
var channelStripVariant = []byte{0x07, 0x00, 0x0e, 0x00}

// findTracks decodes one channel strip per chunk. The record is a leading
// byte, a name padded to 16 bytes, and an 8-byte descriptor. Chunks are in
// file order, so the result is ordered by offset, which assignAudioUnits
// relies on.
func findTracks(chunks []Chunk) []Track {
	var tracks []Track
	for _, chunk := range chunks {
		if chunk.Type != channelStripChunk ||
			!bytes.Equal(chunk.Header[channelStripHeaderStart:channelStripHeaderStart+4], channelStripVariant) ||
			len(chunk.Data) < channelStripRecord+channelStripRecordSize {
			continue
		}
		record := chunk.Data[channelStripRecord : channelStripRecord+channelStripRecordSize]
		field := record[:16]
		nameEnd := 16
		if zero := bytes.IndexByte(field[1:], 0); zero >= 0 {
			nameEnd = zero + 1
		}
		if nameEnd == 1 || !allZero(field[nameEnd:]) || !printable(field[1:nameEnd]) {
			continue
		}
		name := strings.TrimSpace(string(field[1:nameEnd]))
		if name == "" {
			continue
		}
		descriptor := record[16:24]
		tracks = append(tracks, Track{
			Name: name, Kind: trackKind(descriptor),
			Offset: chunk.Offset + chunkHeaderSize + channelStripRecord,
			Active: descriptor[2]&0x04 != 0 || descriptor[4] != 0,
			strip:  binary.LittleEndian.Uint16(chunk.Header[14:]),
		})
	}
	return tracks
}

// assignAudioUnits attaches each plug-in to the channel strip named in its
// chunk header. Within a strip the instrument occupies slot zero of the audio
// chain and the inserts follow it, so a chain with gaps keeps its numbering.
func assignAudioUnits(tracks []Track, units []AudioUnit) {
	byStrip := make(map[uint16][]AudioUnit, len(tracks))
	for _, unit := range units {
		byStrip[unit.strip] = append(byStrip[unit.strip], unit)
	}
	for i := range tracks {
		track := &tracks[i]
		strip := byStrip[track.strip]
		slices.SortFunc(strip, func(a, b AudioUnit) int { return cmp.Compare(a.Slot, b.Slot) })
		for _, unit := range strip {
			switch {
			case unit.midi:
				track.MIDIFX = append(track.MIDIFX, unit)
			case unit.Slot == 0 && track.Kind == TrackKindInstrument && track.Instrument == nil:
				u := unit
				track.Instrument = &u
			default:
				track.AudioFX = append(track.AudioFX, unit)
			}
		}
	}
}

// trackKind classifies a channel strip from its 8-byte descriptor.
func trackKind(d []byte) TrackKind {
	switch d[0] {
	case 0x89:
		return TrackKindMaster
	case 0x49:
		return TrackKindOutput
	case 0xe9:
		return TrackKindBus
	case 0xab:
		if d[1] == 0xf5 {
			return TrackKindAux
		}
		return TrackKindAudio
	case 0x29:
		if d[2] == 0xf3 || d[2] == 0xf7 {
			return TrackKindInstrument
		}
		return TrackKindInput
	default:
		return TrackKindUnknown
	}
}
