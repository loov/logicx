// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/egonelbre/logicx/internal/record"
)

// MIDISequence is an active MIDI region discovered in ProjectData. Position
// and Duration are its arrangement bounds; looped events are expanded.
type MIDISequence struct {
	Name           string
	ChunkOffset    int
	SequenceID     uint32
	Position       uint32
	Duration       uint32
	SourceDuration uint32
	Looped         bool
	Notes          []MIDINote
	Chords         []Chord
}

// MIDINote contains the stable fields of Logic's 32-byte note record.
// Raw preserves the remaining undocumented fields.
type MIDINote struct {
	Position           uint32
	PositionFraction   uint16
	Pitch              uint8
	Duration           uint32
	Lyrics             []Lyric
	ScoreArticulations []ScoreArticulation
	ScoreFermatas      []ScoreFermata
	ScoreOrnaments     []ScoreOrnament
	ScoreArpeggios     []ScoreArpeggio
	ScoreSlurs         []ScoreSlur
	Raw                [32]byte
}

// Lyric is a score lyric attached to a MIDI note. Verse is zero when Logic
// does not assign an explicit verse number.
type Lyric struct {
	Position         uint32
	PositionFraction uint16
	Verse            uint8
	Text             string
	Raw              []byte
}

// ScoreArticulationKind identifies a notation symbol attached to a note.
type ScoreArticulationKind string

const (
	// ScoreArticulationUnknown is a symbol this package does not recognize;
	// ScoreArticulation.Code still carries Logic's value.
	ScoreArticulationUnknown ScoreArticulationKind = ""
	// ScoreArticulationStaccato is a staccato dot.
	ScoreArticulationStaccato ScoreArticulationKind = "staccato"
	// ScoreArticulationTenuto is a tenuto line.
	ScoreArticulationTenuto ScoreArticulationKind = "tenuto"
	// ScoreArticulationAccent is an accent.
	ScoreArticulationAccent ScoreArticulationKind = "accent"
	// ScoreArticulationMarcato is a marcato wedge; see Flipped for direction.
	ScoreArticulationMarcato ScoreArticulationKind = "marcato"
	// ScoreArticulationStaccatissimo is a staccatissimo wedge.
	ScoreArticulationStaccatissimo ScoreArticulationKind = "staccatissimo"
)

// ScoreArticulation is a Logic Score Editor symbol. Code and Raw preserve
// values that have not yet been decoded.
type ScoreArticulation struct {
	Kind    ScoreArticulationKind
	Code    uint8
	Flags   uint8
	Flipped bool
	Raw     [16]byte
}

// ScoreFermata is a Logic Score Editor fermata attached to a note.
type ScoreFermata struct {
	Inverted bool
	Code     uint8
	Raw      [16]byte
}

// ScoreSlurType identifies one endpoint of a reconstructed slur.
type ScoreSlurType string

const (
	// ScoreSlurTypeUnknown is an endpoint that could not be classified.
	ScoreSlurTypeUnknown ScoreSlurType = ""
	// ScoreSlurTypeStart is the first note under a slur.
	ScoreSlurTypeStart ScoreSlurType = "start"
	// ScoreSlurTypeStop is the last note under a slur.
	ScoreSlurTypeStop ScoreSlurType = "stop"
)

// ScoreSlurPlacement identifies an explicit slur placement. An empty value
// lets the notation program choose automatically.
type ScoreSlurPlacement string

const (
	// ScoreSlurPlacementAutomatic leaves placement to the notation program.
	ScoreSlurPlacementAutomatic ScoreSlurPlacement = ""
	// ScoreSlurPlacementAbove forces the slur above the staff.
	ScoreSlurPlacementAbove ScoreSlurPlacement = "above"
	// ScoreSlurPlacementBelow forces the slur below the staff.
	ScoreSlurPlacementBelow ScoreSlurPlacement = "below"
)

// ScoreSlur is one endpoint of a Logic Score Editor slur. Number pairs chord
// tones; Raw preserves every 16-byte segment record in the slur.
type ScoreSlur struct {
	Type      ScoreSlurType
	Placement ScoreSlurPlacement
	Number    uint8
	Code      uint8
	Raw       [][16]byte
}

// ScoreOrnamentKind identifies an ornament attached to a note.
type ScoreOrnamentKind string

const (
	// ScoreOrnamentUnknown is an ornament this package does not recognize;
	// ScoreOrnament.Code still carries Logic's value.
	ScoreOrnamentUnknown ScoreOrnamentKind = ""
	// ScoreOrnamentTurn is a turn.
	ScoreOrnamentTurn ScoreOrnamentKind = "turn"
	// ScoreOrnamentInvertedTurn is an inverted turn.
	ScoreOrnamentInvertedTurn ScoreOrnamentKind = "inverted-turn"
	// ScoreOrnamentInvertedTurnWithLine is an inverted turn with a vertical line.
	ScoreOrnamentInvertedTurnWithLine ScoreOrnamentKind = "inverted-turn-with-line"
	// ScoreOrnamentMordent is a mordent.
	ScoreOrnamentMordent ScoreOrnamentKind = "mordent"
	// ScoreOrnamentInvertedMordent is an inverted mordent.
	ScoreOrnamentInvertedMordent ScoreOrnamentKind = "inverted-mordent"
	// ScoreOrnamentTrill is a trill mark.
	ScoreOrnamentTrill ScoreOrnamentKind = "trill"
	// ScoreOrnamentTremolo is a tremolo.
	ScoreOrnamentTremolo ScoreOrnamentKind = "tremolo"
)

// ScoreOrnament is a positioned Logic Score Editor ornament.
type ScoreOrnament struct {
	Position         uint32
	PositionFraction uint16
	Kind             ScoreOrnamentKind
	Code             uint8
	Raw              [32]byte
}

// ScoreArpeggioDirection identifies an arpeggio's explicit direction.
type ScoreArpeggioDirection string

const (
	// ScoreArpeggioDirectionNone is an arpeggio without an explicit direction.
	ScoreArpeggioDirectionNone ScoreArpeggioDirection = ""
	// ScoreArpeggioDirectionUp is an upward arpeggio.
	ScoreArpeggioDirectionUp ScoreArpeggioDirection = "up"
	// ScoreArpeggioDirectionDown is a downward arpeggio.
	ScoreArpeggioDirectionDown ScoreArpeggioDirection = "down"
)

// ScoreArpeggio is a positioned Logic Score Editor arpeggio mark.
type ScoreArpeggio struct {
	Position         uint32
	PositionFraction uint16
	Direction        ScoreArpeggioDirection
	Code             uint8
	Raw              [32]byte
}

// Marker is a Logic global marker. RTF preserves the formatted source text;
// Name is its best-effort plain-text form.
type Marker struct {
	Position uint32
	Length   uint32
	TextID   uint32
	Name     string
	RTF      string
	Raw      [48]byte
}

// sequenceSource is a decoded event sequence before arrangement links place
// it. Its positions are relative to the source sequence, not the project.
type sequenceSource struct {
	id             chordSequenceID
	name           string
	chunkOffset    int
	duration       uint32
	positionOffset int32
	notes          []MIDINote
	chords         []Chord
}

// findMIDISequences pairs event sequences with their descriptors, then places
// each source on the timeline through the arrangement links that reference it.
// A source referenced by several links becomes several sequences.
func findMIDISequences(chunks []Chunk) []MIDISequence {
	descriptors := make(map[chordSequenceID]Chunk)
	events := make(map[chordSequenceID]Chunk)
	for _, chunk := range chunks {
		id := chunkSequenceID(chunk)
		switch chunk.Type {
		case "MSeq":
			descriptors[id] = chunk
		case "EvSq":
			events[id] = chunk
		}
	}

	var ordered []sequenceSource
	sources := make(map[chordSequenceID]sequenceSource)
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		id := chunkSequenceID(chunk)
		descriptor, ok := descriptors[id]
		if !ok {
			continue
		}
		notes := findMIDINotes(chunk.Data)
		chords := record.Scan(chunk.Data, 32, 16, decodeChordEvent)
		if len(notes) == 0 && len(chords) == 0 {
			continue
		}
		s := sequenceSource{
			id: id, name: sequenceName(descriptor.Data), chunkOffset: chunk.Offset,
			duration: sequenceDuration(descriptor.Data), positionOffset: sequencePositionOffset(descriptor.Data),
			notes: notes, chords: chords,
		}
		sources[id] = s
		ordered = append(ordered, s)
	}

	// Walk chunks rather than the events map so the result does not depend on
	// map iteration order.
	var sequences []MIDISequence
	for _, event := range chunks {
		id := chunkSequenceID(event)
		if event.Type != "EvSq" || events[id].Offset != event.Offset {
			continue
		}
		descriptor, ok := descriptors[id]
		if !ok || sequenceName(descriptor.Data) == "Global Harmonies" {
			continue
		}
		links := record.Scan(event.Data, 80, 80, decodeRegionLink)
		for _, link := range links {
			s, ok := sources[chordSequenceID{group: id.group, sequence: link.sequence}]
			if !ok {
				continue
			}
			sequence, valid := materializeRegion(s, link)
			if valid {
				sequences = append(sequences, sequence)
			}
		}
	}
	if len(sequences) != 0 {
		slices.SortFunc(sequences, func(a, b MIDISequence) int {
			return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ChunkOffset, b.ChunkOffset))
		})
		return sequences
	}

	// Older or partially recovered projects may not expose arrangement links.
	for _, s := range ordered {
		inferChordDurationsUntil(s.chords, 0)
		sequences = append(sequences, MIDISequence{
			Name: s.name, ChunkOffset: s.chunkOffset, SequenceID: s.id.sequence,
			Duration: s.duration, SourceDuration: s.duration, Notes: s.notes, Chords: s.chords,
		})
	}
	return sequences
}

const (
	// sequenceMetadataTail is the offset of the sequence duration counted back
	// from the end of an MSeq payload. MSeq names are variable-length, so these
	// fields are addressed from the stable end of the payload.
	sequenceMetadataTail = 219
	// noRegionLoop is the loop length Logic writes for an unlooped region.
	noRegionLoop = 0x3fffffff
)

// sequenceDuration returns the source length of an MSeq descriptor in ticks.
func sequenceDuration(data []byte) uint32 {
	if len(data) < sequenceMetadataTail {
		return 0
	}
	return binary.LittleEndian.Uint32(data[len(data)-sequenceMetadataTail:])
}

// sequencePositionOffset returns the signed shift between a source sequence's
// event positions and its placed position, which is non-zero for cropped
// regions.
func sequencePositionOffset(data []byte) int32 {
	if len(data) < 55 {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(data[len(data)-55:]))
}

// regionLink places a source sequence on the arrangement timeline.
type regionLink struct {
	position uint32
	duration uint32
	sequence uint32
}

// decodeRegionLink decodes an 80-byte arrangement locator.
func decodeRegionLink(data []byte) (regionLink, bool) {
	var link regionLink
	ok := record.Decode(data,
		record.Equal(0, 0x20, 0),
		record.Uint32LE(4, &link.position),
		record.Uint32LE(28, &link.duration),
		record.Uint32LE(32, &link.sequence),
		record.Equal(36, 0, 0, 0, 0x88),
		record.Equal(68, 0, 0, 0, 0x88),
	)
	return link, ok
}

// materializeRegion places a source sequence at a link's position, expanding
// loop repeats and trimming events that fall outside the region. It reports
// false when the placement would overflow the tick range.
func materializeRegion(s sequenceSource, link regionLink) (MIDISequence, bool) {
	if link.position > math.MaxUint32-projectChordPositionBias {
		return MIDISequence{}, false
	}
	position := link.position + projectChordPositionBias
	duration := s.duration
	looped := link.duration != 0 && link.duration != noRegionLoop
	if looped {
		duration = link.duration
	}
	end := uint64(position) + uint64(duration)
	if end > math.MaxUint32 {
		return MIDISequence{}, false
	}
	repeats := uint32(1)
	if looped && s.duration != 0 {
		repeats = uint32((uint64(duration) + uint64(s.duration) - 1) / uint64(s.duration))
	}
	sequence := MIDISequence{
		Name: s.name, ChunkOffset: s.chunkOffset, SequenceID: s.id.sequence,
		Position: position, Duration: duration, SourceDuration: s.duration, Looped: looped,
	}
	for repeat := uint32(0); repeat < repeats; repeat++ {
		baseShift := int64(s.positionOffset)
		repeatShift := int64(repeat) * int64(s.duration)
		for _, note := range s.notes {
			base := int64(note.Position) + baseShift
			if looped && base >= int64(position)+int64(s.duration) {
				continue
			}
			p := base + repeatShift
			if p < int64(position) || p < 0 || uint64(p) >= end || p > math.MaxUint32 {
				continue
			}
			note.Position = uint32(p)
			if uint64(note.Position)+uint64(note.Duration) > end {
				note.Duration = uint32(end - uint64(note.Position))
			}
			sequence.Notes = append(sequence.Notes, note)
		}
		for _, chord := range s.chords {
			base := int64(chord.Position) + baseShift
			if looped && base >= int64(position)+int64(s.duration) {
				continue
			}
			p := base + repeatShift
			if p < int64(position) || p < 0 || uint64(p) >= end || p > math.MaxUint32 {
				continue
			}
			chord.Position = uint32(p)
			sequence.Chords = append(sequence.Chords, chord)
		}
	}
	inferChordDurationsUntil(sequence.Chords, uint32(end))
	return sequence, true
}

// findMIDINotes decodes the notes of an event sequence along with the score
// symbols attached to them. Lyrics, ornaments and arpeggios precede the note
// they belong to; articulations, fermatas and slur segments follow it.
func findMIDINotes(data []byte) []MIDINote {
	var notes []MIDINote
	var lyrics []Lyric
	var ornaments []ScoreOrnament
	var arpeggios []ScoreArpeggio
	var slurSegments []scoreSlurSegment
	for offset := 0; offset+32 <= len(data); offset += 16 {
		if lyric, size, ok := decodeLyric(data[offset:]); ok {
			lyrics = append(lyrics, lyric)
			offset += size - 16
			continue
		}
		if ornament, ok := decodeScoreOrnament(data[offset : offset+32]); ok {
			ornaments = append(ornaments, ornament)
			offset += 16
			continue
		}
		if arpeggio, ok := decodeScoreArpeggio(data[offset : offset+32]); ok {
			arpeggios = append(arpeggios, arpeggio)
			offset += 16
			continue
		}
		note, ok := decodeMIDINote(data[offset : offset+32])
		if !ok {
			continue
		}
		for len(lyrics) > 0 && (lyrics[0].Position < note.Position ||
			lyrics[0].Position == note.Position && lyrics[0].PositionFraction <= note.PositionFraction) {
			if lyrics[0].Position == note.Position && lyrics[0].PositionFraction == note.PositionFraction {
				note.Lyrics = append(note.Lyrics, lyrics[0])
			}
			lyrics = lyrics[1:]
		}
		for len(ornaments) > 0 && scorePositionAtOrBefore(ornaments[0].Position, ornaments[0].PositionFraction, note) {
			if ornaments[0].Position == note.Position && ornaments[0].PositionFraction == note.PositionFraction {
				note.ScoreOrnaments = append(note.ScoreOrnaments, ornaments[0])
			}
			ornaments = ornaments[1:]
		}
		for len(arpeggios) > 0 && scorePositionAtOrBefore(arpeggios[0].Position, arpeggios[0].PositionFraction, note) {
			if arpeggios[0].Position == note.Position && arpeggios[0].PositionFraction == note.PositionFraction {
				note.ScoreArpeggios = append(note.ScoreArpeggios, arpeggios[0])
			}
			arpeggios = arpeggios[1:]
		}
		var slurSegment scoreSlurSegment
		for articulationOffset := offset + 32; articulationOffset+16 <= len(data); articulationOffset += 16 {
			if isScoreEventStart(data[articulationOffset:]) {
				break
			}
			if slur, ok := decodeScoreSlurSegment(data[articulationOffset : articulationOffset+16]); ok {
				slurSegment = slur
				continue
			}
			if fermata, ok := decodeScoreFermata(data[articulationOffset : articulationOffset+16]); ok {
				note.ScoreFermatas = append(note.ScoreFermatas, fermata)
				continue
			}
			articulation, ok := decodeScoreArticulation(data[articulationOffset : articulationOffset+16])
			if ok {
				note.ScoreArticulations = append(note.ScoreArticulations, articulation)
			}
		}
		notes = append(notes, note)
		slurSegments = append(slurSegments, slurSegment)
	}
	attachScoreSlurs(notes, slurSegments)
	return notes
}

// scorePositionAtOrBefore reports whether a symbol position is at or before
// the note's position.
func scorePositionAtOrBefore(position uint32, fraction uint16, note MIDINote) bool {
	return position < note.Position || position == note.Position && fraction <= note.PositionFraction
}

// decodeMIDINote decodes a 32-byte note-on record.
func decodeMIDINote(data []byte) (MIDINote, bool) {
	var note MIDINote
	ok := record.Decode(data,
		record.Equal(0, 0x90),
		record.Uint16LE(2, &note.PositionFraction),
		record.Uint32LE(4, &note.Position),
		record.Uint8(12, &note.Pitch),
		record.Equal(16, 0x40, 0, 0, 0, 0, 0, 0, 0x89, 0, 0, 0, 0),
		record.Uint32LE(28, &note.Duration),
		record.Copy(0, note.Raw[:]),
	)
	if !ok || note.Pitch > 127 {
		return MIDINote{}, false
	}
	return note, true
}

// decodeScoreArticulation decodes a 16-byte articulation record that trails a
// note.
func decodeScoreArticulation(data []byte) (ScoreArticulation, bool) {
	var articulation ScoreArticulation
	if !record.Decode(data,
		record.Equal(0, 0, 0, 0, 0),
		record.Uint8(4, &articulation.Code),
		record.Equal(5, 0),
		record.Uint8(6, &articulation.Flags),
		record.Equal(7, 0x85, 0, 0, 0, 0, 0, 0, 0, 0),
		record.Copy(0, articulation.Raw[:]),
	) || articulation.Code == 0 {
		return ScoreArticulation{}, false
	}
	switch articulation.Code {
	case 3:
		articulation.Kind = ScoreArticulationStaccato
	case 9:
		articulation.Kind = ScoreArticulationTenuto
	case 5:
		articulation.Kind = ScoreArticulationAccent
	case 6:
		articulation.Kind = ScoreArticulationMarcato
		articulation.Flipped = true
	case 7:
		articulation.Kind = ScoreArticulationMarcato
	case 4, 8:
		articulation.Kind = ScoreArticulationStaccatissimo
	}
	return articulation, true
}

// decodeScoreFermata decodes a 16-byte fermata record that trails a note.
func decodeScoreFermata(data []byte) (ScoreFermata, bool) {
	var fermata ScoreFermata
	if !record.Decode(data,
		record.Equal(0, 0, 0, 0, 0),
		record.Uint8(4, &fermata.Code),
		record.Equal(5, 0, 0, 0x85, 0, 0, 0, 0, 0, 0, 0, 0),
		record.Copy(0, fermata.Raw[:]),
	) || fermata.Code != 0 && fermata.Code != 19 {
		return ScoreFermata{}, false
	}
	fermata.Inverted = fermata.Code == 19
	return fermata, true
}

// scoreSlurSegment is the raw per-note slur marker Logic stores. Slur
// endpoints are reconstructed from runs of these by attachScoreSlurs.
type scoreSlurSegment struct {
	code uint8
	raw  [16]byte
}

// decodeScoreSlurSegment decodes a 16-byte slur marker that trails a note.
func decodeScoreSlurSegment(data []byte) (scoreSlurSegment, bool) {
	var segment scoreSlurSegment
	if !record.Decode(data,
		record.Equal(0, 0, 0, 0, 0, 0, 0, 0, 0x8c, 0, 0, 0, 0, 0, 0, 0),
		record.Uint8(15, &segment.code),
		record.Copy(0, segment.raw[:]),
	) || segment.code < 1 || segment.code > 3 {
		return scoreSlurSegment{}, false
	}
	return segment, true
}

// attachScoreSlurs turns per-note slur markers into start and stop endpoints.
// segments is parallel to notes. A slur spans from the marked note to the next
// note position; chord tones at both ends are paired by slur number.
func attachScoreSlurs(notes []MIDINote, segments []scoreSlurSegment) {
	consumed := make([]bool, len(notes))
	for start := 0; start < len(notes); start = nextNotePosition(notes, start) {
		segment := segments[start]
		if segment.code == 0 || consumed[start] {
			continue
		}
		end := nextNotePosition(notes, start)
		if end == len(notes) {
			continue
		}
		raw := [][16]byte{segment.raw}
		placement := ScoreSlurPlacementAutomatic
		switch segment.code {
		case 1:
			for end < len(notes) {
				continuation := segments[end]
				if continuation.code != 1 || consumed[end] {
					break
				}
				consumed[end] = true
				raw = append(raw, continuation.raw)
				end = nextNotePosition(notes, end)
			}
		case 2:
			placement = ScoreSlurPlacementAbove
		case 3:
			placement = ScoreSlurPlacementBelow
			if marker := segments[end]; marker.code == 1 {
				consumed[end] = true
				raw = append(raw, marker.raw)
			}
		}
		if end == len(notes) {
			continue
		}
		startEnd := nextNotePosition(notes, start)
		stopEnd := nextNotePosition(notes, end)
		count := min(startEnd-start, stopEnd-end)
		if count > 255 {
			count = 255
		}
		for i := range count {
			number := uint8(i + 1)
			notes[start+i].ScoreSlurs = append(notes[start+i].ScoreSlurs, ScoreSlur{
				Type: ScoreSlurTypeStart, Placement: placement, Number: number,
				Code: segment.code, Raw: slices.Clone(raw),
			})
			notes[end+i].ScoreSlurs = append(notes[end+i].ScoreSlurs, ScoreSlur{
				Type: ScoreSlurTypeStop, Number: number, Code: segment.code, Raw: slices.Clone(raw),
			})
		}
	}
}

// nextNotePosition returns the index of the first note after the chord that
// starts at index start, or len(notes).
func nextNotePosition(notes []MIDINote, start int) int {
	end := start + 1
	for end < len(notes) && notes[end].Position == notes[start].Position &&
		notes[end].PositionFraction == notes[start].PositionFraction {
		end++
	}
	return end
}

// decodeScoreOrnament decodes a positioned 32-byte ornament record.
func decodeScoreOrnament(data []byte) (ScoreOrnament, bool) {
	var ornament ScoreOrnament
	if !decodePositionedScoreSymbol(data, 0x42, &ornament.Position, &ornament.PositionFraction, &ornament.Code, ornament.Raw[:]) {
		return ScoreOrnament{}, false
	}
	switch ornament.Code {
	case 0:
		ornament.Kind = ScoreOrnamentTurn
	case 1:
		ornament.Kind = ScoreOrnamentInvertedTurnWithLine
	case 2:
		ornament.Kind = ScoreOrnamentInvertedMordent
	case 3:
		ornament.Kind = ScoreOrnamentMordent
	case 4:
		ornament.Kind = ScoreOrnamentTrill
	case 7:
		ornament.Kind = ScoreOrnamentTremolo
	case 19:
		ornament.Kind = ScoreOrnamentInvertedTurn
	}
	return ornament, true
}

// decodeScoreArpeggio decodes a positioned 32-byte arpeggio record.
func decodeScoreArpeggio(data []byte) (ScoreArpeggio, bool) {
	var arpeggio ScoreArpeggio
	if !decodePositionedScoreSymbol(data, 0x49, &arpeggio.Position, &arpeggio.PositionFraction, &arpeggio.Code, arpeggio.Raw[:]) || arpeggio.Code > 2 {
		return ScoreArpeggio{}, false
	}
	if arpeggio.Code == 1 {
		arpeggio.Direction = ScoreArpeggioDirectionUp
	} else if arpeggio.Code == 2 {
		arpeggio.Direction = ScoreArpeggioDirectionDown
	}
	return arpeggio, true
}

// decodePositionedScoreSymbol decodes the 32-byte record shared by the
// positioned score symbols, matching symbol as the record's discriminator.
func decodePositionedScoreSymbol(data []byte, symbol uint8, position *uint32, fraction *uint16, code *uint8, raw []byte) bool {
	return record.Decode(data,
		record.Equal(0, 0x70, 0),
		record.Uint16LE(2, fraction),
		record.Uint32LE(4, position),
		record.Equal(8, 0, 0, 0),
		record.Uint8(11, code),
		record.Equal(12, symbol, 0, 0, 1),
		record.Equal(23, 0x88),
		record.Copy(0, raw),
	)
}

// decodeLyric decodes a variable-length lyric record and returns its size in
// bytes, so the caller can skip past it.
func decodeLyric(data []byte) (Lyric, int, bool) {
	var lyric Lyric
	if !record.Decode(data,
		record.Equal(0, 0x70, 0),
		record.Uint16LE(2, &lyric.PositionFraction),
		record.Uint32LE(4, &lyric.Position),
		record.Equal(8, 0, 0, 0),
		record.Uint8(11, &lyric.Verse),
		record.Equal(12, 0x3d, 0, 0, 1),
	) {
		return Lyric{}, 0, false
	}
	size := nextScoreEvent(data)
	if size < 64 {
		return Lyric{}, 0, false
	}
	// ponytail: Logic's fixture-proven ASCII cells are decoded here; Raw
	// remains available if non-ASCII lyrics prove a different encoding.
	var text []byte
	for offset := 48; offset < size; offset += 8 {
		end := min(offset+8, size)
		for i := end - 1; i >= offset; i-- {
			if data[i] != 0 && data[i] != 0x88 {
				text = append(text, data[i])
			}
		}
	}
	lyric.Text = strings.TrimSpace(string(text))
	lyric.Raw = bytes.Clone(data[:size])
	return lyric, size, lyric.Text != ""
}

// nextScoreEvent returns the offset of the next event record after the one at
// the start of data. A record that is the last one in its sequence runs to the
// end of the data.
func nextScoreEvent(data []byte) int {
	for offset := 16; offset+16 <= len(data); offset += 16 {
		if isScoreEventStart(data[offset:]) {
			return offset
		}
	}
	return len(data) / 16 * 16
}

// isScoreEventStart reports whether data starts a note or positioned score
// event, which bounds the trailing records that belong to the previous note.
func isScoreEventStart(data []byte) bool {
	return len(data) >= 16 && (data[0] == 0x90 || data[0] == 0xb0 ||
		data[0] == 0x70 && data[1] == 0 && data[12] >= 0x3c)
}

// sequenceName returns the region name stored at the end of an MSeq payload.
func sequenceName(data []byte) string {
	runs := printableRun.FindAll(data, -1)
	if len(runs) == 0 {
		return "MIDI Sequence"
	}
	return strings.TrimSpace(string(runs[len(runs)-1]))
}

// findMarkers decodes global markers and resolves their text, sorted by
// position.
func findMarkers(chunks []Chunk) []Marker {
	texts := make(map[uint32]string)
	for _, chunk := range chunks {
		if chunk.Type == "TxSq" {
			textID := binary.LittleEndian.Uint32(chunk.Header[10:14])
			texts[textID] = markerRTF(chunk.Data)
		}
	}

	var markers []Marker
	for _, chunk := range chunks {
		if chunk.Type != "EvSq" {
			continue
		}
		markers = append(markers, record.Scan(chunk.Data, 48, 16, markerDecoder(texts))...)
	}
	slices.SortFunc(markers, func(a, b Marker) int { return cmp.Compare(a.Position, b.Position) })
	return markers
}

// markerDecoder returns a decoder for 48-byte marker records that resolves
// each marker's text through texts, rejecting markers whose text is missing.
func markerDecoder(texts map[uint32]string) func([]byte) (Marker, bool) {
	return func(data []byte) (Marker, bool) {
		var marker Marker
		if !record.Decode(data,
			record.Equal(0, 0x12, 0, 0, 0),
			record.Uint32LE(4, &marker.Position),
			record.Uint32LE(16, &marker.TextID),
			record.Equal(20, 0, 0, 0, 0x88),
			record.Uint32LE(28, &marker.Length),
			record.Copy(0, marker.Raw[:]),
		) {
			return Marker{}, false
		}
		rtf, ok := texts[marker.TextID]
		if !ok {
			return Marker{}, false
		}
		marker.Name, marker.RTF = plainRTF(rtf), rtf
		return marker, true
	}
}

// markerRTF extracts the RTF payload of a TxSq chunk.
func markerRTF(data []byte) string {
	start := bytes.Index(data, []byte(`{\rtf`))
	if start < 0 {
		return ""
	}
	return strings.TrimRight(string(data[start:]), "\x00")
}

// rtfControl matches an RTF control word.
var rtfControl = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?`)

// plainRTF reduces an RTF marker body to plain text.
func plainRTF(rtf string) string {
	runs := printableRun.FindAllString(rtf, -1)
	if len(runs) == 0 {
		return ""
	}
	// ponytail: Logic writes the marker body as the final printable RTF run;
	// use a full RTF decoder if projects with embedded/non-ASCII names appear.
	text := rtfControl.ReplaceAllString(runs[len(runs)-1], "")
	text = strings.NewReplacer(`\{`, "{", `\}`, "}", `\\`, `\`).Replace(text)
	return strings.Trim(text, "{} \t\r\n")
}
