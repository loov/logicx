// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-to-musicxml exports discovered MIDI sequences from a Logic
// project bundle as a MusicXML score.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/egonelbre/logicx"
)

func main() {
	output := flag.String("o", "", "output file (default: stdout)")
	alternative := flag.String("alternative", "", "project alternative (default: first)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: logicx-to-musicxml [-o score.musicxml] [-alternative 000] project.logicx")
		os.Exit(2)
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
	if err := writeMusicXML(&document, alt); err != nil {
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "logicx-to-musicxml:", err)
	os.Exit(1)
}

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
