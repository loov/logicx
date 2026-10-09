// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/loov/logicx/internal/record"
)

// MIDISequence is an active MIDI region discovered in ProjectData. Position
// and Duration are its arrangement bounds; looped events are expanded.
//
// A region places a source sequence, whose name and length (SourceDuration)
// are stored once for every region that places it. A looped region's
// Duration is its loop length; an unlooped one lasts SourceDuration. The
// source's events keep their positions when a region moves: a shift stored
// with the source follows the region instead, so a source placed by several
// regions cannot move with just one of them.
type MIDISequence struct {
	Name           string
	ChunkOffset    int
	SequenceID     uint32
	Position       uint32
	Duration       uint32
	SourceDuration uint32
	Looped         bool
	// Mute, Transpose, Velocity, Delay (in ticks), Quantize and Color are
	// the region's parameters, kept with its source. Quantize is Logic's
	// code: 0 is off and -8 a 1/8 note; the others are not decoded. Color is
	// a palette code, as [Track.Color]. They are zero, and Save keeps them,
	// for a source in the older layout.
	Mute      bool
	Transpose int8
	Velocity  int8
	Delay     int16
	Quantize  int16
	Color     uint8
	// Track is the name of the track the region is on, or empty when it is
	// not known. Save does not move a region between tracks.
	Track      string
	Notes      []MIDINote
	Chords     []Chord
	descriptor *Chunk
	// name is Name as decoded, so that Save writes it only when changed.
	name string
	// link is the arrangement locator placing the region, linkDuration its
	// stored loop length and position the region's position as decoded.
	// shared is set when other locators place the same source.
	link         eventRef
	linkDuration uint32
	position     uint32
	shared       bool
	// parameters reports whether the source holds the region parameters,
	// and params is them as decoded.
	parameters  bool
	params      regionParams
	trackObject uint32
}

// regionParams is the stored form of a region's parameters.
type regionParams struct {
	mute                       bool
	transpose, velocity, color uint8
	delay, quantize            uint16
}

// A region's parameters sit in its source's descriptor, from the end of its
// name, in a tail of sequenceParameterTail bytes; the link placing it repeats
// the mute, velocity and transpose and names the track's environment object.
const (
	sequenceParameterTail = 279
	sequenceColor         = 9
	sequenceQuantizeCopy  = 72
	sequenceMute          = 78 // 0x01
	sequenceDelay         = 98
	sequenceQuantize      = 120
	sequenceVelocity      = 160
	sequenceTranspose     = 161
	linkMute              = 12 // 0x01
	linkTrack             = 16
	linkVelocity          = 52
	linkTranspose         = 53
)

// descriptorFields is the layout of the region parameters in a source
// descriptor whose name ends at tail.
func (p *regionParams) descriptorFields(tail int) []record.Field {
	return []record.Field{
		record.Uint8(tail+sequenceColor, &p.color),
		record.Uint16LE(tail+sequenceQuantizeCopy, &p.quantize),
		record.Bit(tail+sequenceMute, 0x01, &p.mute),
		record.Uint16LE(tail+sequenceDelay, &p.delay),
		record.Uint16LE(tail+sequenceQuantize, &p.quantize),
		record.Uint8(tail+sequenceVelocity, &p.velocity),
		record.Uint8(tail+sequenceTranspose, &p.transpose),
	}
}

// linkFields is the layout of the parameters a region's link repeats.
func (p *regionParams) linkFields() []record.Field {
	return []record.Field{
		record.Bit(linkMute, 0x01, &p.mute),
		record.Uint8(linkVelocity, &p.velocity),
		record.Uint8(linkTranspose, &p.transpose),
	}
}

// decodeRegion decodes s's parameters and the track its link names.
func (s *MIDISequence) decodeRegion() {
	if s.link.event != nil {
		s.trackObject = binary.LittleEndian.Uint32(s.link.event.Data[linkTrack:])
	}
	data := s.descriptor.Data
	tail, ok := sequenceTail(data)
	if !ok || len(data)-tail != sequenceParameterTail {
		return
	}
	s.parameters = true
	record.Decode(data, s.params.descriptorFields(tail)...)
	p := s.params
	s.Mute, s.Transpose, s.Velocity = p.mute, int8(p.transpose), int8(p.velocity)
	s.Delay, s.Quantize, s.Color = int16(p.delay), int16(p.quantize), p.color
}

// regionParams returns s's parameters in their stored form.
func (s *MIDISequence) regionParams() regionParams {
	return regionParams{
		mute: s.Mute, transpose: uint8(s.Transpose), velocity: uint8(s.Velocity), color: s.Color,
		delay: uint16(s.Delay), quantize: uint16(s.Quantize),
	}
}

// assignRegionTracks names the track each region is on.
func assignRegionTracks(sequences []MIDISequence, tracks []Track) {
	names := make(map[uint32]string, len(tracks))
	for _, t := range tracks {
		if t.environment != nil {
			names[chunkSequenceID(t.environment).sequence] = t.Name
		}
	}
	for i := range sequences {
		sequences[i].Track = names[sequences[i].trackObject]
	}
}

// MIDINote contains the stable fields of Logic's 32-byte note record.
// Raw preserves the remaining undocumented fields.
//
// Position and Duration place the note in the arrangement: shifted by its
// region, repeated by a looped region and cut at the region's end.
// SourcePosition and SourceDuration are the values stored in the region's
// sequence, and are what Save writes. Every repeat of a looped note shares one
// record, so saving any of them changes them all.
//
// Velocity is the note-on velocity, 1 to 127. It was found from its values
// across 16,278 notes in 46 projects — always within that range, spread as
// played velocities are, peaking at Logic's default of 80 — and confirmed by
// Logic showing a saved velocity in its inspector. Channel, ReleaseVelocity,
// ArticulationID and Muted were each found by changing one note in Logic and
// comparing the saves. ReleaseVelocity is 0 to 127, and Logic also stores 128,
// whose meaning is unknown.
type MIDINote struct {
	Position           uint32
	SourcePosition     uint32
	PositionFraction   uint16
	Pitch              uint8
	Velocity           uint8
	Channel            uint8
	ReleaseVelocity    uint8
	ArticulationID     uint8
	Muted              bool
	Duration           uint32
	SourceDuration     uint32
	Lyrics             []Lyric
	ScoreArticulations []ScoreArticulation
	ScoreFermatas      []ScoreFermata
	ScoreOrnaments     []ScoreOrnament
	ScoreArpeggios     []ScoreArpeggio
	ScoreSlurs         []ScoreSlur
	Attributes         NoteAttributes
	Raw                [32]byte
	ref                eventRef
	// flags holds byte 15: 0x10 is Muted, 0x80 marks a selected note, and
	// 0x01 and 0x04 are not understood.
	flags uint8
}

// Lyric is a score lyric attached to a MIDI note. Verse is zero when Logic
// does not assign an explicit verse number.
type Lyric struct {
	Position         uint32
	PositionFraction uint16
	Verse            uint8
	Text             string
	Raw              []byte
	text             string
	ref              eventRef
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
	ref     atomRef
}

// ScoreFermata is a Logic Score Editor fermata attached to a note.
type ScoreFermata struct {
	Inverted bool
	Code     uint8
	Raw      [16]byte
	ref      atomRef
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
	ref              eventRef
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
	ref              eventRef
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
	ref      eventRef
	// text is the chunk holding the marker's text, and name Name as decoded,
	// so that Save rewrites the text only when Name changed.
	text *Chunk
	name string
}

// sequenceSource is a decoded event sequence before arrangement links place
// it. Its positions are relative to the source sequence, not the project.
type sequenceSource struct {
	id             chordSequenceID
	descriptor     *Chunk
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
func findMIDISequences(chunks []*Chunk) []MIDISequence {
	descriptors := make(map[chordSequenceID]*Chunk)
	events := make(map[chordSequenceID]*Chunk)
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
		notes := findMIDINotes(chunk)
		chords := decodeChordEvents(chunk)
		if len(notes) == 0 && len(chords) == 0 {
			continue
		}
		s := sequenceSource{
			id: id, descriptor: descriptor, name: sequenceName(descriptor.Data), chunkOffset: chunk.Offset,
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
		for _, link := range decodeRegionLinks(event) {
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
	placements := make(map[uint32]int)
	for _, s := range sequences {
		placements[s.SequenceID]++
	}
	for i := range sequences {
		sequences[i].shared = placements[sequences[i].SequenceID] > 1
		sequences[i].decodeRegion()
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
			descriptor: s.descriptor, name: s.name,
		})
		sequences[len(sequences)-1].decodeRegion()
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

// sequenceOffsetTail is the offset of the position shift counted back from
// the end of an MSeq payload.
const sequenceOffsetTail = 55

// sequencePositionOffset returns the signed shift between a source sequence's
// event positions and its placed position. Logic leaves events where they are
// when a region moves or is cropped, and changes this instead.
func sequencePositionOffset(data []byte) int32 {
	if len(data) < sequenceOffsetTail {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(data[len(data)-sequenceOffsetTail:]))
}

// regionLink places a source sequence on the arrangement timeline.
type regionLink struct {
	position uint32
	duration uint32
	sequence uint32
	ref      eventRef
}

// decodeRegionLinks decodes the arrangement locators of one sequence.
func decodeRegionLinks(chunk *Chunk) []regionLink {
	var links []regionLink
	for _, event := range chunk.Events {
		if event.Type != eventLink {
			continue
		}
		if link, ok := decodeRegionLink(event.Data); ok {
			link.ref = eventRef{chunk, event}
			links = append(links, link)
		}
	}
	return links
}

// decodeRegionLink decodes an 80-byte arrangement locator.
func decodeRegionLink(data []byte) (regionLink, bool) {
	var link regionLink
	return link, record.Decode(data, link.fields()...)
}

// fields is the layout of an arrangement locator.
func (l *regionLink) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0x20, 0),
		record.Uint32LE(4, &l.position),
		record.Uint32LE(28, &l.duration),
		record.Uint32LE(32, &l.sequence),
		record.Equal(36, 0, 0, 0, 0x88),
		record.Equal(68, 0, 0, 0, 0x88),
	}
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
		descriptor: s.descriptor, name: s.name, link: link.ref, linkDuration: link.duration, position: position,
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
// symbols attached to them. Lyrics, ornaments and arpeggios are separate
// records preceding the note they belong to; articulations, fermatas and slur
// segments are extra atoms of the note record itself.
func findMIDINotes(chunk *Chunk) []MIDINote {
	var notes []MIDINote
	var lyrics []Lyric
	var ornaments []ScoreOrnament
	var arpeggios []ScoreArpeggio
	var slurSegments []scoreSlurSegment
	for _, event := range chunk.Events {
		ref := eventRef{chunk, event}
		if event.Type == eventScore {
			if lyric, ok := decodeLyric(event.Data); ok {
				lyric.ref = ref
				lyrics = append(lyrics, lyric)
			} else if ornament, ok := decodeScoreOrnament(event.Data); ok {
				ornament.ref = ref
				ornaments = append(ornaments, ornament)
			} else if arpeggio, ok := decodeScoreArpeggio(event.Data); ok {
				arpeggio.ref = ref
				arpeggios = append(arpeggios, arpeggio)
			}
			continue
		}
		if !isNote(event.Type) {
			continue
		}
		note, ok := decodeMIDINote(event.Data)
		if !ok {
			continue
		}
		note.ref = ref
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
		for offset := 32; offset+16 <= len(event.Data); offset += 16 {
			atom := event.Data[offset : offset+16]
			if note.Attributes.decodeAtom(atom) {
				continue
			}
			if slur, ok := decodeScoreSlurSegment(atom); ok {
				slurSegment = slur
				continue
			}
			if fermata, ok := decodeScoreFermata(atom); ok {
				fermata.ref = atomRef{ref, offset, atom[7]}
				note.ScoreFermatas = append(note.ScoreFermatas, fermata)
				continue
			}
			if articulation, ok := decodeScoreArticulation(atom); ok {
				articulation.ref = atomRef{ref, offset, atom[7]}
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
	var status, release uint8
	if !record.Decode(data, append(note.fields(&status, &release), record.Copy(0, note.Raw[:]))...) ||
		!isNote(status) || note.Pitch > 127 {
		return MIDINote{}, false
	}
	note.Channel = status&0x0f + 1
	note.Muted = note.flags&noteMuted != 0
	note.Position, note.Duration = note.SourcePosition, note.SourceDuration
	return note, true
}

// noteMuted is the flag of a muted note.
const noteMuted = 0x10

// fields is the layout of a note record. status is the MIDI status byte,
// which holds the channel, and release the release velocity widened to eight
// bits, which Logic stores beside it.
func (n *MIDINote) fields(status, release *uint8) []record.Field {
	return []record.Field{
		record.Uint8(0, status),
		record.Uint16LE(2, &n.PositionFraction),
		record.Uint32LE(4, &n.SourcePosition),
		record.Uint8(11, &n.Velocity),
		record.Uint8(12, &n.Pitch),
		record.Equal(13, 0),
		record.Uint8(14, &n.ArticulationID),
		record.Uint8(15, &n.flags),
		record.Uint8(16, &n.ReleaseVelocity),
		record.Uint8(17, release),
		// Bytes 18..22 and 26..27 carry per-note values such as tuning, which
		// Melodyne transcriptions fill in; only the record markers are fixed.
		record.Equal(23, 0x89, 0, 0),
		record.Uint32LE(28, &n.SourceDuration),
	}
}

// NoteAttributes are a note's Note Attributes in the Score Editor; the zero
// value leaves every one at its default. Logic stores them in trailing atoms
// of the note record, each atom only while one of its attributes is set.
//
// Each was found by setting it on a note in Logic and comparing the saves, or
// by writing it and reading it back in Logic's Note Attributes dialog.
type NoteAttributes struct {
	EnharmonicShift EnharmonicShift
	AccidentalType  AccidentalType
	// AccidentalPosition moves the accidental horizontally.
	AccidentalPosition int8
	NoteHead           NoteHead
	Tie                Tie
	StemDirection      StemDirection
	StemPosition       StemPosition
	Syncopation        Syncopation
	Interpretation     Interpretation
	// HorizontalPosition moves the note horizontally. Logic shows it
	// unsigned, 0 to 255, unlike the signed accidental position and size.
	HorizontalPosition uint8
	// Size changes the note's size.
	Size int8
}

// EnharmonicShift respells a note by the given number of semitones.
type EnharmonicShift int8

const (
	EnharmonicDoubleFlat  EnharmonicShift = -2
	EnharmonicFlat        EnharmonicShift = -1
	EnharmonicNone        EnharmonicShift = 0
	EnharmonicSharp       EnharmonicShift = 1
	EnharmonicDoubleSharp EnharmonicShift = 2
)

// AccidentalType forces, hides or guides a note's accidental.
type AccidentalType uint8

const (
	AccidentalAuto  AccidentalType = 0
	AccidentalGuide AccidentalType = 0x08
	AccidentalForce AccidentalType = 0x10
	AccidentalHide  AccidentalType = 0x20
)

// NoteHead is the shape of a note's head. Logic shows 2 as Default too.
type NoteHead uint8

const (
	NoteHeadDefault            NoteHead = 0
	NoteHeadSlantedOval        NoteHead = 1
	NoteHeadHidden             NoteHead = 3
	NoteHeadCross              NoteHead = 4 // ×
	NoteHeadCircledCross       NoteHead = 5 // ⊗
	NoteHeadDiamond            NoteHead = 6 // ◇
	NoteHeadFilledDiamond      NoteHead = 7 // ◆
	NoteHeadTriangle           NoteHead = 8 // △
	NoteHeadFilledTriangle     NoteHead = 9 // ▲
	NoteHeadSlash              NoteHead = 10
	NoteHeadBoldCross          NoteHead = 11
	NoteHeadParallelogram      NoteHead = 12 // filled
	NoteHeadCircledBoldCross   NoteHead = 13
	NoteHeadDownTriangle       NoteHead = 14 // ▽
	NoteHeadFilledDownTriangle NoteHead = 15 // ▼
)

// Tie is the direction of a note's tie.
type Tie uint8

const (
	TieDefault Tie = 0
	TieUp      Tie = 1
	TieDown    Tie = 2
	TieHide    Tie = 3
)

// StemDirection is the direction of a note's stem.
type StemDirection uint8

const (
	StemDefault StemDirection = 0
	StemUp      StemDirection = 1
	StemDown    StemDirection = 2
	StemHide    StemDirection = 3
)

// StemPosition is where a note's stem attaches; the codes do not follow the
// menu's order.
type StemPosition uint8

const (
	StemPositionDefault   StemPosition = 0
	StemPositionCenter    StemPosition = 1
	StemPositionAutomatic StemPosition = 2
	StemPositionSide      StemPosition = 3
)

// Syncopation forces or defeats a note's syncopated display.
type Syncopation uint8

const (
	SyncopationDefault Syncopation = 0
	SyncopationForce   Syncopation = 1
	SyncopationDefeat  Syncopation = 2
)

// Interpretation forces or defeats a note's display interpretation.
type Interpretation uint8

const (
	InterpretationDefault Interpretation = 0
	InterpretationForce   Interpretation = 1
	InterpretationDefeat  Interpretation = 2
)

// Tags of the note attribute atoms, in the order Logic stores them, ahead of
// a note's articulations.
const (
	atomStem       = 0x81 // stem, syncopation and interpretation
	atomAccidental = 0x82 // spelling, accidental, note head and tie
	atomPlacement  = 0x83 // horizontal position and size
)

// decodeAtom reads the attributes an attribute atom holds, reporting whether
// atom is one.
func (a *NoteAttributes) decodeAtom(atom []byte) bool {
	switch atom[7] {
	case atomStem:
		a.Syncopation, a.Interpretation = Syncopation(atom[2]&0x03), Interpretation(atom[2]>>4&0x03)
		a.StemDirection, a.StemPosition = StemDirection(atom[6]&0x03), StemPosition(atom[6]>>2&0x03)
	case atomAccidental:
		if atom[4]&0x07 > 4 {
			return false
		}
		a.EnharmonicShift = EnharmonicShift(atom[4]&0x07) - 2
		a.AccidentalType = AccidentalType(atom[4] & 0x38)
		a.AccidentalPosition = int8(atom[5])
		a.NoteHead, a.Tie = NoteHead(atom[6]&0x1f), Tie(atom[6]>>5&0x03)
	case atomPlacement:
		a.HorizontalPosition, a.Size = atom[4], int8(atom[6])
	default:
		return false
	}
	return true
}

// encodeAtom writes the attributes of the atom tagged tag over atom, a copy of
// the existing one or nil, keeping the bits no attribute uses.
func (a NoteAttributes) encodeAtom(tag byte, atom []byte) []byte {
	out := make([]byte, atomSize)
	copy(out, atom)
	out[7] = tag
	switch tag {
	case atomStem:
		out[2] = out[2]&^0x33 | uint8(a.Syncopation) | uint8(a.Interpretation)<<4
		out[6] = out[6]&^0x0f | uint8(a.StemDirection) | uint8(a.StemPosition)<<2
	case atomAccidental:
		out[4] = out[4]&^0x3f | uint8(a.EnharmonicShift+2) | uint8(a.AccidentalType)
		out[5] = uint8(a.AccidentalPosition)
		out[6] = out[6]&^0x7f | uint8(a.NoteHead) | uint8(a.Tie)<<5
	case atomPlacement:
		out[4], out[6] = a.HorizontalPosition, uint8(a.Size)
	}
	return out
}

// validate reports attributes that do not fit their stored fields.
func (a NoteAttributes) validate() error {
	switch {
	case a.EnharmonicShift < EnharmonicDoubleFlat || a.EnharmonicShift > EnharmonicDoubleSharp:
		return fmt.Errorf("logicx: enharmonic shift %d out of range", a.EnharmonicShift)
	case a.AccidentalType != AccidentalAuto && a.AccidentalType != AccidentalGuide &&
		a.AccidentalType != AccidentalForce && a.AccidentalType != AccidentalHide:
		return fmt.Errorf("logicx: accidental type %#x not known", uint8(a.AccidentalType))
	case a.NoteHead > 0x1f || a.Tie > 3 || a.StemDirection > 3 || a.StemPosition > 3 || a.Syncopation > 3 || a.Interpretation > 3:
		return fmt.Errorf("logicx: note attributes %+v out of range", a)
	}
	return nil
}

// rewrite returns a note record with its attribute atoms set from a: an atom
// is added, in tag order, when one of its attributes is set, and removed when
// none is and it holds nothing else.
func (a NoteAttributes) rewrite(data []byte) []byte {
	var atoms [][]byte
	for offset := 32; offset+atomSize <= len(data); offset += atomSize {
		atoms = append(atoms, data[offset:offset+atomSize])
	}
	for _, tag := range []byte{atomStem, atomAccidental, atomPlacement} {
		i := slices.IndexFunc(atoms, func(atom []byte) bool { return atom[7] == tag })
		var existing []byte
		if i >= 0 {
			existing = atoms[i]
		}
		atom := a.encodeAtom(tag, existing)
		unset := bytes.Equal(atom, NoteAttributes{}.encodeAtom(tag, nil))
		switch {
		case i >= 0 && unset:
			atoms = slices.Delete(atoms, i, i+1)
		case i >= 0:
			atoms[i] = atom
		case !unset:
			at := slices.IndexFunc(atoms, func(atom []byte) bool { return atom[7] > tag })
			if at < 0 {
				at = len(atoms)
			}
			atoms = slices.Insert(atoms, at, atom)
		}
	}
	out := slices.Clip(data[:32])
	for _, atom := range atoms {
		out = append(out, atom...)
	}
	return out
}

// relinkAtoms points n's articulations and fermatas at their atoms again,
// after attribute atoms were added or removed ahead of them.
func (n *MIDINote) relinkAtoms() {
	data := n.ref.event.Data
	articulation, fermata := 0, 0
	for offset := 32; offset+atomSize <= len(data); offset += atomSize {
		atom := data[offset : offset+atomSize]
		if _, ok := decodeScoreSlurSegment(atom); ok {
			continue
		}
		if _, ok := decodeScoreFermata(atom); ok && fermata < len(n.ScoreFermatas) {
			n.ScoreFermatas[fermata].ref = atomRef{n.ref, offset, atom[7]}
			fermata++
			continue
		}
		if _, ok := decodeScoreArticulation(atom); ok && articulation < len(n.ScoreArticulations) {
			n.ScoreArticulations[articulation].ref = atomRef{n.ref, offset, atom[7]}
			articulation++
		}
	}
}

// widenRelease returns the eight-bit form of a release velocity that Logic
// stores beside it: the part above 64, widened from six bits by repeating its
// top bits, and zero for 64 and below. It holds for all 18,051 notes in 278
// projects.
func widenRelease(velocity uint8) uint8 {
	if velocity <= 64 || velocity > 127 {
		return 0
	}
	v := velocity - 64
	return v<<2 | v>>4
}

// Save writes n's pitch, velocity, release velocity, channel, articulation ID,
// muting, source position and duration into the record
// it was decoded from, keeping the bytes this package does not decode, and
// moves the record when its position changed. Attached symbols are saved on
// their own. The project's sequences are not updated; see
// [ProjectData.Refresh].
func (n *MIDINote) Save() error {
	if n.Pitch > 127 || n.Velocity < 1 || n.Velocity > 127 || n.ReleaseVelocity > 128 || n.Channel < 1 || n.Channel > 16 {
		return fmt.Errorf("logicx: note pitch %d, velocity %d, release velocity %d or channel %d out of range",
			n.Pitch, n.Velocity, n.ReleaseVelocity, n.Channel)
	}
	if err := n.Attributes.validate(); err != nil {
		return err
	}
	if err := n.ref.check("note"); err != nil {
		return err
	}
	if data := n.Attributes.rewrite(n.ref.event.Data); !bytes.Equal(data, n.ref.event.Data) {
		n.ref.event.Data = data
		n.relinkAtoms()
	}
	status, release := eventNote|(n.Channel-1), widenRelease(n.ReleaseVelocity)
	n.flags &^= noteMuted
	if n.Muted {
		n.flags |= noteMuted
	}
	if err := n.ref.save("note", n.fields(&status, &release)...); err != nil {
		return err
	}
	copy(n.Raw[:], n.ref.event.Data)
	return nil
}

// Delete removes n's record, with the attributes, articulations, fermatas and
// slur markers stored in it. Lyrics, ornaments and arpeggios are records of their
// own and are kept.
func (n *MIDINote) Delete() error { return n.ref.delete("note") }

// Duplicate inserts a copy of n's record after it and returns the copy, to be
// changed and saved. The copy carries n's articulations, fermatas and slur
// markers but not its lyrics, ornaments or arpeggios.
func (n *MIDINote) Duplicate() (MIDINote, error) {
	ref, err := n.ref.duplicate("note")
	if err != nil {
		return MIDINote{}, err
	}
	copied := *n
	copied.ref = ref
	copied.Lyrics, copied.ScoreOrnaments, copied.ScoreArpeggios = nil, nil, nil
	copied.ScoreArticulations = slices.Clone(n.ScoreArticulations)
	for i := range copied.ScoreArticulations {
		copied.ScoreArticulations[i].ref.eventRef = ref
	}
	copied.ScoreFermatas = slices.Clone(n.ScoreFermatas)
	for i := range copied.ScoreFermatas {
		copied.ScoreFermatas[i].ref.eventRef = ref
	}
	copied.ScoreSlurs = slices.Clone(n.ScoreSlurs)
	return copied, nil
}

// articulationKind names an articulation code, and reports whether the
// symbol is drawn flipped.
func articulationKind(code uint8) articulationName {
	switch code {
	case 3:
		return articulationName{ScoreArticulationStaccato, false}
	case 9:
		return articulationName{ScoreArticulationTenuto, false}
	case 5:
		return articulationName{ScoreArticulationAccent, false}
	case 6:
		return articulationName{ScoreArticulationMarcato, true}
	case 7:
		return articulationName{ScoreArticulationMarcato, false}
	case 4, 8:
		return articulationName{ScoreArticulationStaccatissimo, false}
	}
	return articulationName{}
}

// articulationName is what an articulation code means.
type articulationName struct {
	kind    ScoreArticulationKind
	flipped bool
}

// articulationCodes are the codes articulationKind names.
var articulationCodes = []uint8{3, 9, 5, 6, 7, 4, 8}

// decodeScoreArticulation decodes a 16-byte articulation record that trails a
// note.
func decodeScoreArticulation(data []byte) (ScoreArticulation, bool) {
	var articulation ScoreArticulation
	if !record.Decode(data, append(articulation.fields(), record.Copy(0, articulation.Raw[:]))...) || articulation.Code == 0 {
		return ScoreArticulation{}, false
	}
	name := articulationKind(articulation.Code)
	articulation.Kind, articulation.Flipped = name.kind, name.flipped
	return articulation, true
}

// fields is the layout of an articulation atom.
func (a *ScoreArticulation) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0, 0, 0, 0),
		record.Uint8(4, &a.Code),
		record.Equal(5, 0),
		record.Uint8(6, &a.Flags),
		record.Equal(7, 0x85, 0, 0, 0, 0, 0, 0, 0, 0),
	}
}

// Save writes a's code and flags into the note record it was decoded from.
// When Kind or Flipped was changed, the code is chosen to match them.
// The project's sequences are not updated; see [ProjectData.Refresh].
func (a *ScoreArticulation) Save() error {
	code, ok := symbolCode(a.Code, articulationName{a.Kind, a.Flipped}, articulationKind, articulationCodes)
	if !ok {
		return fmt.Errorf("logicx: no articulation code for %q", a.Kind)
	}
	a.Code = code
	if err := a.ref.save("articulation", a.fields()...); err != nil {
		return err
	}
	copy(a.Raw[:], a.ref.atom())
	return nil
}

// decodeScoreFermata decodes a 16-byte fermata record that trails a note.
func decodeScoreFermata(data []byte) (ScoreFermata, bool) {
	var fermata ScoreFermata
	if !record.Decode(data, append(fermata.fields(), record.Copy(0, fermata.Raw[:]))...) ||
		fermata.Code != 0 && fermata.Code != fermataInverted {
		return ScoreFermata{}, false
	}
	fermata.Inverted = fermata.Code == fermataInverted
	return fermata, true
}

// fermataInverted is the code of an inverted fermata; an upright one is zero.
const fermataInverted = 19

// fields is the layout of a fermata atom.
func (f *ScoreFermata) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0, 0, 0, 0),
		record.Uint8(4, &f.Code),
		record.Equal(5, 0, 0, 0x85, 0, 0, 0, 0, 0, 0, 0, 0),
	}
}

// Save writes f into the note record it was decoded from, with the code set
// from Inverted. The project's sequences are not updated; see
// [ProjectData.Refresh].
func (f *ScoreFermata) Save() error {
	f.Code = 0
	if f.Inverted {
		f.Code = fermataInverted
	}
	if err := f.ref.save("fermata", f.fields()...); err != nil {
		return err
	}
	copy(f.Raw[:], f.ref.atom())
	return nil
}

// symbolCode returns the code to store for a symbol named want: code itself
// when it already names want, otherwise the first of codes that does.
func symbolCode[N comparable](code uint8, want N, name func(uint8) N, codes []uint8) (uint8, bool) {
	if name(code) == want {
		return code, true
	}
	for _, c := range codes {
		if name(c) == want {
			return c, true
		}
	}
	return 0, false
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
	if !record.Decode(data, append(segment.fields(), record.Copy(0, segment.raw[:]))...) ||
		segment.code < 1 || segment.code > 3 {
		return scoreSlurSegment{}, false
	}
	return segment, true
}

// fields is the layout of a slur marker atom.
func (s *scoreSlurSegment) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0, 0, 0, 0, 0, 0, 0, 0x8c, 0, 0, 0, 0, 0, 0, 0),
		record.Uint8(15, &s.code),
	}
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
		count := min(startEnd-start, stopEnd-end, 255)
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
	if !record.Decode(data, append(ornament.fields(), record.Copy(0, ornament.Raw[:]))...) {
		return ScoreOrnament{}, false
	}
	ornament.Kind = ornamentKind(ornament.Code)
	return ornament, true
}

// ornamentKind names an ornament code.
func ornamentKind(code uint8) ScoreOrnamentKind {
	switch code {
	case 0:
		return ScoreOrnamentTurn
	case 1:
		return ScoreOrnamentInvertedTurnWithLine
	case 2:
		return ScoreOrnamentInvertedMordent
	case 3:
		return ScoreOrnamentMordent
	case 4:
		return ScoreOrnamentTrill
	case 7:
		return ScoreOrnamentTremolo
	case 19:
		return ScoreOrnamentInvertedTurn
	}
	return ScoreOrnamentUnknown
}

// ornamentCodes are the codes ornamentKind names.
var ornamentCodes = []uint8{0, 1, 2, 3, 4, 7, 19}

// fields is the layout of an ornament record.
func (o *ScoreOrnament) fields() []record.Field {
	return positionedScoreSymbol(0x42, &o.Position, &o.PositionFraction, &o.Code)
}

// Save writes o into the record it was decoded from, keeping the bytes this
// package does not decode, and moves the record when its position changed.
// When Kind was changed, the code is chosen to match it. The project's
// sequences are not updated; see [ProjectData.Refresh].
func (o *ScoreOrnament) Save() error {
	code, ok := symbolCode(o.Code, o.Kind, ornamentKind, ornamentCodes)
	if !ok {
		return fmt.Errorf("logicx: no ornament code for %q", o.Kind)
	}
	o.Code = code
	if err := o.ref.save("ornament", o.fields()...); err != nil {
		return err
	}
	copy(o.Raw[:], o.ref.event.Data)
	return nil
}

// Delete removes o's record from the project.
func (o *ScoreOrnament) Delete() error { return o.ref.delete("ornament") }

// Duplicate inserts a copy of o's record after it and returns the copy, to be
// changed and saved.
func (o *ScoreOrnament) Duplicate() (ScoreOrnament, error) {
	ref, err := o.ref.duplicate("ornament")
	copied := *o
	copied.ref = ref
	return copied, err
}

// decodeScoreArpeggio decodes a positioned 32-byte arpeggio record.
func decodeScoreArpeggio(data []byte) (ScoreArpeggio, bool) {
	var arpeggio ScoreArpeggio
	if !record.Decode(data, append(arpeggio.fields(), record.Copy(0, arpeggio.Raw[:]))...) || arpeggio.Code > 2 {
		return ScoreArpeggio{}, false
	}
	arpeggio.Direction = arpeggioDirection(arpeggio.Code)
	return arpeggio, true
}

// arpeggioDirection names an arpeggio code.
func arpeggioDirection(code uint8) ScoreArpeggioDirection {
	switch code {
	case 1:
		return ScoreArpeggioDirectionUp
	case 2:
		return ScoreArpeggioDirectionDown
	}
	return ScoreArpeggioDirectionNone
}

// fields is the layout of an arpeggio record.
func (a *ScoreArpeggio) fields() []record.Field {
	return positionedScoreSymbol(0x49, &a.Position, &a.PositionFraction, &a.Code)
}

// Save writes a into the record it was decoded from, keeping the bytes this
// package does not decode, and moves the record when its position changed.
// The code is set from Direction. The project's sequences are not updated;
// see [ProjectData.Refresh].
func (a *ScoreArpeggio) Save() error {
	code, ok := symbolCode(a.Code, a.Direction, arpeggioDirection, []uint8{0, 1, 2})
	if !ok {
		return fmt.Errorf("logicx: no arpeggio code for %q", a.Direction)
	}
	a.Code = code
	if err := a.ref.save("arpeggio", a.fields()...); err != nil {
		return err
	}
	copy(a.Raw[:], a.ref.event.Data)
	return nil
}

// Delete removes a's record from the project.
func (a *ScoreArpeggio) Delete() error { return a.ref.delete("arpeggio") }

// Duplicate inserts a copy of a's record after it and returns the copy, to be
// changed and saved.
func (a *ScoreArpeggio) Duplicate() (ScoreArpeggio, error) {
	ref, err := a.ref.duplicate("arpeggio")
	copied := *a
	copied.ref = ref
	return copied, err
}

// positionedScoreSymbol is the layout of the 32-byte record shared by the
// positioned score symbols, with symbol as the record's discriminator.
func positionedScoreSymbol(symbol uint8, position *uint32, fraction *uint16, code *uint8) []record.Field {
	return []record.Field{
		record.Equal(0, 0x70, 0),
		record.Uint16LE(2, fraction),
		record.Uint32LE(4, position),
		record.Equal(8, 0, 0, 0),
		record.Uint8(11, code),
		record.Equal(12, symbol, 0, 0, 1),
		record.Equal(23, 0x88),
	}
}

// lyricText is where a lyric's text begins: every atom after the third.
const lyricText = 48

// decodeLyric decodes a lyric record. A record too short to hold any text is
// not a lyric.
func decodeLyric(data []byte) (Lyric, bool) {
	var lyric Lyric
	if !record.Decode(data, lyric.fields()...) || len(data) < lyricText+atomSize {
		return Lyric{}, false
	}
	// Logic sometimes stores a trailing space, which Text drops.
	lyric.Text = strings.TrimSpace(lyricStoredText(data[lyricText:]))
	lyric.text = lyric.Text
	lyric.Raw = bytes.Clone(data)
	return lyric, lyric.Text != ""
}

// lyricStoredText reads the text atoms of a lyric record. Each atom holds two
// cells of text, each filled from its end backwards: seven characters before
// the atom's continuation byte, then eight.
//
// Only ASCII text has been seen; Raw keeps the bytes should other text prove
// to be encoded differently.
func lyricStoredText(data []byte) string {
	var text []byte
	for offset := 0; offset+8 <= len(data); offset += 8 {
		for i := offset + 7; i >= offset; i-- {
			if data[i] != 0 && data[i] != 0x88 {
				text = append(text, data[i])
			}
		}
	}
	return string(text)
}

// fields is the layout of a lyric record, apart from its text.
func (l *Lyric) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0x70, 0),
		record.Uint16LE(2, &l.PositionFraction),
		record.Uint32LE(4, &l.Position),
		record.Equal(8, 0, 0, 0),
		record.Uint8(11, &l.Verse),
		record.Equal(12, 0x3d, 0, 0, 1),
	}
}

// encodeLyricText lays text out in atoms as decodeLyric reads it.
func encodeLyricText(text string) ([]byte, error) {
	if text == "" || strings.TrimSpace(text) != text || !printable([]byte(text)) {
		return nil, fmt.Errorf("logicx: lyric %q is not trimmed, non-empty printable ASCII", text)
	}
	// Fifteen characters fit an atom, but Logic always leaves room for a
	// terminating zero.
	atoms := len(text)/15 + 1
	data := make([]byte, atoms*atomSize)
	k := 0
	for offset := 0; offset < len(data); offset += 8 {
		end := offset + 7
		if offset%atomSize == 0 {
			data[end] = 0x88
			end--
		}
		for i := end; i >= offset && k < len(text); i-- {
			data[i] = text[k]
			k++
		}
	}
	return data, nil
}

// Save writes l's position and verse into the record it was decoded from,
// keeping the bytes this package does not decode, and moves the record when
// its position changed. Changed text is rewritten, resizing the record to
// fit. The project's sequences are not updated; see [ProjectData.Refresh].
func (l *Lyric) Save() error {
	if err := l.ref.check("lyric"); err != nil {
		return err
	}
	if l.Text != l.text {
		text, err := encodeLyricText(l.Text)
		if err != nil {
			return err
		}
		l.ref.event.Data = append(slices.Clip(l.ref.event.Data[:lyricText]), text...)
		l.text = l.Text
	}
	if err := l.ref.save("lyric", l.fields()...); err != nil {
		return err
	}
	l.Raw = bytes.Clone(l.ref.event.Data)
	return nil
}

// Delete removes l's record from the project.
func (l *Lyric) Delete() error { return l.ref.delete("lyric") }

// Duplicate inserts a copy of l's record after it and returns the copy, to be
// changed and saved.
func (l *Lyric) Duplicate() (Lyric, error) {
	ref, err := l.ref.duplicate("lyric")
	copied := *l
	copied.Raw = bytes.Clone(l.Raw)
	copied.ref = ref
	return copied, err
}

// sequenceNameLength is where an MSeq payload stores its region name: a
// two-byte length, then the name in UTF-8. The fixed-size tail follows at the
// next even offset, after a zero byte when the name ends at an odd one. The
// tail is 278 or 279 bytes depending on the Logic version that wrote it, so
// its fields are addressed from the payload's end.
const sequenceNameLength = 16

// sequenceTail returns the offset of an MSeq payload's tail, after its name
// and alignment.
func sequenceTail(data []byte) (int, bool) {
	var name string
	if !record.Decode(data, record.String16(sequenceNameLength, &name)) {
		return 0, false
	}
	end := sequenceNameLength + 2 + len(name)
	end += end & 1
	return end, end <= len(data)
}

// renameSequence returns an MSeq payload with its region name replaced,
// keeping the tail at an even offset as Logic does.
func renameSequence(data []byte, name string) ([]byte, error) {
	tail, ok := sequenceTail(data)
	if !ok {
		return nil, errors.New("logicx: sequence descriptor has no name")
	}
	if len(name) > 0xffff {
		return nil, errors.New("logicx: region name too long")
	}
	out := slices.Clip(data[:sequenceNameLength])
	out = binary.LittleEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)
	if len(out)%2 == 1 {
		out = append(out, 0)
	}
	return append(out, data[tail:]...), nil
}

// sequenceName returns the region name of an MSeq payload.
func sequenceName(data []byte) string {
	var name string
	if !record.Decode(data, record.String16(sequenceNameLength, &name)) || strings.TrimSpace(name) == "" {
		return "MIDI Sequence"
	}
	return strings.TrimSpace(name)
}

// Save writes s's name and source length into the descriptor of its source
// sequence, and its position and loop length into the arrangement locator
// that places it. The name and length are shared by every region placing the
// same source. A sequence found without arrangement locators saves only its
// name and length. Notes and chords are saved on their own. The project's
// sequences are not updated; see [ProjectData.Refresh].
func (s *MIDISequence) Save() error {
	if s.descriptor == nil || len(s.descriptor.Data) < sequenceMetadataTail {
		return errors.New("logicx: sequence was not decoded from a project")
	}
	var link regionLink
	if s.link.event != nil {
		if err := s.link.check("region"); err != nil {
			return err
		}
		if s.Position < projectChordPositionBias {
			return fmt.Errorf("logicx: region position %d precedes the project start", s.Position)
		}
		if s.Position != s.position && s.shared {
			return fmt.Errorf("logicx: region %q places a source other regions place too, so it cannot move alone", s.Name)
		}
		if s.regionParams() != s.params && s.shared {
			return fmt.Errorf("logicx: region %q places a source other regions place too, so its parameters cannot change alone", s.Name)
		}
		link = regionLink{position: s.Position - projectChordPositionBias, duration: s.linkDuration, sequence: s.SequenceID}
		switch {
		case s.Looped && (s.Duration == 0 || s.Duration == noRegionLoop):
			return fmt.Errorf("logicx: loop length %d is not valid", s.Duration)
		case s.Looped:
			link.duration = s.Duration
		case link.duration != 0 && link.duration != noRegionLoop:
			link.duration = noRegionLoop
		}
	}

	data := s.descriptor.Data
	if s.Name != s.name {
		var err error
		if data, err = renameSequence(data, s.Name); err != nil {
			return fmt.Errorf("logicx: region %q: %w", s.Name, err)
		}
	}
	fields := []record.Field{record.Uint32LE(len(data)-sequenceMetadataTail, &s.SourceDuration)}
	params := s.regionParams()
	if !s.parameters && params != s.params {
		return fmt.Errorf("logicx: region %q has no parameters to change", s.Name)
	}
	if s.parameters {
		tail, _ := sequenceTail(data)
		fields = append(fields, params.descriptorFields(tail)...)
	}
	if s.link.event != nil && s.Position != s.position {
		// The source's events stay put; the shift that places them follows
		// the region.
		shift := int64(sequencePositionOffset(data)) + int64(s.Position) - int64(s.position)
		if shift < math.MinInt32 || shift > math.MaxInt32 {
			return fmt.Errorf("logicx: region %q moved too far", s.Name)
		}
		stored := uint32(int32(shift))
		fields = append(fields, record.Uint32LE(len(data)-sequenceOffsetTail, &stored))
	}
	data, err := record.Encode(data, fields...)
	if err != nil {
		return fmt.Errorf("logicx: region %q: %w", s.Name, err)
	}
	s.descriptor.Data, s.name, s.params = data, s.Name, params
	if s.link.event == nil {
		return nil
	}
	linkFields := link.fields()
	if s.parameters {
		linkFields = append(linkFields, params.linkFields()...)
	}
	if err := s.link.save("region", linkFields...); err != nil {
		return err
	}
	s.linkDuration, s.position = link.duration, s.Position
	return nil
}

// findMarkers decodes global markers and resolves their text, sorted by
// position.
func findMarkers(chunks []*Chunk) []Marker {
	texts := make(map[uint32]*Chunk)
	for _, chunk := range chunks {
		if chunk.Type == "TxSq" {
			texts[binary.LittleEndian.Uint32(chunk.Header[10:14])] = chunk
		}
	}

	var markers []Marker
	decode := markerDecoder(texts)
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		if event.Type != eventMarker {
			return
		}
		if marker, ok := decode(event.Data); ok {
			marker.ref = eventRef{chunk, event}
			markers = append(markers, marker)
		}
	})
	slices.SortFunc(markers, func(a, b Marker) int { return cmp.Compare(a.Position, b.Position) })
	return markers
}

// markerDecoder returns a decoder for marker records that resolves each
// marker's text through texts, rejecting markers whose text is missing.
func markerDecoder(texts map[uint32]*Chunk) func([]byte) (Marker, bool) {
	return func(data []byte) (Marker, bool) {
		var marker Marker
		if !record.Decode(data, append(marker.fields(), record.Copy(0, marker.Raw[:]))...) {
			return Marker{}, false
		}
		text, ok := texts[marker.TextID]
		if !ok {
			return Marker{}, false
		}
		marker.RTF = markerRTF(text.Data)
		marker.Name = plainRTF(marker.RTF)
		marker.text, marker.name = text, marker.Name
		return marker, true
	}
}

// fields is the layout of a marker record.
func (m *Marker) fields() []record.Field {
	return []record.Field{
		record.Equal(0, 0x12, 0, 0, 0),
		record.Uint32LE(4, &m.Position),
		record.Uint32LE(16, &m.TextID),
		record.Equal(20, 0, 0, 0, 0x88),
		record.Uint32LE(28, &m.Length),
	}
}

// Save writes m's position, length and text reference into the record it was
// decoded from, keeping the bytes this package does not decode, and moves the
// record when its position changed. A changed Name is written into the text
// chunk, in place of the old name within its RTF; every marker sharing the
// text is renamed. RTF is not written. ProjectData.Markers is not updated;
// see [ProjectData.Refresh].
func (m *Marker) Save() error {
	var text []byte
	if m.Name != m.name {
		if m.text == nil {
			return errors.New("logicx: marker has no text to rename")
		}
		var err error
		if text, err = renameMarkerText(m.text.Data, m.name, m.Name); err != nil {
			return err
		}
	}
	if err := m.ref.save("marker", m.fields()...); err != nil {
		return err
	}
	copy(m.Raw[:], m.ref.event.Data)
	if text != nil {
		m.text.Data, m.name = text, m.Name
		m.RTF = markerRTF(text)
	}
	return nil
}

// markerTextStart is where the RTF begins in a marker text chunk. The
// chunk's payload length is stored at byte 0 and again at byte 20, and the
// RTF's start at byte 16.
const markerTextStart = 98

// markerTextFields is the layout of a marker text chunk ahead of its RTF.
func markerTextFields(size, start, repeated *uint32) []record.Field {
	return []record.Field{record.Uint32LE(0, size), record.Uint32LE(16, start), record.Uint32LE(20, repeated)}
}

// rtfEscape escapes the characters RTF treats specially.
var rtfEscape = strings.NewReplacer(`\`, `\\`, `{`, `\{`, `}`, `\}`)

// renameMarkerText returns a marker text chunk's payload with the name old
// replaced by name within its RTF, refusing when the layout or the old name
// is not as expected or the result would not read back as name.
func renameMarkerText(data []byte, old, name string) ([]byte, error) {
	var size, start, repeated uint32
	if !record.Decode(data, markerTextFields(&size, &start, &repeated)...) ||
		int(size) != len(data) || repeated != size || start != markerTextStart || len(data) < markerTextStart ||
		!bytes.HasPrefix(data[start:], []byte(`{\rtf`)) {
		return nil, errors.New("logicx: marker text chunk layout not recognized")
	}
	if name == "" || strings.TrimSpace(name) != name || !printable([]byte(name)) {
		return nil, fmt.Errorf("logicx: marker name %q is not trimmed, non-empty printable ASCII", name)
	}
	escaped := []byte(rtfEscape.Replace(old))
	at := bytes.LastIndex(data[start:], escaped)
	if old == "" || at < 0 {
		return nil, fmt.Errorf("logicx: marker name %q not found in its text", old)
	}
	at += int(start)
	out := slices.Concat(data[:at], []byte(rtfEscape.Replace(name)), data[at+len(escaped):])
	size = uint32(len(out))
	out, err := record.Encode(out, markerTextFields(&size, &start, &size)...)
	if err != nil {
		return nil, err
	}
	if got := plainRTF(markerRTF(out)); got != name {
		return nil, fmt.Errorf("logicx: renamed marker text reads back as %q", got)
	}
	return out, nil
}

// Delete removes m's record from the project. The text chunk it refers to is
// kept.
func (m *Marker) Delete() error { return m.ref.delete("marker") }

// Duplicate inserts a copy of m's record after it and returns the copy, to be
// changed and saved. The copy refers to the same text.
func (m *Marker) Duplicate() (Marker, error) {
	ref, err := m.ref.duplicate("marker")
	copied := *m
	copied.ref = ref
	return copied, err
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
