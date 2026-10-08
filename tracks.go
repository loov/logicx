// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/loov/logicx/internal/record"
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
	chunk        *Chunk
	// name and setting are Name and Setting as decoded, so that Save writes
	// them only when they were changed.
	name, setting string
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

// Track contains a decoded channel strip, its mixer settings and its plug-in
// chain.
type Track struct {
	Name   string
	Kind   TrackKind
	Offset int
	Active bool
	// Volume is the fader as 8.24 fixed point: 90.0 is 0 dB, and Logic's
	// fader tops out at 127.0, about +6 dB. See [Track.VolumeDB].
	Volume uint32
	// Pan runs from -64, fully left, to 63, fully right.
	Pan             int8
	Mute            bool
	Solo            bool
	InputMonitoring bool
	Instrument      *AudioUnit
	MIDIFX          []AudioUnit
	AudioFX         []AudioUnit
	strip           uint16
	chunk           *Chunk
	// mixer reports whether the strip is long enough to hold the mixer
	// settings.
	mixer bool
	// name is Name as decoded, so that Save writes it only when it was
	// changed.
	name string
}

// Mixer settings within a channel strip chunk. The volume is stored twice:
// as 8.24 fixed point, and as its whole part alone.
const (
	channelStripMonitor  = 84 // 0x08 is input monitoring
	channelStripVolume7  = 85
	channelStripSolo     = 88 // 0x01 is solo
	channelStripPan      = 89 // 64 is center
	channelStripMute     = 90 // 0x01 is mute; 0x02 is muted by another's solo
	channelStripVolume   = 116
	channelStripMixerEnd = channelStripVolume + 4
)

// mixerFields is the layout of a strip's mixer settings; volume7 and pan
// hold the stored forms of the volume's whole part and of Pan.
func (t *Track) mixerFields(volume7, pan *uint8) []record.Field {
	return []record.Field{
		record.Bit(channelStripMonitor, 0x08, &t.InputMonitoring),
		record.Uint8(channelStripVolume7, volume7),
		record.Bit(channelStripSolo, 0x01, &t.Solo),
		record.Uint8(channelStripPan, pan),
		record.Bit(channelStripMute, 0x01, &t.Mute),
		record.Uint32LE(channelStripVolume, &t.Volume),
	}
}

// unityVolume is Track.Volume at 0 dB.
const unityVolume = 90 << 24

// VolumeDB returns the fader level in decibels, -Inf when it is all the way
// down. Logic's fader follows the square of its position: 40·log10(v/90).
func (t *Track) VolumeDB() float64 {
	return 40 * math.Log10(float64(t.Volume)/unityVolume)
}

// SetVolumeDB sets the fader level in decibels, limited to Logic's range.
func (t *Track) SetVolumeDB(db float64) {
	v := math.Round(unityVolume * math.Pow(10, db/40))
	t.Volume = uint32(min(max(v, 0), 127<<24))
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
func findAudioUnits(chunks []*Chunk) []AudioUnit {
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
			chunk:        chunk,
		}
		unit.name, unit.setting = unit.Name, unit.Setting
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
func findTracks(chunks []*Chunk) []Track {
	var tracks []Track
	for _, chunk := range chunks {
		if chunk.Type != channelStripChunk ||
			!bytes.Equal(chunk.Header[channelStripHeaderStart:channelStripHeaderStart+4], channelStripVariant) ||
			len(chunk.Data) < channelStripRecord+channelStripRecordSize {
			continue
		}
		strip := chunk.Data[channelStripRecord : channelStripRecord+channelStripRecordSize]
		field := strip[:16]
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
		descriptor := strip[16:24]
		track := Track{
			Name: name, Kind: trackKind(descriptor),
			Offset: chunk.Offset + chunkHeaderSize + channelStripRecord,
			Active: descriptor[2]&0x04 != 0 || descriptor[4] != 0,
			strip:  binary.LittleEndian.Uint16(chunk.Header[14:]),
			chunk:  chunk, name: name,
		}
		if len(chunk.Data) >= channelStripMixerEnd {
			var volume7, pan uint8
			record.Decode(chunk.Data, track.mixerFields(&volume7, &pan)...)
			track.Pan, track.mixer = int8(pan-64), true
		}
		tracks = append(tracks, track)
	}
	return tracks
}

// channelStripName is where the name sits within a channel strip chunk,
// after the record's leading byte.
const (
	channelStripName     = channelStripRecord + 1
	channelStripNameSize = 15
)

// Save writes t's mixer settings, and its name when it was changed, into the
// channel strip it was decoded from. Kind and Active are not written.
// ProjectData.Tracks is not updated; see [ProjectData.Refresh].
func (t *Track) Save() error {
	if t.chunk == nil {
		return errors.New("logicx: track was not decoded from a project")
	}
	var fields []record.Field
	if t.Name != t.name {
		if t.Name == "" || !printable([]byte(t.Name)) {
			return fmt.Errorf("logicx: track name %q is not non-empty printable ASCII", t.Name)
		}
		fields = append(fields, record.CString(channelStripName, channelStripNameSize, &t.Name))
	}
	if t.mixer {
		if t.Volume > 127<<24 || t.Pan < -64 {
			return fmt.Errorf("logicx: track %q: volume %#x or pan %d out of range", t.Name, t.Volume, t.Pan)
		}
		volume7, pan := uint8(t.Volume>>24), uint8(t.Pan+64)
		fields = append(fields, t.mixerFields(&volume7, &pan)...)
	}
	data, err := record.Encode(t.chunk.Data, fields...)
	if err != nil {
		return fmt.Errorf("logicx: track %q: %w", t.Name, err)
	}
	t.chunk.Data, t.name = data, t.Name
	return nil
}

// Save writes a's slot, and its name and setting when they were changed, into
// the plug-in chunk it was decoded from. The component description is not
// written: it identifies the plug-in whose state the chunk holds.
// ProjectData.AudioUnits and the tracks are not updated; see
// [ProjectData.Refresh].
func (a *AudioUnit) Save() error {
	if a.chunk == nil {
		return errors.New("logicx: plug-in was not decoded from a project")
	}
	fields := []record.Field{record.Uint16LE(pluginSlotOffset, &a.Slot)}
	for _, f := range []struct {
		value, decoded *string
		offset, size   int
	}{{&a.Name, &a.name, pluginName, pluginNameSize}, {&a.Setting, &a.setting, pluginSetting, pluginSettingSize}} {
		if *f.value == *f.decoded {
			continue
		}
		if !printable([]byte(*f.value)) {
			return fmt.Errorf("logicx: plug-in text %q is not printable ASCII", *f.value)
		}
		fields = append(fields, record.CString(f.offset, f.size, f.value))
	}
	data, err := record.Encode(a.chunk.Data, fields...)
	if err != nil {
		return fmt.Errorf("logicx: plug-in %q: %w", a.Name, err)
	}
	a.chunk.Data, a.name, a.setting = data, a.Name, a.Setting
	return nil
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
