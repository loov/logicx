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
	output := flag.String("o", "", "output file (default: stdout)")
	alternative := flag.String("alternative", "", "project alternative (default: first)")
	realizeChords := flag.Bool("realize-chords", false, "write chord staves as voiced pitches instead of rhythm slashes")
	quantize := flag.String("quantize", "1/16", "coarsest note value to snap note timing to (1/1 to 1/64)")
	quantizeChords := flag.String("quantize-chords", "1/4", "note value to snap chord symbols to (1/1 to 1/64)")
	midi := flag.Bool("midi", false, "also write the unquantized note data beside -o as a Standard MIDI File")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: logicx-to-musicxml [-o score.musicxml] [-alternative 000] [-quantize 1/16] [-quantize-chords 1/4] [-realize-chords] [-midi] project.logicx")
		os.Exit(2)
	}

	grid, err := quantizeGrid(*quantize)
	if err != nil {
		fatal(err)
	}
	chordGrid, err := quantizeGrid(*quantizeChords)
	if err != nil {
		fatal(err)
	}

	bundle, err := logicx.OpenBundle(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	alt, err := chooseAlternative(bundle.Alternatives, *alternative)
	if err != nil {
		fatal(err)
	}

	var document bytes.Buffer
	if err := writeMusicXML(&document, alt, options{realizeChords: *realizeChords, quantize: grid, quantizeChord: chordGrid}); err != nil {
		fatal(err)
	}
	if *output == "" {
		if *midi {
			fatal(errors.New("-midi needs -o, as two files cannot share stdout"))
		}
		if _, err := os.Stdout.Write(document.Bytes()); err != nil {
			fatal(err)
		}
		return
	}
	if err := os.WriteFile(*output, document.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	if !*midi {
		return
	}

	var notes bytes.Buffer
	if err := writeMIDI(&notes, alt); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(midiPath(*output), notes.Bytes(), 0o644); err != nil {
		fatal(err)
	}
}

// midiPath is the score path with a .mid extension, the companion file -midi
// writes.
func midiPath(score string) string {
	return strings.TrimSuffix(score, filepath.Ext(score)) + ".mid"
}

// quantizeGrid turns a note-value flag such as "1/16" into ticks.
func quantizeGrid(value string) (uint32, error) {
	divisor, err := strconv.ParseUint(strings.TrimPrefix(value, "1/"), 10, 32)
	if err != nil || !strings.HasPrefix(value, "1/") || divisor < 1 || divisor > 64 || divisor&(divisor-1) != 0 {
		return 0, fmt.Errorf("%q is not a note value from 1/1 to 1/64", value)
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
