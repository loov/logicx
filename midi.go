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
	ScoreArticulationUnknown       ScoreArticulationKind = ""
	ScoreArticulationStaccato      ScoreArticulationKind = "staccato"
	ScoreArticulationTenuto        ScoreArticulationKind = "tenuto"
	ScoreArticulationAccent        ScoreArticulationKind = "accent"
	ScoreArticulationMarcato       ScoreArticulationKind = "marcato"
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

// ScoreOrnamentKind identifies an ornament attached to a note.
type ScoreOrnamentKind string

const (
	ScoreOrnamentUnknown              ScoreOrnamentKind = ""
	ScoreOrnamentTurn                 ScoreOrnamentKind = "turn"
	ScoreOrnamentInvertedTurn         ScoreOrnamentKind = "inverted-turn"
	ScoreOrnamentInvertedTurnWithLine ScoreOrnamentKind = "inverted-turn-with-line"
	ScoreOrnamentMordent              ScoreOrnamentKind = "mordent"
	ScoreOrnamentInvertedMordent      ScoreOrnamentKind = "inverted-mordent"
	ScoreOrnamentTrill                ScoreOrnamentKind = "trill"
	ScoreOrnamentTremolo              ScoreOrnamentKind = "tremolo"
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
	ScoreArpeggioDirectionNone ScoreArpeggioDirection = ""
	ScoreArpeggioDirectionUp   ScoreArpeggioDirection = "up"
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

type sequenceSource struct {
	id             chordSequenceID
	name           string
	chunkOffset    int
	duration       uint32
	positionOffset int32
	notes          []MIDINote
	chords         []Chord
}

func findMIDISequences(chunks []Chunk) []MIDISequence {
	descriptors := make(map[chordSequenceID]Chunk)
	events := make(map[chordSequenceID]Chunk)
	for _, chunk := range chunks {
		id := chordSequenceID{
			group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
			sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
		}
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
		id := chordSequenceID{
			group:    binary.LittleEndian.Uint32(chunk.Header[6:10]),
			sequence: binary.LittleEndian.Uint32(chunk.Header[10:14]),
		}
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

	var sequences []MIDISequence
	for id, event := range events {
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
	// MSeq names are variable-length, so these fields are addressed from the
	// stable end of the payload.
	sequenceMetadataTail = 219
	noRegionLoop         = 0x3fffffff
)

func sequenceDuration(data []byte) uint32 {
	if len(data) < sequenceMetadataTail {
		return 0
	}
	return binary.LittleEndian.Uint32(data[len(data)-sequenceMetadataTail:])
}

func sequencePositionOffset(data []byte) int32 {
	if len(data) < 55 {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(data[len(data)-55:]))
}

type regionLink struct {
	position uint32
	duration uint32
	sequence uint32
}

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

func findMIDINotes(data []byte) []MIDINote {
	var notes []MIDINote
	var lyrics []Lyric
	var ornaments []ScoreOrnament
	var arpeggios []ScoreArpeggio
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
		for articulationOffset := offset + 32; articulationOffset+16 <= len(data); articulationOffset += 16 {
			if isScoreEventStart(data[articulationOffset:]) {
				break
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
	}
	return notes
}

func scorePositionAtOrBefore(position uint32, fraction uint16, note MIDINote) bool {
	return position < note.Position || position == note.Position && fraction <= note.PositionFraction
}

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

func nextScoreEvent(data []byte) int {
	for offset := 16; offset+16 <= len(data); offset += 16 {
		if isScoreEventStart(data[offset:]) {
			return offset
		}
	}
	return 0
}

func isScoreEventStart(data []byte) bool {
	return len(data) >= 16 && (data[0] == 0x90 || data[0] == 0xb0 ||
		data[0] == 0x70 && data[1] == 0 && data[12] >= 0x3c)
}

func sequenceName(data []byte) string {
	runs := printableRun.FindAll(data, -1)
	if len(runs) == 0 {
		return "MIDI Sequence"
	}
	return strings.TrimSpace(string(runs[len(runs)-1]))
}

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
	return markers
}

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

func markerRTF(data []byte) string {
	start := bytes.Index(data, []byte(`{\rtf`))
	if start < 0 {
		return ""
	}
	return strings.TrimRight(string(data[start:]), "\x00")
}

var rtfControl = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?`)

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
