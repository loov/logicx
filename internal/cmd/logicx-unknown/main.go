// SPDX-License-Identifier: GPL-3.0-or-later

// Command logicx-unknown prints the bytes of a project that no layout in
// package logicx describes, to find what they mean.
//
//	logicx-unknown song.logicx > before.txt
//	(change one thing in Logic and save)
//	logicx-unknown song.logicx > after.txt
//	diff before.txt after.txt
//
// Each line is one record, keyed by its chunk's type and sequence identity,
// its kind, its position and its occurrence among records with the same key,
// so that records line up between saves. -summary instead groups records by
// kind and size and shows, for each undescribed byte, whether it is constant
// or how many values it takes: the varying ones are where the data is.
package main

import (
	"cmp"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/loov/logicx"
)

func main() {
	summary := flag.Bool("summary", false, "summarize undescribed bytes by record kind")
	alternative := flag.String("alt", "000", "alternative to read from a .logicx bundle")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: logicx-unknown [-summary] [-alt 000] song.logicx|ProjectData")
		os.Exit(2)
	}
	path := flag.Arg(0)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "Alternatives", *alternative, "ProjectData")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logicx-unknown:", err)
		os.Exit(1)
	}
	p, err := logicx.ParseProjectData(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logicx-unknown:", err)
		os.Exit(1)
	}
	records := p.Unknown()
	if *summary {
		summarize(records)
	} else {
		dump(records)
	}
}

// chunkKey identifies a chunk by its type and the group and sequence numbers
// in its header, which survive Logic reordering chunks.
func chunkKey(c *logicx.Chunk) string {
	return fmt.Sprintf("%s %d:%d", c.Type, binary.LittleEndian.Uint32(c.Header[6:10]), binary.LittleEndian.Uint32(c.Header[10:14]))
}

func dump(records []logicx.UnknownRecord) {
	seen := make(map[string]int)
	for _, r := range records {
		key := chunkKey(r.Chunk) + " " + r.Kind
		if r.Event != nil && len(r.Event.Data) >= 8 {
			key += fmt.Sprintf(" @%d.%d", binary.LittleEndian.Uint32(r.Event.Data[4:]), binary.LittleEndian.Uint16(r.Event.Data[2:]))
		}
		n := seen[key]
		seen[key]++
		if len(r.Spans) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s #%d size %d:", key, n, r.Size)
		for _, s := range r.Spans {
			fmt.Fprintf(&b, " [%d] %s", s.Offset, hex(s.Data))
		}
		fmt.Println(b.String())
	}
}

// hex formats bytes, collapsing runs of zeros.
func hex(data []byte) string {
	var parts []string
	for i := 0; i < len(data); {
		if data[i] == 0 {
			j := i
			for j < len(data) && data[j] == 0 {
				j++
			}
			if j-i > 2 {
				parts = append(parts, fmt.Sprintf("00×%d", j-i))
				i = j
				continue
			}
		}
		parts = append(parts, fmt.Sprintf("%02x", data[i]))
		i++
	}
	return strings.Join(parts, " ")
}

// group gathers records of one kind and size.
type group struct {
	kind    string
	size    int
	count   int
	total   int // bytes in all records
	unknown int // undescribed bytes in all records
	// values holds, for each undescribed offset, which values it took.
	values map[int]*[256]bool
}

func summarize(records []logicx.UnknownRecord) {
	groups := make(map[[2]any]*group)
	var total, unknown int
	for _, r := range records {
		size := r.Size
		if r.Event == nil {
			size = -1 // chunk payloads vary in size; group them by kind alone
		}
		key := [2]any{r.Kind, size}
		g := groups[key]
		if g == nil {
			g = &group{kind: r.Kind, size: size, values: make(map[int]*[256]bool)}
			groups[key] = g
		}
		g.count++
		g.total += r.Size
		total += r.Size
		for _, s := range r.Spans {
			g.unknown += len(s.Data)
			unknown += len(s.Data)
			if size < 0 {
				continue
			}
			for i, v := range s.Data {
				seen := g.values[s.Offset+i]
				if seen == nil {
					seen = new([256]bool)
					g.values[s.Offset+i] = seen
				}
				seen[v] = true
			}
		}
	}

	sorted := make([]*group, 0, len(groups))
	for _, g := range groups {
		sorted = append(sorted, g)
	}
	slices.SortFunc(sorted, func(a, b *group) int {
		return cmp.Or(cmp.Compare(b.unknown, a.unknown), cmp.Compare(a.kind, b.kind), cmp.Compare(a.size, b.size))
	})
	fmt.Printf("%d of %d bytes undescribed (%.1f%%)\n\n", unknown, total, percent(unknown, total))
	for _, g := range sorted {
		if g.unknown == 0 {
			continue
		}
		size := "any size"
		if g.size >= 0 {
			size = fmt.Sprintf("%d bytes", g.size)
		}
		fmt.Printf("%s, %s, ×%d: %d of %d bytes undescribed (%.1f%%)\n", g.kind, size, g.count, g.unknown, g.total, percent(g.unknown, g.total))
		if g.size >= 0 {
			for _, line := range offsetRuns(g) {
				fmt.Println("    " + line)
			}
		}
	}
}

// offsetRuns describes a group's undescribed offsets, joining neighbours that
// are all constant, or that all vary.
func offsetRuns(g *group) []string {
	offsets := make([]int, 0, len(g.values))
	for offset := range g.values {
		offsets = append(offsets, offset)
	}
	slices.Sort(offsets)
	distinct := func(offset int) (n int, value byte) {
		for v, seen := range g.values[offset] {
			if seen {
				n++
				value = byte(v)
			}
		}
		return n, value
	}
	var lines []string
	for i := 0; i < len(offsets); {
		start := offsets[i]
		n, _ := distinct(start)
		constant := n == 1
		j := i
		var values []byte
		for j < len(offsets) && offsets[j] == start+(j-i) {
			m, v := distinct(offsets[j])
			if (m == 1) != constant {
				break
			}
			values = append(values, v)
			j++
		}
		end := start + (j - i)
		if constant {
			lines = append(lines, fmt.Sprintf("[%d:%d] constant %s", start, end, hex(values)))
		} else {
			var counts []string
			for _, offset := range offsets[i:j] {
				m, _ := distinct(offset)
				counts = append(counts, fmt.Sprint(m))
			}
			lines = append(lines, fmt.Sprintf("[%d:%d] varies, values per byte: %s", start, end, strings.Join(counts, " ")))
		}
		i = j
	}
	return lines
}

func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
