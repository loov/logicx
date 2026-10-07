// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-from-audio creates a Logic Pro project with one audio track
// playing the given file from the first bar.
//
// The project is a copy of an embedded template — a project Logic saved after
// importing one file into an empty project — with the audio file, its region,
// the track and the bundle renamed. The audio is converted with afconvert, so
// this runs on macOS only.
package main

import (
	"embed"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/loov/logicx"
	"howett.net/plist"
)

//go:embed template.logicx
var template embed.FS

const templateRoot = "template.logicx"

func main() {
	output := flag.String("o", "", "project to create (default: the audio file's name and folder, with .logicx)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: logicx-from-audio [-o song.logicx] song.mp3")
		os.Exit(2)
	}
	audio := flag.Arg(0)
	out := *output
	if out == "" {
		out = strings.TrimSuffix(audio, filepath.Ext(audio)) + ".logicx"
	}
	if err := create(audio, out); err != nil {
		fmt.Fprintln(os.Stderr, "logicx-from-audio:", err)
		os.Exit(1)
	}
}

// create writes the project into a temporary bundle beside out and renames it
// into place, so a failure never leaves a half-written project.
func create(audio, out string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s already exists", out)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), ".logicx-from-audio-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := build(tmp, audio, strings.TrimSuffix(filepath.Base(out), ".logicx")); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

// build fills dir with the template, renamed for project and holding audio.
func build(dir, audio, project string) error {
	metadata, format, err := readPlist(templateRoot + "/Alternatives/000/MetaData.plist")
	if err != nil {
		return err
	}
	sampleRate, ok := metadata["SampleRate"].(uint64)
	if !ok {
		return errors.New("template metadata has no sample rate")
	}

	name := strings.TrimSuffix(filepath.Base(audio), filepath.Ext(audio))
	wavName := name + ".wav"
	wav := filepath.Join(dir, "Media", "Audio Files", wavName)
	if err := os.MkdirAll(filepath.Dir(wav), 0o755); err != nil {
		return err
	}
	convert := exec.Command("afconvert", "-f", "WAVE", "-d", fmt.Sprintf("LEI16@%d", sampleRate), audio, wav)
	if msg, err := convert.CombinedOutput(); err != nil {
		return fmt.Errorf("convert %s: %v: %s", audio, err, msg)
	}
	info, err := readWAV(wav)
	if err != nil {
		return err
	}

	data, err := template.ReadFile(templateRoot + "/Alternatives/000/ProjectData")
	if err != nil {
		return err
	}
	p, err := logicx.ParseProjectData(data)
	if err != nil {
		return err
	}
	if len(p.AudioFiles) != 1 || len(p.AudioRegions) != 1 {
		return errors.New("template must hold exactly one audio file and region")
	}
	file := p.AudioFiles[0]
	file.Name, file.Dir, file.Size = wavName, "Audio Files", info.size
	file.Frames, file.SampleRate, file.Channels, file.BitDepth = info.frames, info.sampleRate, info.channels, info.bitDepth
	if err := file.Save(); err != nil {
		return err
	}
	// Logic names the track after the file it was created for.
	region := p.AudioRegions[0]
	oldName := region.Name
	for _, o := range p.Environment {
		if o.Name == oldName {
			o.Name = name
			if err := o.Save(); err != nil {
				return err
			}
		}
	}
	region.Name, region.Frames = name, info.frames
	if err := region.Save(); err != nil {
		return err
	}
	if err := p.RenameLoops(oldName, name); err != nil {
		return err
	}
	data, err = p.MarshalBinary()
	if err != nil {
		return err
	}

	metadata["AudioFiles"] = []any{"Audio Files/" + wavName}
	information, informationFormat, err := readPlist(templateRoot + "/Resources/ProjectInformation.plist")
	if err != nil {
		return err
	}
	information["VariantNames"] = map[string]any{"0": project}

	alternative := filepath.Join(dir, "Alternatives", "000")
	if err := os.MkdirAll(alternative, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(alternative, "ProjectData"), data, 0o644); err != nil {
		return err
	}
	if err := writePlist(filepath.Join(alternative, "MetaData.plist"), metadata, format); err != nil {
		return err
	}
	if err := writePlist(filepath.Join(dir, "Resources", "ProjectInformation.plist"), information, informationFormat); err != nil {
		return err
	}
	for _, f := range []string{"Alternatives/000/DisplayState.plist", "Alternatives/000/DisplayStateArchive"} {
		if err := copyTemplate(f, filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			return err
		}
	}
	return nil
}

func readPlist(name string) (map[string]any, int, error) {
	data, err := template.ReadFile(name)
	if err != nil {
		return nil, 0, err
	}
	var values map[string]any
	format, err := plist.Unmarshal(data, &values)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", name, err)
	}
	return values, format, nil
}

func writePlist(name string, values map[string]any, format int) error {
	data, err := plist.Marshal(values, format)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o644)
}

func copyTemplate(name, to string) error {
	data, err := fs.ReadFile(template, path.Join(templateRoot, name))
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}

type wavInfo struct {
	size       uint32
	frames     uint32
	sampleRate uint32
	channels   uint16
	bitDepth   uint16
}

// readWAV reads the format and length of a PCM WAVE file.
func readWAV(name string) (wavInfo, error) {
	f, err := os.Open(name)
	if err != nil {
		return wavInfo{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return wavInfo{}, err
	}
	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil || string(riff[:4]) != "RIFF" || string(riff[8:]) != "WAVE" {
		return wavInfo{}, fmt.Errorf("%s: not a WAVE file", name)
	}
	info := wavInfo{size: uint32(stat.Size())}
	for {
		var header [8]byte
		if _, err := io.ReadFull(f, header[:]); err != nil {
			return wavInfo{}, fmt.Errorf("%s: no audio data", name)
		}
		size := binary.LittleEndian.Uint32(header[4:])
		switch string(header[:4]) {
		case "fmt ":
			var format [16]byte
			if size < 16 {
				return wavInfo{}, fmt.Errorf("%s: short format chunk", name)
			}
			if _, err := io.ReadFull(f, format[:]); err != nil {
				return wavInfo{}, err
			}
			info.channels = binary.LittleEndian.Uint16(format[2:])
			info.sampleRate = binary.LittleEndian.Uint32(format[4:])
			info.bitDepth = binary.LittleEndian.Uint16(format[14:])
			size -= 16
		case "data":
			if info.channels == 0 || info.bitDepth == 0 {
				return wavInfo{}, fmt.Errorf("%s: audio data before its format", name)
			}
			info.frames = size / (uint32(info.channels) * uint32(info.bitDepth/8))
			return info, nil
		}
		// Chunks are padded to an even length.
		if _, err := f.Seek(int64(size+size&1), io.SeekCurrent); err != nil {
			return wavInfo{}, err
		}
	}
}
