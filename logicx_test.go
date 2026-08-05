// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"howett.net/plist"
)

func TestOpenBundle_ParsesBinaryMetadataAndInstrument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo.logicx", "Alternatives", "000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	payload := make([]byte, 8)
	track := append([]byte{0x20}, []byte("Inst 1")...)
	track = append(track, make([]byte, 16-len(track))...)
	track = append(track, []byte{0x29, 0, 0xf7, 0xc5, 1, 0, 0, 0}...)
	payload = append(payload, track...)
	payload = append(payload, []byte("Pigments\x00utrAumua1taK")...)
	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	chunkHeader := make([]byte, 36)
	copy(chunkHeader, "tseT")
	binary.LittleEndian.PutUint64(chunkHeader[28:], uint64(len(payload)))
	data = append(data, chunkHeader...)
	data = append(data, payload...)
	if err := os.WriteFile(filepath.Join(dir, "ProjectData"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	metadata, err := plist.Marshal(map[string]any{
		"SongKey": "C", "BeatsPerMinute": int64(120), "NumberOfTracks": int64(-1),
		"SongSignatureNumerator": int64(-1), "AudioFiles": []string{"take.wav"},
	}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MetaData.plist"), metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := plist.Marshal(map[string]any{"Window": "Mixer"}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "DisplayStateArchive"), archive, 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := OpenBundle(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	alt := bundle.Alternatives[0]
	if len(bundle.PropertyLists) != 2 || bundle.PropertyLists["Alternatives/000/MetaData.plist"] == nil || bundle.PropertyLists["Alternatives/000/DisplayStateArchive"] == nil {
		t.Fatalf("property lists = %+v", bundle.PropertyLists)
	}
	if alt.Metadata.Key != "C" || math.Abs(alt.Metadata.BPM-120) > 1e-9 || alt.Metadata.AudioFileCount != 1 || alt.Metadata.TrackCount != 0 || alt.Metadata.TimeSignature != [2]uint64{4, 4} {
		t.Fatalf("metadata = %+v", alt.Metadata)
	}
	if len(alt.Project.Tracks) != 1 || alt.Project.Tracks[0].Kind != TrackKindInstrument {
		t.Fatalf("tracks = %+v", alt.Project.Tracks)
	}
	if len(alt.Project.Chunks) == 0 {
		t.Fatal("no raw chunks")
	}
	instrument := alt.Project.Tracks[0].Instrument
	if instrument == nil || instrument.Fingerprint() != "aumu/Kat1/Artu" || instrument.Name != "Pigments" {
		t.Fatalf("instrument = %+v", instrument)
	}
}

func TestParseProjectData_ParsesMIDINotes(t *testing.T) {
	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	data = appendChunk(data, "qeSM", []byte("header\x00Lead Trumpet"))
	data = appendChunk(data, "karT", nil)

	events := make([]byte, 48)
	note := events[16:48]
	note[0] = 0x90
	binary.LittleEndian.PutUint16(note[2:4], 3)
	binary.LittleEndian.PutUint32(note[4:8], 38_400)
	copy(note[10:12], "AP")
	note[12] = 74
	binary.LittleEndian.PutUint32(note[28:32], 720)
	data = appendChunk(data, "qSvE", events)

	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Sequences) != 1 || project.Sequences[0].Name != "Lead Trumpet" {
		t.Fatalf("sequences = %+v", project.Sequences)
	}
	got := project.Sequences[0].Notes[0]
	if got.Position != 38_400 || got.PositionFraction != 3 || got.Pitch != 74 || got.Duration != 720 || got.Raw[10] != 'A' {
		t.Fatalf("note = %+v", got)
	}
}

func TestParseProjectData_ParsesMarkers(t *testing.T) {
	data := make([]byte, 24)
	copy(data, []byte{0x23, 0x47, 0xc0, 0xab})
	events := make([]byte, 64)
	binary.LittleEndian.PutUint32(events[0:4], 0x12)
	binary.LittleEndian.PutUint32(events[4:8], 38_400)
	binary.LittleEndian.PutUint32(events[16:20], 4)
	binary.LittleEndian.PutUint32(events[20:24], 0x88000000)
	binary.LittleEndian.PutUint32(events[28:32], 7_680)
	data = appendChunk(data, "qSvE", events)
	data = appendChunkID(data, "qSxT", 4, []byte("prefix{\\rtf1\\ansi \\f0\\fs24 Chorus}"))

	project, err := ParseProjectData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Markers) != 1 {
		t.Fatalf("markers = %+v", project.Markers)
	}
	marker := project.Markers[0]
	if marker.Position != 38_400 || marker.Length != 7_680 || marker.TextID != 4 || marker.Name != "Chorus" || marker.RTF == "" {
		t.Fatalf("marker = %+v", marker)
	}
}

func appendChunk(data []byte, descriptor string, payload []byte) []byte {
	return appendChunkID(data, descriptor, 0, payload)
}

func appendChunkID(data []byte, descriptor string, id uint32, payload []byte) []byte {
	header := make([]byte, 36)
	copy(header, descriptor)
	binary.LittleEndian.PutUint32(header[10:14], id)
	binary.LittleEndian.PutUint64(header[28:], uint64(len(payload)))
	return append(append(data, header...), payload...)
}
