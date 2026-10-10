// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"unicode/utf16"

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
	// audioFileRegions is how many regions of the file the project has,
	// numbered from zero in their chunk headers.
	audioFileRegions = 508
	audioFileTail    = 578
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
	// Color is the region's color code, as [Track.Color].
	Color uint8
	chunk *Chunk
}

const (
	audioRegionChunk      = "AuRg"
	audioRegionColor      = 3
	audioRegionOffset     = 6
	audioRegionFrames     = 22
	audioRegionNameLength = 74
	// audioRegionTail is everything after the name and the zero byte that
	// pads an odd-length name: padding, the region's UUID and trailing fields.
	audioRegionTail = 133
	// audioRegionUUID is where the region's UUID starts in the tail.
	audioRegionUUID = 86
)

// fields is the layout of an audio region chunk.
func (r *AudioRegion) fields() []record.Field {
	return []record.Field{
		record.Uint8(audioRegionColor, &r.Color),
		record.Uint32LE(audioRegionOffset, &r.Offset),
		record.Uint32LE(audioRegionFrames, &r.Frames),
		record.String16(audioRegionNameLength, &r.Name),
	}
}

// findAudioRegions decodes the audio regions in file order.
func findAudioRegions(chunks []*Chunk) []AudioRegion {
	var regions []AudioRegion
	for _, chunk := range chunks {
		var region AudioRegion
		if chunk.Type != audioRegionChunk || !record.Decode(chunk.Data, region.fields()...) || !audioRegionPadded(chunk.Data, region.Name) {
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
	old := r.chunk.Data
	odd := int(binary.LittleEndian.Uint16(old[audioRegionNameLength:])) % 2
	data, err := record.Encode(old, r.fields()...)
	if err != nil {
		return fmt.Errorf("logicx: audio region %q: %w", r.Name, err)
	}
	// The encoded name is followed by the old name's pad byte, if any.
	end := audioRegionNameLength + 2 + len(r.Name)
	switch {
	case odd == 1 && len(r.Name)%2 == 0:
		data = slices.Delete(data, end, end+1)
	case odd == 0 && len(r.Name)%2 == 1:
		data = slices.Insert(data, end, 0)
	}
	r.chunk.Data = data
	return nil
}

// audioRegionFile and audioRegionNumber are where an audio region's chunk
// header holds its file, as the file's chunk header does, and its number
// among that file's regions.
const (
	audioRegionFile   = 10
	audioRegionNumber = 14
)

// DuplicateAudioRegion inserts a copy of r, a region of p, after the last
// region of its file and returns the copy, to be changed and saved. The copy
// has the next number in its file and a new UUID; Logic gives a split region
// these and the name "name.1". p.AudioRegions is not updated; see
// [ProjectData.Refresh].
func (p *ProjectData) DuplicateAudioRegion(r *AudioRegion) (AudioRegion, error) {
	at := slices.Index(p.Chunks, r.chunk)
	if r.chunk == nil || at < 0 {
		return AudioRegion{}, errors.New("logicx: audio region is not in the project")
	}
	count, err := p.audioRegionCount(r)
	if err != nil {
		return AudioRegion{}, err
	}
	file := r.chunk.Header[audioRegionFile : audioRegionFile+4]
	for i, c := range p.Chunks {
		if c.Type == audioRegionChunk && bytes.Equal(c.Header[audioRegionFile:audioRegionFile+4], file) {
			at = i
		}
	}
	number := binary.LittleEndian.Uint32(count)
	chunk := &Chunk{Type: r.chunk.Type, Header: r.chunk.Header, Data: bytes.Clone(r.chunk.Data)}
	binary.LittleEndian.PutUint32(chunk.Header[audioRegionNumber:], number)
	uuid := chunk.Data[len(chunk.Data)-audioRegionTail+audioRegionUUID:][:16]
	rand.Read(uuid)
	uuid[6], uuid[8] = uuid[6]&0x0f|0x40, uuid[8]&0x3f|0x80
	binary.LittleEndian.PutUint32(count, number+1)
	p.Chunks = slices.Insert(p.Chunks, at+1, chunk)
	copied := *r
	copied.chunk = chunk
	return copied, nil
}

// DeleteAudioRegion removes r, a region of p, and its placements, as Logic's
// project audio browser does. The file's later regions are numbered down to
// close the gap, and their placements with them. p's values are not updated;
// see [ProjectData.Refresh].
func (p *ProjectData) DeleteAudioRegion(r *AudioRegion) error {
	at := slices.Index(p.Chunks, r.chunk)
	if r.chunk == nil || at < 0 {
		return errors.New("logicx: audio region is not in the project")
	}
	count, err := p.audioRegionCount(r)
	if err != nil {
		return err
	}
	file := binary.LittleEndian.Uint32(r.chunk.Header[audioRegionFile:])
	number := binary.LittleEndian.Uint32(r.chunk.Header[audioRegionNumber:])
	p.Chunks = slices.Delete(p.Chunks, at, at+1)
	for _, c := range p.Chunks {
		if c.Type == audioRegionChunk && binary.LittleEndian.Uint32(c.Header[audioRegionFile:]) == file {
			if n := binary.LittleEndian.Uint32(c.Header[audioRegionNumber:]); n > number {
				binary.LittleEndian.PutUint32(c.Header[audioRegionNumber:], n-1)
			}
		}
		c.Events = slices.DeleteFunc(c.Events, func(e *Event) bool {
			d := e.Data
			if e.Type != eventAudioRegion || len(d) < audioPlacementMinimum || binary.LittleEndian.Uint32(d[audioPlacementFile:]) != file {
				return false
			}
			n := binary.LittleEndian.Uint32(d[audioPlacementRegion:])
			if n > number {
				binary.LittleEndian.PutUint32(d[audioPlacementRegion:], n-1)
			}
			return n == number
		})
	}
	binary.LittleEndian.PutUint32(count, binary.LittleEndian.Uint32(count)-1)
	return nil
}

// audioRegionCount returns the bytes of the count of regions in r's file.
func (p *ProjectData) audioRegionCount(r *AudioRegion) ([]byte, error) {
	file := r.chunk.Header[audioRegionFile : audioRegionFile+4]
	for _, c := range p.Chunks {
		if c.Type != audioFileChunk || !bytes.Equal(c.Header[audioRegionFile:audioRegionFile+4], file) {
			continue
		}
		var f AudioFile
		var overview uint32
		if !record.Decode(c.Data, f.fields(&overview)...) {
			return nil, fmt.Errorf("logicx: audio region %q: its file's chunk does not decode", r.Name)
		}
		end := audioFileNameLength + 2 + 2*len(utf16.Encode([]rune(f.Name)))
		return c.Data[end+audioFileRegions : end+audioFileRegions+4], nil
	}
	return nil, fmt.Errorf("logicx: audio region %q has no file", r.Name)
}

// audioRegionPadded reports whether data, an audio region chunk named name,
// has a zero pad byte after an odd-length name and the tail after that.
func audioRegionPadded(data []byte, name string) bool {
	end := audioRegionNameLength + 2 + len(name)
	if len(name)%2 == 1 {
		if end >= len(data) || data[end] != 0 {
			return false
		}
		end++
	}
	return len(data)-end == audioRegionTail
}

// AudioPlacement is an audio region placed on a track: an event in the
// arrange sequence naming the region by its audio file and its number within
// that file.
type AudioPlacement struct {
	// Region is the placed region's index in ProjectData.AudioRegions. Save
	// can place another region of the project as it was decoded.
	Region int
	// Position and Fraction are on the same timeline as [MIDINote.Position].
	Position uint32
	Fraction uint16
	// Track is the name of the track the region is on, or empty when it is
	// not known. Save moves the region to another track that has an arrange
	// track.
	Track string
	Mute  bool
	// Gain is the region's gain in dB, and FadeIn and FadeOut its fades in
	// milliseconds.
	Gain    int8
	FadeIn  uint32
	FadeOut uint32
	// Loop is how long the looped region plays from its start, in ticks, or
	// zero when it is not looped.
	Loop uint32
	// unlooped is how the event marks an unlooped region: Logic writes
	// noRegionLoop, and some projects zero.
	unlooped    uint32
	ref         eventRef
	regions     []audioRegionKey
	trackObject uint32
	track       string
	arrange     map[string]arrangeTarget
}

const (
	eventAudioRegion = 0x24
	// audioPlacementRegion and audioPlacementFile hold the region's
	// AuRg header fields [14:16] and [10:14].
	audioPlacementRegion = 40
	audioPlacementFile   = 44
	audioPlacementLoop   = 28
	// audioPlacementLooped holds the bits Logic sets on a looped region.
	audioPlacementLooped     = 13
	audioPlacementLoopedBits = 0x12
	audioPlacementGain       = 52
	audioPlacementFadeOut    = 72
	audioPlacementFadeIn     = 76
	audioPlacementMinimum    = 80
)

// fields is the layout of an audio placement; position is the stored form of
// Position and gain of Gain.
func (a *AudioPlacement) fields(position *uint32, gain *uint8, loop *uint32, key *audioRegionKey) []record.Field {
	return []record.Field{
		record.Equal(0, eventAudioRegion),
		record.Uint16LE(2, &a.Fraction),
		record.Uint32LE(4, position),
		record.Bit(linkMute, 0x01, &a.Mute),
		record.Uint32LE(audioPlacementLoop, loop),
		record.Uint32LE(audioPlacementRegion, &key.region),
		record.Uint32LE(audioPlacementFile, &key.file),
		record.Uint8(audioPlacementGain, gain),
		record.Equal(55, 0x8a),
		record.Equal(71, 0x89),
		record.Uint32LE(audioPlacementFadeOut, &a.FadeOut),
		record.Uint32LE(audioPlacementFadeIn, &a.FadeIn),
	}
}

// audioRegionKey is how a placement names its region.
type audioRegionKey struct {
	file   uint32
	region uint32
}

// findAudioPlacements decodes the placements of regions, in file order. A
// placement naming no region is left out.
func findAudioPlacements(chunks []*Chunk, regions []AudioRegion) []AudioPlacement {
	keys := make([]audioRegionKey, len(regions))
	byKey := make(map[audioRegionKey]int, len(regions))
	for i, r := range regions {
		keys[i] = audioRegionKey{binary.LittleEndian.Uint32(r.chunk.Header[audioRegionFile:]), binary.LittleEndian.Uint32(r.chunk.Header[audioRegionNumber:])}
		byKey[keys[i]] = i
	}
	var placements []AudioPlacement
	sequenceEvents(chunks, func(chunk *Chunk, event *Event) {
		d := event.Data
		if event.Type != eventAudioRegion || len(d) < audioPlacementMinimum {
			return
		}
		var a AudioPlacement
		var position, loop uint32
		var gain uint8
		var key audioRegionKey
		if !record.Decode(d, a.fields(&position, &gain, &loop, &key)...) || position > math.MaxUint32-projectChordPositionBias {
			return
		}
		region, ok := byKey[key]
		if !ok {
			return
		}
		a.unlooped = noRegionLoop
		if loop == 0 || loop == noRegionLoop {
			a.unlooped = loop
		} else {
			a.Loop = loop
		}
		a.Region, a.Position, a.Gain, a.regions = region, position+projectChordPositionBias, int8(gain), keys
		a.trackObject = binary.LittleEndian.Uint32(d[linkTrack:])
		a.ref = eventRef{chunk, event}
		placements = append(placements, a)
	})
	return placements
}

// Save writes a's region, position, track, mute, gain, fades and loop into
// its event, keeping the sequence in time order. ProjectData.AudioPlacements
// is not updated; see [ProjectData.Refresh].
func (a *AudioPlacement) Save() error {
	if a.Position < projectChordPositionBias {
		return fmt.Errorf("logicx: audio region position %d precedes the project start", a.Position)
	}
	if a.Region < 0 || a.Region >= len(a.regions) {
		return fmt.Errorf("logicx: audio region %d is not in the project", a.Region)
	}
	if a.Loop >= noRegionLoop {
		return fmt.Errorf("logicx: loop length %d is not valid", a.Loop)
	}
	position, gain, loop, key := a.Position-projectChordPositionBias, uint8(a.Gain), a.Loop, a.regions[a.Region]
	looped := loop != 0
	if !looped {
		loop = a.unlooped
	}
	move, target, err := moveFields("audio region", a.track, a.Track, a.arrange)
	if err != nil {
		return err
	}
	fields := append(a.fields(&position, &gain, &loop, &key), record.Bit(audioPlacementLooped, audioPlacementLoopedBits, &looped))
	if err := a.ref.save("audio region placement", append(fields, move...)...); err != nil {
		return err
	}
	if target != nil {
		a.trackObject, a.track = target.object, a.Track
	}
	return nil
}

// Delete removes a's event, taking the region out of the arrangement; the
// region stays in the project.
func (a *AudioPlacement) Delete() error { return a.ref.delete("audio region placement") }

// Duplicate inserts a copy of a's event after it and returns the copy, to be
// changed and saved.
func (a *AudioPlacement) Duplicate() (AudioPlacement, error) {
	ref, err := a.ref.duplicate("audio region placement")
	copied := *a
	copied.ref = ref
	return copied, err
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
