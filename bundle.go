// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Bundle contains every project alternative found in a .logicx directory.
type Bundle struct {
	Name          string
	Alternatives  []Alternative
	PropertyLists map[string]any
}

// Alternative is one saved project alternative within a bundle.
type Alternative struct {
	Name     string
	Metadata Metadata
	Project  ProjectData
}

// OpenBundle reads a .logicx directory. It never writes to the bundle.
func OpenBundle(path string) (*Bundle, error) {
	propertyLists, err := readPropertyLists(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(path, "Alternatives"))
	if err != nil {
		return nil, fmt.Errorf("read alternatives: %w", err)
	}
	bundle := &Bundle{
		Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), PropertyLists: propertyLists,
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(path, "Alternatives", entry.Name())
		project, err := os.ReadFile(filepath.Join(dir, "ProjectData"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read alternative %q: %w", entry.Name(), err)
		}
		metadataPath := filepath.ToSlash(filepath.Join("Alternatives", entry.Name(), "MetaData.plist"))
		metadataValues, ok := propertyLists[metadataPath].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("metadata %q is not a dictionary", entry.Name())
		}
		parsed, err := ParseProjectData(project)
		if err != nil {
			return nil, fmt.Errorf("parse alternative %q: %w", entry.Name(), err)
		}
		bundle.Alternatives = append(bundle.Alternatives, Alternative{
			Name: entry.Name(), Metadata: metadataFromValues(metadataValues), Project: parsed,
		})
	}
	if len(bundle.Alternatives) == 0 {
		return nil, errors.New("logicx: bundle contains no project alternatives")
	}
	return bundle, nil
}
