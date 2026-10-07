// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

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
	// audioFileOverview is ceil(frames/128)+8 in every file seen, plausibly
	// the length of the waveform overview Logic draws from.
	audioFileOverview = 530
	audioFileTail     = 578
)

// fields is the layout of an audio file chunk. overview is written rather
// than decoded: it is derived from the frame count.
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
// bytes this package does not decode. ProjectData.AudioFiles is not updated.
func (f *AudioFile) Save() error {
	if f.chunk == nil {
		return errors.New("logicx: audio file was not decoded from a project")
	}
	// Logic reads the folder up to a NUL, so one must fit.
	if len(f.Dir) >= audioFileDirSize {
		return fmt.Errorf("logicx: audio file folder longer than %d bytes", audioFileDirSize-1)
	}
	overview := (f.Frames+127)/128 + 8
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
	Name   string
	Frames uint32
	chunk  *Chunk
}

const (
	audioRegionChunk      = "AuRg"
	audioRegionFrames     = 22
	audioRegionNameLength = 74
	// audioRegionTail is everything after the name: padding, the region's
	// UUID and trailing fields.
	audioRegionTail = 134
)

// fields is the layout of an audio region chunk.
func (r *AudioRegion) fields() []record.Field {
	return []record.Field{
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
