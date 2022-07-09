// Copyright 2018 The go-ethereum Authors
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

// Package rawdb contains a collection of low level database accessors.
package rawdb

import (
	"encoding/binary"

	"github.com/gochain/gochain/v4/common"
)

const (
	// Data item prefixes (use single byte to avoid mixing data types, avoid `i`).
	headerPrefix        byte = 'h' // headerPrefix + num (uint64 big endian) + hash -> header
	tdSuffix            byte = 't' // headerPrefix + num (uint64 big endian) + hash + tdSuffix -> td
	numSuffix           byte = 'n' // headerPrefix + num (uint64 big endian) + numSuffix -> hash
	blockHashPrefix     byte = 'H' // blockHashPrefix + hash -> num (uint64 big endian)
	bodyPrefix          byte = 'b' // bodyPrefix + num (uint64 big endian) + hash -> block body
	blockReceiptsPrefix byte = 'r' // blockReceiptsPrefix + num (uint64 big endian) + hash -> block receipts
	lookupPrefix        byte = 'l' // lookupPrefix + hash -> transaction/receipt lookup metadata
	bloomBitsPrefix     byte = 'B' // bloomBitsPrefix + bit (uint16 big endian) + section (uint64 big endian) + hash -> bloom bits
)

// The fields below define the low level database schema prefixing.
var (
	// databaseVersionKey tracks the current database version.
	databaseVersionKey = []byte("BlockchainVersion")

	// headHeaderKey tracks the latest know header's hash.
	headHeaderKey = []byte("LastHeader")

	// headBlockKey tracks the latest know full block's hash.
	headBlockKey = []byte("LastBlock")

	// headFastBlockKey tracks the latest known incomplete block's hash duirng fast sync.
	headFastBlockKey = []byte("LastFast")

	// fastTrieProgressKey tracks the number of trie entries imported during fast sync.
	fastTrieProgressKey = []byte("TrieSync")

	preimagePrefix = "secure-key-"              // preimagePrefix + hash -> preimage
	configPrefix   = []byte("ethereum-config-") // config prefix for the db

	// Chain index prefixes (use `i` + single byte to avoid mixing data types).
	BloomBitsIndexPrefix = []byte("iB") // BloomBitsIndexPrefix is the data table of a chain indexer to track its progress
)

// LegacyTxLookupEntry is the legacy TxLookupEntry definition with some unnecessary
// fields.
type LegacyTxLookupEntry struct {
	BlockHash  common.Hash
	BlockIndex uint64
	Index      uint64
}

// encodeBlockNumber encodes a block number as big endian uint64
func encodeBlockNumber(number uint64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

func numHashKey(prefix byte, number uint64, hash common.Hash) []byte {
	var k [41]byte
	k[0] = prefix
	binary.BigEndian.PutUint64(k[1:], number)
	copy(k[9:], hash[:])
	return k[:]
}

func numKey(number uint64) []byte {
	var k [10]byte
	k[0] = headerPrefix
	binary.BigEndian.PutUint64(k[1:], number)
	k[9] = numSuffix
	return k[:]
}

func tdKey(number uint64, hash common.Hash) []byte {
	var k [42]byte
	k[0] = headerPrefix
	binary.BigEndian.PutUint64(k[1:], number)
	copy(k[9:], hash[:])
	k[41] = tdSuffix
	return k[:]
}

func hashKey(prefix byte, hash common.Hash) []byte {
	var k [33]byte
	k[0] = prefix
	copy(k[1:], hash[:])
	return k[:]
}

// preimageKey = preimagePrefix + hash
func preimageKey(hash common.Hash) []byte {
	return append([]byte(preimagePrefix), hash.Bytes()...)
}

// configKey = configPrefix + hash
func configKey(hash common.Hash) []byte {
	return append(configPrefix, hash.Bytes()...)
}
