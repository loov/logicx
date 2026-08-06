// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"howett.net/plist"
)

// Metadata contains the commonly used values from MetaData.plist.
type Metadata struct {
	Key                  string
	Mode                 string
	BPM                  float64
	TimeSignature        [2]uint64
	TrackCount           uint64
	SampleRate           uint64
	AudioFileCount       int
	ImpulseResponseCount int
	FrameRateIndex       uint64
}

// ParseMetadata parses either an XML or binary MetaData.plist payload.
func ParseMetadata(data []byte) (Metadata, error) {
	value, err := ParsePropertyList(data)
	if err != nil {
		return Metadata{}, fmt.Errorf("parse metadata: %w", err)
	}
	values, ok := value.(map[string]any)
	if !ok {
		return Metadata{}, errors.New("parse metadata: root is not a dictionary")
	}
	return metadataFromValues(values), nil
}

// ParsePropertyList parses an XML or binary plist without dropping fields.
func ParsePropertyList(data []byte) (any, error) {
	var value any
	if _, err := plist.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// metadataFromValues extracts the known keys, substituting defaults for keys
// that are missing or of an unexpected type.
func metadataFromValues(values map[string]any) Metadata {
	return Metadata{
		Key:                  metadataString(values["SongKey"], "?"),
		Mode:                 metadataString(values["SongGenderKey"], "?"),
		BPM:                  metadataFloat(values["BeatsPerMinute"]),
		TimeSignature:        [2]uint64{metadataUint(values["SongSignatureNumerator"], 4), metadataUint(values["SongSignatureDenominator"], 4)},
		TrackCount:           metadataUint(values["NumberOfTracks"], 0),
		SampleRate:           metadataUint(values["SampleRate"], 0),
		AudioFileCount:       metadataArrayLen(values["AudioFiles"]),
		ImpulseResponseCount: metadataArrayLen(values["ImpulsResponsesFiles"]),
		FrameRateIndex:       metadataUint(values["FrameRateIndex"], 0),
	}
}

// metadataString returns value as a string, or fallback when it is not a
// non-empty string.
func metadataString(value any, fallback string) string {
	s, ok := value.(string)
	if !ok || s == "" {
		return fallback
	}
	return s
}

// metadataFloat returns value as a float64, or zero when it is not a
// non-negative number.
func metadataFloat(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case uint64:
		return float64(n)
	case int64:
		if n >= 0 {
			return float64(n)
		}
	}
	return 0
}

// metadataUint returns value as a uint64, or fallback when it is not a
// non-negative integer.
func metadataUint(value any, fallback uint64) uint64 {
	switch n := value.(type) {
	case uint64:
		return n
	case int64:
		if n >= 0 {
			return uint64(n)
		}
	}
	return fallback
}

// metadataArrayLen returns the length of value as an array, or zero.
func metadataArrayLen(value any) int {
	items, ok := value.([]any)
	if !ok {
		return 0
	}
	return len(items)
}

// readPropertyLists parses every property list under root, keyed by its
// slash-separated path relative to root.
func readPropertyLists(root string) (map[string]any, error) {
	values := make(map[string]any)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		isPropertyList, err := looksLikePropertyList(path)
		if err != nil {
			return err
		}
		if !isPropertyList {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		value, err := ParsePropertyList(data)
		if err != nil {
			return fmt.Errorf("parse plist %q: %w", path, err)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		values[filepath.ToSlash(relative)] = value
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read property lists: %w", err)
	}
	return values, nil
}

// looksLikePropertyList reports whether path is a property list, by extension
// or by sniffing its leading bytes.
func looksLikePropertyList(path string) (bool, error) {
	if strings.EqualFold(filepath.Ext(path), ".plist") {
		return true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()
	prefix := make([]byte, 256)
	n, err := file.Read(prefix)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read %q: %w", path, err)
	}
	prefix = bytes.TrimSpace(prefix[:n])
	return bytes.HasPrefix(prefix, []byte("bplist00")) || bytes.Contains(prefix, []byte("<plist")), nil
}
