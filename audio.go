// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/loov/logicx/internal/record"
	"howett.net/plist"
)

// AudioFile is one file in the project audio bin, an "AuFl" chunk.
//
// The chunk opens with the file name as a length-prefixed UTF-16 string and
// every field after it sits at a fixed distance from the name's end; see
// [AudioFile.fields].
type AudioFile struct {
	// Name is the file name within Dir.
	Name string
	// Dir is the folder holding the file. Logic writes either "Audio Files",
	// relative to the bundle's Media folder, or a "~/"-prefixed path.
	Dir        string
	Size       uint32
	Format     string // "WAVE"
	Frames     uint32
	SampleRate uint32
	Channels   uint16
	BitDepth   uint16
	chunk      *Chunk
}

const (
	audioFileChunk      = "AuFl"
	audioFileNameLength = 8
	// Offsets past the end of the name.
	audioFileMarker     = 0
	audioFileDir        = 138
	audioFileDirSize    = 256
	audioFileSize       = 406
	audioFileFormat     = 456
	audioFileFrames     = 468
	audioFileSampleRate = 476
	audioFileChannels   = 480
	audioFileBitDepth   = 482
	// audioFileOverview is ceil(frames/256)*2+8 in all but one of the 248
	// files seen, plausibly the length of the waveform overview Logic draws
	// from.
	audioFileOverview = 530
	audioFileTail     = 578
)

// fields is the layout of an audio file chunk. overview is held apart from f:
// Save derives it from the frame count.
func (f *AudioFile) fields(overview *uint32) []record.Field {
	return []record.Field{
		record.UTF16(audioFileNameLength, &f.Name,
			record.Len(audioFileTail),
			record.Equal(audioFileMarker, []byte("LFUA")...),
			record.CString(audioFileDir, audioFileDirSize, &f.Dir),
			record.Uint32LE(audioFileSize, &f.Size),
			record.Code4(audioFileFormat, &f.Format),
			record.Uint32LE(audioFileFrames, &f.Frames),
			record.Uint32LE(audioFileSampleRate, &f.SampleRate),
			record.Uint16LE(audioFileChannels, &f.Channels),
			record.Uint16LE(audioFileBitDepth, &f.BitDepth),
			record.Uint32LE(audioFileOverview, overview),
		),
	}
}

// findAudioFiles decodes the audio bin in file order.
func findAudioFiles(chunks []*Chunk) []AudioFile {
	var files []AudioFile
	for _, chunk := range chunks {
		var file AudioFile
		var overview uint32
		if chunk.Type != audioFileChunk || !record.Decode(chunk.Data, file.fields(&overview)...) {
			continue
		}
		file.chunk = chunk
		files = append(files, file)
	}
	return files
}

// Save writes f's fields into the chunk it was decoded from, keeping the
// bytes this package does not decode. The overview length is recomputed only
// when the frame count changed, since one file seen stores a value the
// formula does not explain. ProjectData.AudioFiles is not updated.
func (f *AudioFile) Save() error {
	if f.chunk == nil {
		return errors.New("logicx: audio file was not decoded from a project")
	}
	// Logic reads the folder up to a NUL, so one must fit.
	if len(f.Dir) >= audioFileDirSize {
		return fmt.Errorf("logicx: audio file folder longer than %d bytes", audioFileDirSize-1)
	}
	var stored AudioFile
	var overview uint32
	if !record.Decode(f.chunk.Data, stored.fields(&overview)...) {
		return fmt.Errorf("logicx: audio file %q no longer decodes", f.Name)
	}
	if f.Frames != stored.Frames {
		overview = (f.Frames+255)/256*2 + 8
	}
	data, err := record.Encode(f.chunk.Data, f.fields(&overview)...)
	if err != nil {
		return fmt.Errorf("logicx: audio file %q: %w", f.Name, err)
	}
	f.chunk.Data = data
	return nil
}

// AudioRegion is one region of an audio file, an "AuRg" chunk. Where it sits
// in the arrangement is an event in a track's sequence, which carries neither
// its length nor its name.
//
// Apple Loops family archives elsewhere in the project carry the region's
// name too; a renamed region needs [ProjectData.RenameLoops] as well.
type AudioRegion struct {
	Name string
	// Offset is where the region starts in its audio file, and Frames how
	// long it is, in sample frames.
	Offset uint32
	Frames uint32
	chunk  *Chunk
}

const (
	audioRegionChunk      = "AuRg"
	audioRegionOffset     = 6
	audioRegionFrames     = 22
	audioRegionNameLength = 74
	// audioRegionTail is everything after the name: padding, the region's
	// UUID and trailing fields.
	audioRegionTail = 134
)

// fields is the layout of an audio region chunk.
func (r *AudioRegion) fields() []record.Field {
	return []record.Field{
		record.Uint32LE(audioRegionOffset, &r.Offset),
		record.Uint32LE(audioRegionFrames, &r.Frames),
		record.String16(audioRegionNameLength, &r.Name, record.Len(audioRegionTail)),
	}
}

// findAudioRegions decodes the audio regions in file order.
func findAudioRegions(chunks []*Chunk) []AudioRegion {
	var regions []AudioRegion
	for _, chunk := range chunks {
		var region AudioRegion
		if chunk.Type != audioRegionChunk || !record.Decode(chunk.Data, region.fields()...) {
			continue
		}
		region.chunk = chunk
		regions = append(regions, region)
	}
	return regions
}

// Save writes r's fields into the chunk it was decoded from, keeping the bytes
// this package does not decode. ProjectData.AudioRegions is not updated.
func (r *AudioRegion) Save() error {
	if r.chunk == nil {
		return errors.New("logicx: audio region was not decoded from a project")
	}
	data, err := record.Encode(r.chunk.Data, r.fields()...)
	if err != nil {
		return fmt.Errorf("logicx: audio region %q: %w", r.Name, err)
	}
	r.chunk.Data = data
	return nil
}

// AudioPlacement is an audio region placed on a track: an event in the
// arrange sequence naming the region by its index.
type AudioPlacement struct {
	// Region is the placed region's index in ProjectData.AudioRegions.
	Region int
	// Position and Fraction are on the same timeline as [MIDINote.Position].
	Position uint32
	Fraction uint16
	// Track is the name of the track the region is on, or empty when it is
	// not known. Save does not move a region between tracks.
	Track string
	// Gain is the region's gain in dB, and FadeIn and FadeOut its fades as
	// Logic's region inspector shows them.
	Gain        int8
	FadeIn      uint32
	FadeOut     uint32
	ref         eventRef
	trackObject uint32
}

const (
	eventAudioRegion      = 0x24
	audioPlacementRegion  = 13
	audioPlacementGain    = 52
	audioPlacementFadeOut = 72
	audioPlacementFadeIn  = 76
	audioPlacementMinimum = 80
)

// fields is the layout of an audio placement; position is the stored form of
// Position and gain of Gain.
func (a *AudioPlacement) fields(position *uint32, gain *uint8) []record.Field {
	return []record.Field{
		record.Equal(0, eventAudioRegion),
		record.Uint16LE(2, &a.Fraction),
		record.Uint32LE(4, position),
		record.Uint8(audioPlacementGain, gain),
		record.Equal(55, 0x8a),
		record.Equal(71, 0x89),
		record.Uint32LE(audioPlacementFadeOut, &a.FadeOut),
		record.Uint32LE(audioPlacementFadeIn, &a.FadeIn),
	}
}

// findAudioPlacements decodes the placements of regions, in file order. A
// placement naming no region is left out.
func findAudioPlacements(chunks []*Chunk, regions []AudioRegion) []AudioPlacement {
	byIndex := make(map[uint16]int, len(regions))
	for i, r := range regions {
		byIndex[binary.LittleEndian.Uint16(r.chunk.Header[14:])] = i
	}
	var placements []AudioPlacement
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		d := event.Data
		if event.Type != eventAudioRegion || len(d) < audioPlacementMinimum {
			return
		}
		region, ok := byIndex[binary.LittleEndian.Uint16(d[audioPlacementRegion:])]
		var a AudioPlacement
		var position uint32
		var gain uint8
		if !ok || !record.Decode(d, a.fields(&position, &gain)...) || position > math.MaxUint32-projectChordPositionBias {
			return
		}
		a.Region, a.Position, a.Gain = region, position+projectChordPositionBias, int8(gain)
		a.trackObject = binary.LittleEndian.Uint32(d[linkTrack:])
		a.ref = eventRef{chunk, event}
		placements = append(placements, a)
	})
	return placements
}

// Save writes a's position, gain and fades into its event, keeping the
// sequence in time order. ProjectData.AudioPlacements is not updated; see
// [ProjectData.Refresh].
func (a *AudioPlacement) Save() error {
	if a.Position < projectChordPositionBias {
		return fmt.Errorf("logicx: audio region position %d precedes the project start", a.Position)
	}
	position, gain := a.Position-projectChordPositionBias, uint8(a.Gain)
	return a.ref.save("audio region placement", a.fields(&position, &gain)...)
}

// RenameLoops replaces the loop name old with name in the Apple Loops family
// archives, which carry the name of the audio region they were made from.
func (p *ProjectData) RenameLoops(old, name string) error {
	if old == name {
		return nil
	}
	for i, c := range p.Chunks {
		if c.Type != "SngO" && c.Type != "GenM" {
			continue
		}
		data, err := renameLoop(c.Data, old, name)
		if err != nil {
			return fmt.Errorf("logicx: rename loop in %s chunk %d: %w", c.Type, i, err)
		}
		c.Data = data
	}
	return nil
}

// renameLoop rewrites the keyed archive near the end of a chunk — {Shared:
// {LoopFamily: {LoopName, LoopId}}} — replacing the string old with name.
// The archive is nested in length-prefixed blocks. The innermost length is
// the largest aligned word before the archive that still fits after it, and
// the enclosing ones are the words between that and the chunk's length. That
// holds for every chunk seen but is not decoded structure.
func renameLoop(d []byte, old, name string) ([]byte, error) {
	at := bytes.Index(d, []byte("bplist00"))
	if at < 0 {
		return d, nil
	}
	oldLen := uint32(0)
	for k := 0; k+4 <= at; k += 4 {
		if v := binary.LittleEndian.Uint32(d[k:]); v <= uint32(len(d)-at) && v > oldLen {
			oldLen = v
		}
	}
	var archive map[string]any
	if _, err := plist.Unmarshal(d[at:at+int(oldLen)], &archive); err != nil {
		return nil, err
	}
	objects, _ := archive["$objects"].([]any)
	found := false
	for k, o := range objects {
		if s, ok := o.(string); ok && s == old {
			objects[k] = name
			found = true
		}
	}
	if !found {
		return d, nil
	}
	encoded, err := plist.Marshal(archive, plist.BinaryFormat)
	if err != nil {
		return nil, err
	}
	delta := uint32(len(encoded)) - oldLen
	out := append(bytes.Clone(d[:at]), encoded...)
	out = append(out, d[at+int(oldLen):]...)
	for k := 0; k+4 <= at; k += 4 {
		v := binary.LittleEndian.Uint32(out[k:])
		if v >= oldLen && v <= uint32(len(d)) {
			binary.LittleEndian.PutUint32(out[k:], v+delta)
		}
	}
	return out, nil
}

// EnvironmentObject is one object of the Logic environment, an "Envi" chunk:
// a track, channel strip, click or input. Only its name is decoded.
type EnvironmentObject struct {
	Name  string
	chunk *Chunk
}

const (
	environmentChunk      = "Envi"
	environmentNameLength = 158
)

// fields is the layout of an environment chunk.
func (o *EnvironmentObject) fields() []record.Field {
	return []record.Field{record.String16(environmentNameLength, &o.Name)}
}

// findEnvironment decodes the environment objects in file order.
func findEnvironment(chunks []*Chunk) []EnvironmentObject {
	var objects []EnvironmentObject
	for _, chunk := range chunks {
		var object EnvironmentObject
		if chunk.Type != environmentChunk || !record.Decode(chunk.Data, object.fields()...) {
			continue
		}
		object.chunk = chunk
		objects = append(objects, object)
	}
	return objects
}

// Save writes o's fields into the chunk it was decoded from, keeping the bytes
// this package does not decode. ProjectData.Environment is not updated.
func (o *EnvironmentObject) Save() error {
	if o.chunk == nil {
		return errors.New("logicx: environment object was not decoded from a project")
	}
	data, err := record.Encode(o.chunk.Data, o.fields()...)
	if err != nil {
		return fmt.Errorf("logicx: environment object %q: %w", o.Name, err)
	}
	o.chunk.Data = data
	return nil
}
