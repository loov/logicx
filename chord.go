// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"encoding/binary"
	"math"
	"slices"

	"github.com/egonelbre/logicx/internal/record"
)

// Chord is a timed harmony recovered from a region or the project chord lane.
// Pitches contains MIDI note numbers. IntervalMask is relative to
// RootPitchClass. ScaleMask is the scale for scale events and the harmonic
// context for chord events; spelling values and Raw preserve Logic's codes.
type Chord struct {
	Position         uint32
	PositionFraction uint16
	Duration         uint32
	Name             string
	Pitches          []uint8
	SequenceID       uint32
	IntervalMask     uint16
	ScaleMask        uint16
	Attributes       uint32
	RootPitchClass   uint8
	RootSpelling     uint8
	NoChord          bool
	Scale            bool
	HasBass          bool
	BassPitchClass   uint8
	BassSpelling     uint8
	Raw              []byte
}

// Logic's chord-link locators precede project positions by 3840 ticks even
// when the project starts in 3/4 or 5/8.
const projectChordPositionBias = 4 * 960

type chordSequenceID struct{ group, sequence uint32 }

type chordLink struct {
	position         uint32
	positionFraction uint16
	sequence         uint32
}

func findProjectChords(chunks []Chunk) []Chord {
	events := make(map[chordSequenceID][]byte)
	durations := make(map[chordSequenceID]uint32)
	for _, chunk := range chunks {
		id := chordSequenceID{
			group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
			sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
		}
		switch {
		case chunk.Type == "EvSq":
			events[id] = chunk.Data
		case chunk.Type == "MSeq" && len(chunk.Data) >= 94 && sequenceName(chunk.Data) == "MIDI Region":
			durations[id] = binary.LittleEndian.Uint32(chunk.Data[90:94])
		}
	}

	var chords []Chord
	for _, chunk := range chunks {
		if chunk.Type != "MSeq" || sequenceName(chunk.Data) != "Global Harmonies" {
			continue
		}
		group := binary.LittleEndian.Uint32(chunk.Header[6:10])
		sequence := binary.LittleEndian.Uint32(chunk.Header[10:14])
		links := record.Scan(events[chordSequenceID{group, sequence}], 80, 80, decodeChordLink)
		for _, link := range links {
			decoded := record.Scan(events[chordSequenceID{group, link.sequence}], 32, 16, decodeChordEvent)
			if len(decoded) == 0 || link.position > math.MaxUint32-projectChordPositionBias {
				continue
			}
			// TODO(logicx): When edited sequences contain differing duplicate
			// events, which record identifies the active revision?
			chord := decoded[0]
			chord.Position = link.position + projectChordPositionBias
			chord.PositionFraction = link.positionFraction
			chord.Duration = durations[chordSequenceID{group, link.sequence}]
			chord.SequenceID = link.sequence
			chords = append(chords, chord)
		}
	}
	inferChordDurations(chords)
	return chords
}

func inferChordDurations(chords []Chord) {
	// TODO(logicx): Inline region chords have no child-sequence duration; how
	// far does their final chord extend after region clipping or looping?
	for i := 0; i+1 < len(chords); i++ {
		if chords[i].Duration == 0 && chords[i+1].Position > chords[i].Position {
			chords[i].Duration = chords[i+1].Position - chords[i].Position
		}
	}
}

func decodeChordLink(data []byte) (chordLink, bool) {
	var link chordLink
	ok := record.Decode(data,
		record.Equal(0, 0x20, 0, 0, 0),
		record.Uint16LE(2, &link.positionFraction),
		record.Uint32LE(4, &link.position),
		record.Equal(20, 1, 0, 0, 0x89),
		record.Uint32LE(32, &link.sequence),
		record.Equal(36, 0, 0, 0, 0x88),
		record.Equal(68, 0, 0, 0, 0x88),
	)
	return link, ok
}

func decodeChordEvent(data []byte) (Chord, bool) {
	var chord Chord
	var rootSpelling, bassSpelling uint8
	if len(data) < 16 || data[15] > 1 || !record.Decode(data,
		record.Equal(0, 0x70, 0, 0, 0),
		record.Uint16LE(2, &chord.PositionFraction),
		record.Uint32LE(4, &chord.Position),
		record.Equal(12, 0x67, 0, 0),
		record.Uint16LE(16, &chord.IntervalMask),
		record.Uint8(18, &bassSpelling),
		record.Uint8(19, &chord.BassPitchClass),
		record.Uint8(20, &rootSpelling),
		record.Uint8(21, &chord.RootPitchClass),
		record.Equal(23, 0xb2),
		record.Uint32LE(28, &chord.Attributes),
	) || chord.IntervalMask&^uint16(0x0fff) != 0 {
		return Chord{}, false
	}
	chord.Raw = slices.Clone(data)
	if chord.IntervalMask == 0 && chord.RootPitchClass == 15 && chord.Attributes == 0x00007f80 {
		chord.Name, chord.NoChord = "no chord", true
		return chord, true
	}
	if chord.RootPitchClass > 11 {
		return Chord{}, false
	}
	chord.Scale = chord.Attributes&0x80 == 0
	chord.ScaleMask = uint16(chord.Attributes>>16) & 0x0fff
	chord.RootSpelling = rootSpelling
	chord.HasBass = bassSpelling <= 4 && chord.BassPitchClass <= 11
	if chord.HasBass {
		chord.BassSpelling = bassSpelling
	}
	chord.Name = chordName(chord)
	pitchMask := chord.IntervalMask
	if chord.Scale {
		pitchMask = chord.ScaleMask
	}
	for interval := uint8(0); interval < 12; interval++ {
		if pitchMask&(1<<interval) != 0 {
			chord.Pitches = append(chord.Pitches, 60+chord.RootPitchClass+interval)
		}
	}
	if chord.HasBass {
		chord.Pitches = append(chord.Pitches, 48+chord.BassPitchClass)
		slices.Sort(chord.Pitches)
		chord.Pitches = slices.Compact(chord.Pitches)
	}
	return chord, true
}

func chordName(chord Chord) string {
	var suffix string
	var ok bool
	if chord.Scale {
		suffix, ok = chordScaleSuffix(chord.ScaleMask)
	} else {
		// TODO(logicx): How does the context mask disambiguate names that share
		// a pitch set, such as minor-third vs. sharp-nine?
		suffix, ok = map[uint16]string{
			0x091: "", 0x089: "m", 0x085: "sus2", 0x0a1: "sus4",
			0x081: "5", 0x111: "aug", 0x049: "dim", 0x291: "6", 0x489: "m7",
			0x491: "7", 0x891: "maj7", 0x093: " add b9", 0x095: " add 9",
			0x499: "7(#9)", 0x0b1: " add 11", 0x0d1: "(#11)",
			0x191: "(b13)", 0x695: "7(9,13)",
		}[chord.IntervalMask]
	}
	root, rootOK := spelledPitchClass(chord.RootPitchClass, chord.RootSpelling)
	if !ok || !rootOK {
		return ""
	}
	name := root + suffix
	if chord.HasBass {
		bass, ok := spelledPitchClass(chord.BassPitchClass, chord.BassSpelling)
		if !ok {
			return ""
		}
		name += "/" + bass
	}
	return name
}

func chordScaleSuffix(mask uint16) (string, bool) {
	suffix, ok := map[uint16]string{
		0x0ab5: " ionian",
		0x0ad5: " lydian",
		0x06b5: " mixolydian",
		0x09b5: " harmonic major",
		0x06d5: " mixolydian #11",
		0x05b5: " mixolydian b13",
		0x05b3: " phrygian dominant",
		0x09b3: " double harmonic",
		0x029d: " major blues",
		0x0593: " klezmer",
		0x0295: " major pentatonic",
		0x06db: " half-whole diminished",
	}[mask]
	return suffix, ok
}

func spelledPitchClass(pitch, spelling uint8) (string, bool) {
	naturals := [...]string{"C", "", "D", "", "E", "F", "", "G", "", "A", "", "B"}
	if pitch > 11 || spelling > 4 {
		return "", false
	}
	alter := int(spelling) - 2
	name := naturals[(int(pitch)-alter+12)%12]
	accidental := [...]string{"bb", "b", "", "#", "##"}
	return name + accidental[spelling], name != ""
}
