// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-to-musicxml exports discovered MIDI sequences from a Logic
// project bundle as a MusicXML score.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egonelbre/logicx"
)

// main reads a bundle and writes the score to stdout or -o.
func main() {
	// Finder launches the app bundle with a process serial number the flag
	// package would reject, and with no project to export.
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "-psn_") {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	if len(os.Args) == 1 && bundled() {
		if err := runUI(""); err != nil {
			fatal(err)
		}
		return
	}

	output := flag.String("o", "", "output file (default: stdout)")
	alternative := flag.String("alternative", "", "project alternative (default: first)")
	realizeChords := flag.Bool("realize-chords", false, "write chord staves as voiced pitches instead of rhythm slashes")
	quantize := flag.String("quantize", "1/16", "coarsest note value to snap note timing to (1/1 to 1/64, or off)")
	quantizeChords := flag.String("quantize-chords", "1/4", "note value to snap chord symbols to (1/1 to 1/64, or off)")
	triplets := flag.Bool("triplets", true, "notate beats played in thirds as triplets")
	midi := flag.Bool("midi", false, "also write the unquantized note data beside -o as a Standard MIDI File")
	ui := flag.Bool("ui", false, "show an export dialog in the browser instead of exporting directly")
	flag.Parse()
	if flag.NArg() > 1 || (flag.NArg() == 0 && !*ui) {
		fmt.Fprintln(os.Stderr, "usage: logicx-to-musicxml [-o score.musicxml] [-alternative 000] [-quantize 1/16] [-quantize-chords 1/4] [-triplets=false] [-realize-chords] [-midi] [-ui] project.logicx")
		os.Exit(2)
	}
	if *ui {
		if err := runUI(flag.Arg(0)); err != nil {
			fatal(err)
		}
		return
	}

	grid, err := quantizeGrid(*quantize)
	if err != nil {
		fatal(err)
	}
	chordGrid, err := quantizeGrid(*quantizeChords)
	if err != nil {
		fatal(err)
	}

	opts := options{
		realizeChords: *realizeChords,
		quantize:      grid,
		quantizeChord: chordGrid,
		noTriplets:    !*triplets,
	}
	if *output != "" {
		if err := exportFiles(flag.Arg(0), *alternative, *output, opts, *midi); err != nil {
			fatal(err)
		}
		return
	}
	if *midi {
		fatal(errors.New("-midi needs -o, as two files cannot share stdout"))
	}

	bundle, err := logicx.OpenBundle(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	alt, err := chooseAlternative(bundle.Alternatives, *alternative)
	if err != nil {
		fatal(err)
	}
	if err := writeMusicXML(os.Stdout, alt, opts); err != nil {
		fatal(err)
	}
}

// exportFiles writes the score for one project to output, plus the unquantized
// MIDI companion when midi is set.
func exportFiles(project, alternative, output string, opts options, midi bool) error {
	bundle, err := logicx.OpenBundle(project)
	if err != nil {
		return err
	}
	alt, err := chooseAlternative(bundle.Alternatives, alternative)
	if err != nil {
		return err
	}

	var document bytes.Buffer
	if err := writeMusicXML(&document, alt, opts); err != nil {
		return err
	}
	if err := os.WriteFile(output, document.Bytes(), 0o644); err != nil {
		return err
	}
	if !midi {
		return nil
	}

	var notes bytes.Buffer
	if err := writeMIDI(&notes, alt); err != nil {
		return err
	}
	return os.WriteFile(midiPath(output), notes.Bytes(), 0o644)
}

// bundled reports whether this binary is the executable of a .app, which is
// how Finder and the Services menu start it.
func bundled() bool {
	executable, err := os.Executable()
	return err == nil && strings.Contains(executable, ".app/Contents/MacOS/")
}

// midiPath is the score path with a .mid extension, the companion file -midi
// writes.
func midiPath(score string) string {
	return strings.TrimSuffix(score, filepath.Ext(score)) + ".mid"
}

// quantizeGrid turns a note-value flag such as "1/16" into ticks, with "off"
// asking for no grid at all.
func quantizeGrid(value string) (uint32, error) {
	if value == "off" {
		return 0, nil
	}
	divisor, err := strconv.ParseUint(strings.TrimPrefix(value, "1/"), 10, 32)
	if err != nil || !strings.HasPrefix(value, "1/") || divisor < 1 || divisor > 64 || divisor&(divisor-1) != 0 {
		return 0, fmt.Errorf("%q is not a note value from 1/1 to 1/64, or \"off\"", value)
	}
	return uint32(uint64(ticksPerQuarter) * 4 / divisor), nil
}

// fatal reports err and exits.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "logicx-to-musicxml:", err)
	os.Exit(1)
}

// chooseAlternative returns the named alternative, or the first one when no
// name is given. OpenBundle guarantees at least one alternative.
func chooseAlternative(alternatives []logicx.Alternative, name string) (logicx.Alternative, error) {
	if name == "" {
		return alternatives[0], nil
	}
	for _, alternative := range alternatives {
		if alternative.Name == name {
			return alternative, nil
		}
	}
	return logicx.Alternative{}, fmt.Errorf("alternative %q not found", name)
}
