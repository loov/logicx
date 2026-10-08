// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-to-tempomap writes a Logic project's tempo map, along with its
// meter, key and markers, as a single-track Standard MIDI File.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/loov/logicx"
	"github.com/loov/logicx/internal/smf"
)

// barOneTick is the project tick of bar 1, which becomes MIDI tick zero.
const barOneTick = 40 * smf.TicksPerQuarter

func main() {
	output := flag.String("o", "", "MIDI file to write (default: the project's name and folder, with .mid)")
	alternative := flag.String("alternative", "", "project alternative (default: first)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: logicx-to-tempomap [-o tempo.mid] [-alternative name] song.logicx")
		os.Exit(2)
	}
	project := filepath.Clean(flag.Arg(0))
	out := *output
	if out == "" {
		out = strings.TrimSuffix(project, filepath.Ext(project)) + ".mid"
	}
	if err := export(project, *alternative, out); err != nil {
		fmt.Fprintln(os.Stderr, "logicx-to-tempomap:", err)
		os.Exit(1)
	}
}

// export writes the tempo map of one project alternative to out.
func export(project, name, out string) error {
	bundle, err := logicx.OpenBundle(project)
	if err != nil {
		return err
	}
	alternative, err := chooseAlternative(bundle.Alternatives, name)
	if err != nil {
		return err
	}
	var file bytes.Buffer
	if err := smf.Write(&file, [][]byte{smf.ConductorTrack(alternative, barOneTick)}); err != nil {
		return err
	}
	return os.WriteFile(out, file.Bytes(), 0o644)
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
