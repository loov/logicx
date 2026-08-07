// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-to-musicxml exports discovered MIDI sequences from a Logic
// project bundle as a MusicXML score.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/egonelbre/logicx"
)

// main reads a bundle and writes the score to stdout or -o.
func main() {
	output := flag.String("o", "", "output file (default: stdout)")
	alternative := flag.String("alternative", "", "project alternative (default: first)")
	realizeChords := flag.Bool("realize-chords", false, "write chord staves as voiced pitches instead of rhythm slashes")
	quantize := flag.String("quantize", "1/16", "coarsest note value to snap timing to (1/4, 1/8, 1/16, 1/32, 1/64)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: logicx-to-musicxml [-o score.musicxml] [-alternative 000] [-quantize 1/16] [-realize-chords] project.logicx")
		os.Exit(2)
	}

	grid, err := quantizeGrid(*quantize)
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
	if err := writeMusicXML(&document, alt, options{realizeChords: *realizeChords, quantize: grid}); err != nil {
		fatal(err)
	}
	if *output != "" {
		if err := os.WriteFile(*output, document.Bytes(), 0o644); err != nil {
			fatal(err)
		}
		return
	}
	if _, err := os.Stdout.Write(document.Bytes()); err != nil {
		fatal(err)
	}
}

// quantizeGrid turns a note-value flag such as "1/16" into ticks.
func quantizeGrid(value string) (uint32, error) {
	divisor, err := strconv.ParseUint(strings.TrimPrefix(value, "1/"), 10, 32)
	if err != nil || !strings.HasPrefix(value, "1/") || divisor < 4 || divisor > 64 || divisor&(divisor-1) != 0 {
		return 0, fmt.Errorf("quantize: %q is not one of 1/4, 1/8, 1/16, 1/32, 1/64", value)
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
