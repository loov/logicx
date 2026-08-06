// SPDX-License-Identifier: GPL-3.0-or-later

package record

import "testing"

func TestDecode_RejectsOutOfBoundsField(t *testing.T) {
	if Decode([]byte{1}, Copy(0, make([]byte, 2))) {
		t.Fatal("Decode() accepted a truncated field")
	}
}
