// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"bytes"
	"math"
	"testing"
)

func TestMemoryGetAndGetPtrBounds(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)
	expected := make([]byte, 32)
	for i := range expected {
		expected[i] = byte(i + 1)
	}
	mem.Set(0, 32, expected)

	// Valid access
	if got := mem.Get(0, 32); !bytes.Equal(got, expected) {
		t.Fatalf("expected %x, got %x", expected, got)
	}
	if got := mem.GetPtr(0, 32); !bytes.Equal(got, expected) {
		t.Fatalf("expected %x, got %x", expected, got)
	}

	// Negative or zero bounds must return nil and not panic
	cases := [][2]int64{
		{0, 0},
		{-1, 5},
		{5, -1},
		{-10, -5},
		{math.MinInt64, 10},
		{10, math.MinInt64},
		{0, 33},
		{16, 17},
		{32, 1},
		{math.MaxInt64, math.MaxInt64},
		{1, math.MaxInt64},
	}
	for _, tc := range cases {
		if got := mem.Get(tc[0], tc[1]); got != nil {
			t.Errorf("Get(%d, %d) = %x, want nil", tc[0], tc[1], got)
		}
		if got := mem.GetPtr(tc[0], tc[1]); got != nil {
			t.Errorf("GetPtr(%d, %d) = %x, want nil", tc[0], tc[1], got)
		}
	}
}
