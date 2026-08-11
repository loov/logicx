// SPDX-License-Identifier: GPL-3.0-or-later

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "dialog.h"
*/
import "C"

import (
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/loov/logicx"
)

// AppKit only talks to the main thread, and the Go runtime only guarantees
// the main goroutine stays there when it is asked in an init.
func init() { runtime.LockOSThread() }

// runUI exports one named project through the dialog, or hands over to the
// app delegate, which waits for a dropped or picked project.
func runUI(project string) error {
	if project == "" {
		C.RunDroplet()
		return nil
	}
	return exportWithDialog(project, false)
}

//export goExport
func goExport(path *C.char, offerMenuItem C.int) *C.char {
	if err := exportWithDialog(C.GoString(path), offerMenuItem != 0); err != nil {
		return C.CString(err.Error())
	}
	return nil
}

// exportWithDialog shows the export dialog for one project and writes what it
// asks for. Backing out of the dialog is not an error.
func exportWithDialog(project string, offerMenuItem bool) error {
	bundle, err := logicx.OpenBundle(project)
	if err != nil {
		return err
	}

	cProject := C.CString(project)
	defer C.free(unsafe.Pointer(cProject))
	cDestination := C.CString(strings.TrimSuffix(project, filepath.Ext(project)) + ".musicxml")
	defer C.free(unsafe.Pointer(cDestination))

	names := make([]*C.char, len(bundle.Alternatives))
	for i, alternative := range bundle.Alternatives {
		names[i] = C.CString(alternative.Name)
		defer C.free(unsafe.Pointer(names[i]))
	}

	offer := C.int(0)
	if offerMenuItem {
		offer = 1
	}
	choice := C.ShowExportDialog(cProject, cDestination,
		(**C.char)(unsafe.Pointer(&names[0])), C.int(len(names)), offer)
	if choice.ok == 0 {
		return nil
	}
	defer func() {
		for _, s := range []*C.char{choice.alternative, choice.quantize, choice.quantizeChords, choice.destination} {
			C.free(unsafe.Pointer(s))
		}
	}()

	grid, err := quantizeGrid(C.GoString(choice.quantize))
	if err != nil {
		return err
	}
	chordGrid, err := quantizeGrid(C.GoString(choice.quantizeChords))
	if err != nil {
		return err
	}

	destination := C.GoString(choice.destination)
	opts := options{
		realizeChords: choice.realizeChords != 0,
		quantize:      grid,
		quantizeChord: chordGrid,
		noTriplets:    choice.triplets == 0,
	}
	if err := exportFiles(project, C.GoString(choice.alternative), destination, opts, choice.midi != 0); err != nil {
		return err
	}

	cWritten := C.CString(destination)
	defer C.free(unsafe.Pointer(cWritten))
	C.RevealFile(cWritten)
	return nil
}
