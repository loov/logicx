// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/loov/logicx/internal/record"
)

// Chord is a timed harmony recovered from a region or the project chord lane.
// Pitches contains MIDI note numbers. IntervalMask is relative to
// RootPitchClass. ScaleMask is the scale for scale events and the harmonic
// context for chord events; spelling values and Raw preserve Logic's codes.
//
// Position and PositionFraction place the chord in the arrangement, and
// Duration runs to the next chord or the end of its region; none of them is
// stored. SourcePosition and SourcePositionFraction are the values stored in
// the chord's sequence, and are what Save writes. A project chord's sequence
// is placed by a link on the global harmony track, which Save does not move.
type Chord struct {
	Position               uint32
	SourcePosition         uint32
	PositionFraction       uint16
	SourcePositionFraction uint16
	Duration               uint32
	Name                   string
	Pitches                []uint8
	SequenceID             uint32
	IntervalMask           uint16
	ScaleMask              uint16
	Attributes             uint32
	RootPitchClass         uint8
	RootSpelling           uint8
	NoChord                bool
	Scale                  bool
	HasBass                bool
	BassPitchClass         uint8
	BassSpelling           uint8
	Raw                    []byte
	// noBass is the stored bass spelling and pitch class of a chord without
	// a bass.
	noBass [2]uint8
	ref    eventRef
}

// Logic's chord-link locators precede project positions by 3840 ticks even
// when the project starts in 3/4 or 5/8.
const projectChordPositionBias = 4 * 960

// chordSequenceID identifies a sequence by the group and sequence numbers
// carried in every chunk header.
type chordSequenceID struct{ group, sequence uint32 }

// chunkSequenceID reads a chunk's sequence identity from its header.
func chunkSequenceID(chunk *Chunk) chordSequenceID {
	return chordSequenceID{
		group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
		sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
	}
}

// chordLink places a child chord sequence on the global harmony track.
type chordLink struct {
	position         uint32
	positionFraction uint16
	sequence         uint32
}

// findProjectChords decodes the global chord track: every "Global Harmonies"
// sequence links to child sequences that hold one chord event each.
func findProjectChords(chunks []*Chunk) []Chord {
	events := make(map[chordSequenceID]*Chunk)
	durations := make(map[chordSequenceID]uint32)
	for _, chunk := range chunks {
		id := chunkSequenceID(chunk)
		switch {
		case chunk.Type == "EvSq":
			events[id] = chunk
		case chunk.Type == "MSeq" && sequenceName(chunk.Data) == "MIDI Region":
			durations[id] = sequenceDuration(chunk.Data)
		}
	}

	var chords []Chord
	for _, chunk := range chunks {
		if chunk.Type != "MSeq" || sequenceName(chunk.Data) != "Global Harmonies" {
			continue
		}
		id := chunkSequenceID(chunk)
		for _, link := range decodeChordLinks(events[id].events()) {
			decoded := decodeChordEvents(events[chordSequenceID{id.group, link.sequence}])
			if len(decoded) == 0 || link.position > math.MaxUint32-projectChordPositionBias {
				continue
			}
			// The link identifies the active child; delete/recreate leaves the
			// previous child sequence orphaned in ProjectData.
			// A child holds one chord, or several when the chords are grouped;
			// the link places the first and the rest keep their spacing.
			base := link.position + projectChordPositionBias
			end := uint64(base) + uint64(durations[chordSequenceID{id.group, link.sequence}])
			shift := int64(base) - int64(decoded[0].Position)
			for i, chord := range decoded {
				// A re-entered chord leaves its predecessor behind at the same
				// position; only the last one at a position counts.
				if i+1 < len(decoded) && decoded[i+1].Position <= chord.Position {
					continue
				}
				position := int64(chord.Position) + shift
				if position < 0 || uint64(position) > math.MaxUint32 {
					continue
				}
				chord.Position = uint32(position)
				if i == 0 {
					chord.PositionFraction = link.positionFraction
				}
				switch {
				case i+1 < len(decoded):
					chord.Duration = decoded[i+1].Position - decoded[i].Position
				case end > uint64(position):
					chord.Duration = uint32(end - uint64(position))
				default:
					chord.Duration = 0
				}
				chord.SequenceID = link.sequence
				chords = append(chords, chord)
			}
		}
	}
	inferChordDurationsUntil(chords, 0)
	return chords
}

// inferChordDurationsUntil fills in zero durations from the distance to the
// next chord. The final chord extends to end, or keeps its zero duration when
// end is zero or not past it.
func inferChordDurationsUntil(chords []Chord, end uint32) {
	for i := 0; i+1 < len(chords); i++ {
		if chords[i].Duration == 0 && chords[i+1].Position > chords[i].Position {
			chords[i].Duration = chords[i+1].Position - chords[i].Position
		}
	}
	if len(chords) != 0 && chords[len(chords)-1].Duration == 0 && end > chords[len(chords)-1].Position {
		chords[len(chords)-1].Duration = end - chords[len(chords)-1].Position
	}
}

// decodeChordLinks decodes the locators of the global harmony track.
func decodeChordLinks(events []*Event) []chordLink {
	var links []chordLink
	for _, event := range events {
		if event.Type != eventLink {
			continue
		}
		if link, ok := decodeChordLink(event.Data); ok {
			links = append(links, link)
		}
	}
	return links
}

// decodeChordEvents decodes the chord and scale events of one sequence.
func decodeChordEvents(chunk *Chunk) []Chord {
	var chords []Chord
	for _, event := range chunk.events() {
		if event.Type != eventScore {
			continue
		}
		if chord, ok := decodeChordEvent(event.Data); ok {
			chord.ref = eventRef{chunk, event}
			chords = append(chords, chord)
		}
	}
	return chords
}

// decodeChordLink decodes an 80-byte locator on the global harmony track.
func decodeChordLink(data []byte) (chordLink, bool) {
	var link chordLink
	return link, record.Decode(data, link.fields()...)
}

// fields is the layout of a locator on the global harmony track.
func (l *chordLink) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0x20, 0),
		record.Uint16LE(2, &l.positionFraction),
		record.Uint32LE(4, &l.position),
		record.Equal(20, 1, 0, 0, 0x89),
		record.Uint32LE(32, &l.sequence),
		record.Equal(36, 0, 0, 0, 0x88),
		record.Equal(68, 0, 0, 0, 0x88),
	}
}

// decodeChordEvent decodes a chord or scale event. Ornaments, arpeggios and
// lyrics share this record type, so a mismatch here means the record is one of
// those rather than that it is malformed.
func decodeChordEvent(data []byte) (Chord, bool) {
	var chord Chord
	var bassSpelling uint8
	if len(data) < 16 || data[15] > 1 || !record.Decode(data, chord.fields(&bassSpelling)...) ||
		chord.IntervalMask&^uint16(0x0fff) != 0 {
		return Chord{}, false
	}
	chord.Position, chord.PositionFraction = chord.SourcePosition, chord.SourcePositionFraction
	chord.Raw = slices.Clone(data)
	chord.HasBass = bassSpelling <= 4 && chord.BassPitchClass <= 11
	if chord.HasBass {
		chord.BassSpelling = bassSpelling
	} else {
		chord.noBass = [2]uint8{bassSpelling, chord.BassPitchClass}
	}
	if chord.IntervalMask == 0 && chord.RootPitchClass == 15 && chord.Attributes == noChordAttributes {
		chord.Name, chord.NoChord = "no chord", true
		return chord, true
	}
	if chord.RootPitchClass > 11 {
		return Chord{}, false
	}
	chord.Scale = chord.Attributes&0x80 == 0
	chord.ScaleMask = uint16(chord.Attributes>>16) & 0x0fff
	chord.Name = chordName(chord)
	pitchMask := chord.IntervalMask
	if chord.Scale {
		pitchMask = chord.ScaleMask
	}
	for interval := range uint8(12) {
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

// noChordAttributes are the attributes of a "no chord" event.
const noChordAttributes = 0x00007f80

// fields is the layout of a chord or scale event. The bass spelling is held
// in bassSpelling, since BassSpelling is only set for a chord with a bass.
func (c *Chord) fields(bassSpelling *uint8) []record.Field {
	return []record.Field{
		record.Equal(0, 0x70, 0),
		record.Uint16LE(2, &c.SourcePositionFraction),
		record.Uint32LE(4, &c.SourcePosition),
		record.Equal(12, 0x67, 0, 0),
		record.Uint16LE(16, &c.IntervalMask),
		record.Uint8(18, bassSpelling),
		record.Uint8(19, &c.BassPitchClass),
		record.Uint8(20, &c.RootSpelling),
		record.Uint8(21, &c.RootPitchClass),
		record.Equal(23, 0xb2),
		record.Uint32LE(28, &c.Attributes),
	}
}

// Save writes c into the record it was decoded from, keeping the bytes this
// package does not decode, and moves the record when its source position
// changed. NoChord, Scale, ScaleMask and HasBass are folded into the stored
// masks, pitch classes and attributes; Name and Pitches are not read.
// The project's chords and sequences are not updated; see
// [ProjectData.Refresh].
func (c *Chord) Save() error {
	switch {
	case c.NoChord:
		c.IntervalMask, c.RootPitchClass, c.Attributes = 0, 15, noChordAttributes
	case c.RootPitchClass > 11 || c.RootSpelling > 4 || c.IntervalMask&^0x0fff != 0 || c.ScaleMask&^0x0fff != 0:
		return fmt.Errorf("logicx: chord root %d, spelling %d, masks %#x/%#x out of range",
			c.RootPitchClass, c.RootSpelling, c.IntervalMask, c.ScaleMask)
	default:
		c.Attributes = c.Attributes&^(0x0fff<<16|0x80) | uint32(c.ScaleMask)<<16
		if !c.Scale {
			c.Attributes |= 0x80
		}
	}
	bassSpelling := c.BassSpelling
	switch {
	case c.HasBass && (c.BassPitchClass > 11 || c.BassSpelling > 4):
		return fmt.Errorf("logicx: chord bass %d, spelling %d out of range", c.BassPitchClass, c.BassSpelling)
	case !c.HasBass && c.noBass == [2]uint8{}:
		bassSpelling, c.BassPitchClass = 7, 15
	case !c.HasBass:
		bassSpelling, c.BassPitchClass = c.noBass[0], c.noBass[1]
	}
	if err := c.ref.save("chord", c.fields(&bassSpelling)...); err != nil {
		return err
	}
	c.Raw = slices.Clone(c.ref.event.Data)
	return nil
}

// Delete removes c's record from the project. Removing the only chord of a
// project chord's sequence leaves its link pointing at an empty sequence.
func (c *Chord) Delete() error { return c.ref.delete("chord") }

// Duplicate inserts a copy of c's record after it and returns the copy, to be
// changed and saved. A copied project chord joins the original's sequence, as
// a grouped chord does.
func (c *Chord) Duplicate() (Chord, error) {
	ref, err := c.ref.duplicate("chord")
	copied := *c
	copied.Pitches, copied.Raw = slices.Clone(c.Pitches), slices.Clone(c.Raw)
	copied.ref = ref
	return copied, err
}

// chordSuffixes names an interval mask relative to the root.
//
// TODO(logicx): How does the context mask disambiguate names that share a
// pitch set, such as minor-third vs. sharp-nine?
var chordSuffixes = map[uint16]string{
	0x091: "", 0x089: "m", 0x085: "sus2", 0x0a1: "sus4",
	0x081: "5", 0x111: "aug", 0x049: "dim", 0x291: "6", 0x489: "m7",
	0x491: "7", 0x891: "maj7", 0x093: " add b9", 0x095: " add 9",
	0x499: "7(#9)", 0x0b1: " add 11", 0x0d1: "(#11)",
	0x191: "(b13)", 0x695: "7(9,13)",
}

// scaleSuffixes names a scale mask relative to the root.
var scaleSuffixes = map[uint16]string{
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
}

// chordName renders a chord or scale as text. It returns an empty name for
// masks and spellings this package cannot name, which callers treat as
// "decoded but not displayable".
func chordName(chord Chord) string {
	var suffix string
	var ok bool
	if chord.Scale {
		suffix, ok = scaleSuffixes[chord.ScaleMask]
	} else {
		suffix, ok = chordSuffixes[chord.IntervalMask]
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

// spelledPitchClass names a pitch class using Logic's spelling code, where 0
// is double-flat and 4 is double-sharp. It reports false for codes that do not
// land on a natural note name.
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
