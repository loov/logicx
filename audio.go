// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"

	"howett.net/plist"
)

// AudioFile is one file in the project audio bin, an "AuFl" chunk.
//
// The chunk opens with the file name as a length-prefixed UTF-16 string and
// every field after it sits at a fixed distance from the name's end.
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
	chunk      int
}

const (
	audioFileChunk      = "AuFl"
	audioFileNameLength = 8
	audioFileName       = 10
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

// audioFileMarker4 follows the name in every audio file chunk.
var audioFileMarker4 = []byte("LFUA")

// findAudioFiles decodes the audio bin in file order.
func findAudioFiles(chunks []Chunk) []AudioFile {
	var files []AudioFile
	for i, chunk := range chunks {
		if chunk.Type != audioFileChunk {
			continue
		}
		file, ok := decodeAudioFile(chunk.Data)
		if !ok {
			continue
		}
		file.chunk = i
		files = append(files, file)
	}
	return files
}

func decodeAudioFile(d []byte) (AudioFile, bool) {
	if len(d) < audioFileName {
		return AudioFile{}, false
	}
	n := int(binary.LittleEndian.Uint16(d[audioFileNameLength:]))
	end := audioFileName + 2*n
	if len(d) != end+audioFileTail || !bytes.Equal(d[end+audioFileMarker:end+audioFileMarker+4], audioFileMarker4) {
		return AudioFile{}, false
	}
	name := make([]uint16, n)
	for i := range name {
		name[i] = binary.LittleEndian.Uint16(d[audioFileName+2*i:])
	}
	t := d[end:]
	dir, _, _ := bytes.Cut(t[audioFileDir:audioFileDir+audioFileDirSize], []byte{0})
	return AudioFile{
		Name:       string(utf16.Decode(name)),
		Dir:        string(dir),
		Size:       binary.LittleEndian.Uint32(t[audioFileSize:]),
		Format:     reverse4(t[audioFileFormat:]),
		Frames:     binary.LittleEndian.Uint32(t[audioFileFrames:]),
		SampleRate: binary.LittleEndian.Uint32(t[audioFileSampleRate:]),
		Channels:   binary.LittleEndian.Uint16(t[audioFileChannels:]),
		BitDepth:   binary.LittleEndian.Uint16(t[audioFileBitDepth:]),
	}, true
}

// SetAudioFile rewrites the i-th audio file's chunk with f's fields, keeping
// the bytes this package does not decode.
func (p *ProjectData) SetAudioFile(i int, f AudioFile) error {
	if i < 0 || i >= len(p.AudioFiles) {
		return fmt.Errorf("logicx: no audio file %d", i)
	}
	if len(f.Dir) >= audioFileDirSize {
		return fmt.Errorf("logicx: audio file folder longer than %d bytes", audioFileDirSize-1)
	}
	if len(f.Format) != 4 {
		return fmt.Errorf("logicx: audio file format %q is not four characters", f.Format)
	}
	chunk := &p.Chunks[p.AudioFiles[i].chunk]
	old := chunk.Data
	oldEnd := audioFileName + 2*int(binary.LittleEndian.Uint16(old[audioFileNameLength:]))

	name := utf16.Encode([]rune(f.Name))
	if len(name) > 0xffff {
		return errors.New("logicx: audio file name too long")
	}
	d := make([]byte, 0, audioFileName+2*len(name)+audioFileTail)
	d = append(d, old[:audioFileNameLength]...)
	d = binary.LittleEndian.AppendUint16(d, uint16(len(name)))
	for _, c := range name {
		d = binary.LittleEndian.AppendUint16(d, c)
	}
	d = append(d, old[oldEnd:]...)

	t := d[audioFileName+2*len(name):]
	clear(t[audioFileDir : audioFileDir+audioFileDirSize])
	copy(t[audioFileDir:], f.Dir)
	binary.LittleEndian.PutUint32(t[audioFileSize:], f.Size)
	copy(t[audioFileFormat:], []byte{f.Format[3], f.Format[2], f.Format[1], f.Format[0]})
	binary.LittleEndian.PutUint32(t[audioFileFrames:], f.Frames)
	binary.LittleEndian.PutUint32(t[audioFileSampleRate:], f.SampleRate)
	binary.LittleEndian.PutUint16(t[audioFileChannels:], f.Channels)
	binary.LittleEndian.PutUint16(t[audioFileBitDepth:], f.BitDepth)
	binary.LittleEndian.PutUint32(t[audioFileOverview:], (f.Frames+127)/128+8)

	chunk.Data = d
	f.chunk = p.AudioFiles[i].chunk
	p.AudioFiles[i] = f
	return nil
}

// AudioRegion is one region of an audio file, an "AuRg" chunk. Where it sits
// in the arrangement is an event in a track's sequence, which carries neither
// its length nor its name.
type AudioRegion struct {
	Name   string
	Frames uint32
	chunk  int
}

const (
	audioRegionChunk      = "AuRg"
	audioRegionFrames     = 22
	audioRegionNameLength = 74
	audioRegionName       = 76
	// audioRegionTail is everything after the name: padding, the region's
	// UUID and trailing fields.
	audioRegionTail = 134
)

// findAudioRegions decodes the audio regions in file order.
func findAudioRegions(chunks []Chunk) []AudioRegion {
	var regions []AudioRegion
	for i, chunk := range chunks {
		if chunk.Type != audioRegionChunk {
			continue
		}
		d := chunk.Data
		if len(d) < audioRegionName {
			continue
		}
		n := int(binary.LittleEndian.Uint16(d[audioRegionNameLength:]))
		if len(d) != audioRegionName+n+audioRegionTail {
			continue
		}
		regions = append(regions, AudioRegion{
			Name:   string(d[audioRegionName : audioRegionName+n]),
			Frames: binary.LittleEndian.Uint32(d[audioRegionFrames:]),
			chunk:  i,
		})
	}
	return regions
}

// SetAudioRegion rewrites the i-th audio region's chunk with r's fields,
// keeping the bytes this package does not decode. A renamed region also
// renames the Apple Loops family archives that carry its old name.
func (p *ProjectData) SetAudioRegion(i int, r AudioRegion) error {
	if i < 0 || i >= len(p.AudioRegions) {
		return fmt.Errorf("logicx: no audio region %d", i)
	}
	if len(r.Name) > 0xffff {
		return errors.New("logicx: audio region name too long")
	}
	prev := p.AudioRegions[i]
	chunk := &p.Chunks[prev.chunk]
	old := chunk.Data
	d := make([]byte, 0, audioRegionName+len(r.Name)+audioRegionTail)
	d = append(d, old[:audioRegionNameLength]...)
	d = binary.LittleEndian.AppendUint16(d, uint16(len(r.Name)))
	d = append(d, r.Name...)
	d = append(d, old[audioRegionName+len(prev.Name):]...)
	binary.LittleEndian.PutUint32(d[audioRegionFrames:], r.Frames)
	chunk.Data = d

	if r.Name != prev.Name {
		for j := range p.Chunks {
			c := &p.Chunks[j]
			if c.Type != "SngO" && c.Type != "GenM" {
				continue
			}
			data, err := renameLoop(c.Data, prev.Name, r.Name)
			if err != nil {
				return fmt.Errorf("logicx: rename loop in %s chunk %d: %w", c.Type, j, err)
			}
			c.Data = data
		}
	}
	r.chunk = prev.chunk
	p.AudioRegions[i] = r
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
	chunk int
}

const (
	environmentChunk      = "Envi"
	environmentNameLength = 158
	environmentName       = 160
)

// findEnvironment decodes the environment objects in file order.
func findEnvironment(chunks []Chunk) []EnvironmentObject {
	var objects []EnvironmentObject
	for i, chunk := range chunks {
		if chunk.Type != environmentChunk || len(chunk.Data) < environmentName {
			continue
		}
		n := int(binary.LittleEndian.Uint16(chunk.Data[environmentNameLength:]))
		if environmentName+n > len(chunk.Data) {
			continue
		}
		objects = append(objects, EnvironmentObject{
			Name: string(chunk.Data[environmentName : environmentName+n]), chunk: i,
		})
	}
	return objects
}

// SetEnvironmentName renames the i-th environment object.
func (p *ProjectData) SetEnvironmentName(i int, name string) error {
	if i < 0 || i >= len(p.Environment) {
		return fmt.Errorf("logicx: no environment object %d", i)
	}
	if len(name) > 0xffff {
		return errors.New("logicx: environment object name too long")
	}
	prev := p.Environment[i]
	chunk := &p.Chunks[prev.chunk]
	old := chunk.Data
	d := make([]byte, 0, len(old)-len(prev.Name)+len(name))
	d = append(d, old[:environmentNameLength]...)
	d = binary.LittleEndian.AppendUint16(d, uint16(len(name)))
	d = append(d, name...)
	d = append(d, old[environmentName+len(prev.Name):]...)
	chunk.Data = d
	p.Environment[i].Name = name
	return nil
}
