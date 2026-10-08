// SPDX-License-Identifier: GPL-3.0-or-later

package smf

import "testing"

func TestMeterMeta_FallsBackWhenTheDenominatorIsNotAPowerOfTwo(t *testing.T) {
	for _, test := range []struct {
		numerator   uint8
		denominator uint16
		want        [2]byte
	}{
		{4, 4, [2]byte{4, 2}},
		{6, 8, [2]byte{6, 3}},
		{5, 16, [2]byte{5, 4}},
		{3, 6, [2]byte{4, 2}}, // not a power of two, so 4/4
		{0, 0, [2]byte{4, 2}},
	} {
		got := meterMeta(test.numerator, test.denominator)
		if got[0] != test.want[0] || got[1] != test.want[1] {
			t.Errorf("%d/%d = %v, want %v", test.numerator, test.denominator, got[:2], test.want)
		}
	}
}
