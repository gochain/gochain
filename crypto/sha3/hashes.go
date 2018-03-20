// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sha3

// This file provides functions for creating instances of the SHA-3
// and SHAKE hash functions, as well as utility functions for hashing
// bytes.

import (
	"hash"
)

// NewKeccak256 creates a new Keccak-256 hash.
func NewKeccak256() hash.Hash { return &state{rate: 136, outputLen: 32, dsbyte: 0x01} }

// NewKeccak256SingleSum is like NewKeccak256, but the returned hash must be
// Reset() after calling Sum(). This allows skipping an internal state copy.
func NewKeccak256SingleSum() hash.Hash {
	return &singleSumState{state{rate: 136, outputLen: 32, dsbyte: 0x01}}
}

// Keccak256 is optimized for cases that call Sum() just once. When h is 32
// bytes it is equivalent to calling NewKeccak256() and using the Write() and
// Sum() hash.Hash interface methods, but avoids the extra allocations in
// (*state).Sum(), which copies the internal state for continued use. This copy
// is unnecessary when the hash.Hash is discarded after calling Sum().
func Keccak256(h []byte, data ...[]byte) {
	s := &state{rate: 136, outputLen: 32, dsbyte: 0x01}
	for _, b := range data {
		s.Write(b)
	}
	s.Read(h)
}

// NewKeccak512 creates a new Keccak-512 hash.
func NewKeccak512() hash.Hash { return &state{rate: 72, outputLen: 64, dsbyte: 0x01} }

// NewKeccak512SingleSum returns an optimized instance, like NewKeccak256SingleSum().
func NewKeccak512SingleSum() hash.Hash {
	return &singleSumState{state{rate: 72, outputLen: 64, dsbyte: 0x01}}
}

// Keccack512 is an optimized alternative to NewKeccak512/Write/Sum, like Keccack256.
func Keccak512(data ...[]byte) []byte {
	var h [64]byte
	s := &state{rate: 72, outputLen: 64, dsbyte: 0x01}
	for _, b := range data {
		s.Write(b)
	}
	s.Read(h[:])
	return h[:]
}

// New224 creates a new SHA3-224 hash.
// Its generic security strength is 224 bits against preimage attacks,
// and 112 bits against collision attacks.
func New224() hash.Hash { return &state{rate: 144, outputLen: 28, dsbyte: 0x06} }

// New256 creates a new SHA3-256 hash.
// Its generic security strength is 256 bits against preimage attacks,
// and 128 bits against collision attacks.
func New256() hash.Hash { return &state{rate: 136, outputLen: 32, dsbyte: 0x06} }

// New384 creates a new SHA3-384 hash.
// Its generic security strength is 384 bits against preimage attacks,
// and 192 bits against collision attacks.
func New384() hash.Hash { return &state{rate: 104, outputLen: 48, dsbyte: 0x06} }

// New512 creates a new SHA3-512 hash.
// Its generic security strength is 512 bits against preimage attacks,
// and 256 bits against collision attacks.
func New512() hash.Hash { return &state{rate: 72, outputLen: 64, dsbyte: 0x06} }

// Sum224 returns the SHA3-224 digest of the data.
func Sum224(data []byte) (digest [28]byte) {
	h := New224()
	h.Write(data)
	h.Sum(digest[:0])
	return
}

// Sum256 returns the SHA3-256 digest of the data.
func Sum256(data []byte) (digest [32]byte) {
	h := New256()
	h.Write(data)
	h.Sum(digest[:0])
	return
}

// Sum384 returns the SHA3-384 digest of the data.
func Sum384(data []byte) (digest [48]byte) {
	h := New384()
	h.Write(data)
	h.Sum(digest[:0])
	return
}

// Sum512 returns the SHA3-512 digest of the data.
func Sum512(data []byte) (digest [64]byte) {
	h := New512()
	h.Write(data)
	h.Sum(digest[:0])
	return
}
