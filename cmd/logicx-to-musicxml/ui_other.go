// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !darwin

package main

import "errors"

// runUI is macOS only, where the dialog comes from AppKit.
func runUI(string) error {
	return errors.New("-ui needs macOS")
}
