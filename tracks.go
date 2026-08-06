// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
)

// AudioUnit identifies an Audio Unit found in a channel strip.
type AudioUnit struct {
	Name         string
	Type         string
	Subtype      string
	Manufacturer string
	Offset       int
}

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
}

// htmlTag matches the markup Logic embeds in some plug-in display names.
var htmlTag = regexp.MustCompile(`<[^>]+>`)

// findAudioUnits scans for Audio Unit component descriptions, which appear as
// three adjacent printable four-character codes with a known type in the
// middle. The scan is byte-wise because channel strips are not chunk-aligned.
func findAudioUnits(data []byte) []AudioUnit {
	var found []AudioUnit
	for off := 4; off+8 <= len(data); off++ {
		marker := string(data[off : off+4])
		if marker != "umua" && marker != "fmua" && marker != "imua" && marker != "xfua" {
			continue
		}
		manufacturer, typ, subtype := data[off-4:off], data[off:off+4], data[off+4:off+8]
		if !printable4(manufacturer) || !printable4(typ) || !printable4(subtype) {
			continue
		}
		found = append(found, AudioUnit{
			Name: extractName(data, off), Type: reverse4(typ), Subtype: reverse4(subtype),
			Manufacturer: reverse4(manufacturer), Offset: off,
		})
	}
	return found
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
		})
	}
	return tracks
}

// assignAudioUnits attaches each Audio Unit to the nearest preceding track.
// Both slices come from findTracks and findAudioUnits, so both are sorted by
// offset.
func assignAudioUnits(tracks []Track, units []AudioUnit) {
	if len(tracks) == 0 {
		return
	}
	for _, unit := range units {
		i := sort.Search(len(tracks), func(i int) bool { return tracks[i].Offset > unit.Offset }) - 1
		if i < 0 {
			continue
		}
		track := &tracks[i]
		switch unit.Type {
		case "aumu":
			if track.Kind == TrackKindInstrument && track.Instrument == nil {
				u := unit
				track.Instrument = &u
			}
		case "aumf", "aumi":
			track.MIDIFX = append(track.MIDIFX, unit)
		default:
			track.AudioFX = append(track.AudioFX, unit)
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

// extractName returns the last plausible printable run before off, which is
// where Logic stores the plug-in's display name.
func extractName(data []byte, off int) string {
	start := max(0, off-200)
	name := "<unknown>"
	for _, run := range printableRun.FindAll(data[start:off], -1) {
		s := string(run)
		if len(s) <= 4 || strings.Contains(s, "$class") || strings.Contains(s, "NS.") || strings.Contains(s, "bplist") || strings.Contains(s, "WNS.") {
			continue
		}
		s = strings.TrimSpace(htmlTag.ReplaceAllString(s, ""))
		if len(s) >= 4 {
			name = s
		}
	}
	return name
}
