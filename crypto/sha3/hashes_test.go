package sha3

import (
	"bytes"
	"testing"
)

func TestKeccak256(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("hello world"),
		[]byte("hello world hello world hello world hello world hello world hello world"),
		[]byte("0123456789asdfjkl;"),
	} {
		h := NewKeccak256()
		h.Write(data)
		a := h.Sum(nil)
		var b [32]byte
		Keccak256(b[:], data)
		if !bytes.Equal(a, b[:]) {
			t.Errorf("%q: expected %X got %X", data, a, b)
		}
	}
}

func BenchmarkKeccak256(b *testing.B) {
	a := []byte("hello world hello world hello world hello world hello world hello world")
	b.Run("unoptimized", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			h := NewKeccak256()
			h.Write(a)
			_ = h.Sum(nil)
		}
	})
	b.Run("optimized", func(b *testing.B) {
		var hash [32]byte
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Keccak256(hash[:], a)
		}
	})
}

func TestKeccak512(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("hello world"),
		[]byte("hello world hello world hello world hello world hello world hello world"),
		[]byte("0123456789asdfjkl;"),
	} {
		h := NewKeccak512()
		h.Write(data)
		a := h.Sum(nil)
		b := Keccak512(data)
		if !bytes.Equal(a, b) {
			t.Errorf("%q: expected %X got %X", data, a, b)
		}
	}
}

func BenchmarkKeccak512(b *testing.B) {
	a := []byte("hello world hello world hello world hello world hello world hello world")
	b.Run("unoptimized", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			h := NewKeccak512()
			h.Write(a)
			_ = h.Sum(nil)
		}
	})
	b.Run("optimized", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = Keccak512(a)
		}
	})
}
