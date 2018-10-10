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

package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/metrics"
	"github.com/gochain-io/gochain/params"
	"github.com/gochain-io/gochain/rlp"
)

// DatabaseReader wraps the Get method of a backing data store.
type DatabaseReader interface {
	Get(key []byte) (value []byte, err error)
}

// DatabaseDeleter wraps the Delete method of a backing data store.
type DatabaseDeleter interface {
	Delete(key []byte) error
}

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

// DBArchivePrefixes is the set of key prefixes which are eligible for archival.
var DBArchivePrefixes = [...]byte{bodyPrefix, blockReceiptsPrefix, headerPrefix}

// DBArchiveKey checks if a key is archivable, and returns its parts if so.
func DBArchiveKey(key []byte) (bool, byte, uint64, common.Hash) {
	if len(key) != 41 {
		return false, 0, 0, common.Hash{}
	}
	switch key[0] {
	case headerPrefix, bodyPrefix, blockReceiptsPrefix:
		return true, key[0], binary.BigEndian.Uint64(key[1:]), common.BytesToHash(key[9:])
	}
	return false, 0, 0, common.Hash{}
}

var (
	headHeaderKey = []byte("LastHeader")
	headBlockKey  = []byte("LastBlock")
	headFastKey   = []byte("LastFast")

	preimagePrefix = "secure-key-"              // preimagePrefix + hash -> preimage
	configPrefix   = []byte("ethereum-config-") // config prefix for the db

	// Chain index prefixes (use `i` + single byte to avoid mixing data types).
	BloomBitsIndexPrefix = []byte("iB") // BloomBitsIndexPrefix is the data table of a chain indexer to track its progress

	// used by old db, now only used for conversion
	oldReceiptsPrefix = []byte("receipts-")
	oldTxMetaSuffix   = []byte{0x01}

	ErrChainConfigNotFound = errors.New("ChainConfig not found") // general config not found error

	preimageCounter    = metrics.NewRegisteredCounter("db/preimage/total", nil)
	preimageHitCounter = metrics.NewRegisteredCounter("db/preimage/hits", nil)
)

// TxLookupEntry is a positional metadata to help looking up the data content of
// a transaction or receipt given only its hash.
type TxLookupEntry struct {
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

// GetCanonicalHash retrieves a hash assigned to a canonical block number.
func GetCanonicalHash(db DatabaseReader, number uint64) common.Hash {
	data, _ := db.Get(numKey(number))
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// missingNumber is returned by GetBlockNumber if no header with the
// given block hash has been stored in the database
const missingNumber = uint64(0xffffffffffffffff)

// GetBlockNumber returns the block number assigned to a block hash
// if the corresponding header is present in the database
func GetBlockNumber(db DatabaseReader, hash common.Hash) uint64 {
	data, _ := db.Get(hashKey(blockHashPrefix, hash))
	if len(data) != 8 {
		return missingNumber
	}
	return binary.BigEndian.Uint64(data)
}

// GetHeadHeaderHash retrieves the hash of the current canonical head block's
// header. The difference between this and GetHeadBlockHash is that whereas the
// last block hash is only updated upon a full block import, the last header
// hash is updated already at header import, allowing head tracking for the
// light synchronization mechanism.
func GetHeadHeaderHash(db DatabaseReader) common.Hash {
	data, _ := db.Get(headHeaderKey)
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// GetHeadBlockHash retrieves the hash of the current canonical head block.
func GetHeadBlockHash(db DatabaseReader) common.Hash {
	data, _ := db.Get(headBlockKey)
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// GetHeadFastBlockHash retrieves the hash of the current canonical head block during
// fast synchronization. The difference between this and GetHeadBlockHash is that
// whereas the last block hash is only updated upon a full block import, the last
// fast hash is updated when importing pre-processed blocks.
func GetHeadFastBlockHash(db DatabaseReader) common.Hash {
	data, _ := db.Get(headFastKey)
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// GetHeaderRLP retrieves a block header in its raw RLP database encoding, or nil
// if the header's not found.
func GetHeaderRLP(db DatabaseReader, hash common.Hash, number uint64) rlp.RawValue {
	data, _ := db.Get(numHashKey(headerPrefix, number, hash))
	return data
}

// GetHeader retrieves the block header corresponding to the hash, nil if none
// found.
func GetHeader(db DatabaseReader, hash common.Hash, number uint64) *types.Header {
	data := GetHeaderRLP(db, hash, number)
	if len(data) == 0 {
		return nil
	}
	header := new(types.Header)
	if err := rlp.Decode(bytes.NewReader(data), header); err != nil {
		log.Error("Invalid block header RLP", "hash", hash, "err", err)
		return nil
	}
	return header
}

// GetBodyRLP retrieves the block body (transactions and uncles) in RLP encoding.
func GetBodyRLP(db DatabaseReader, hash common.Hash, number uint64) rlp.RawValue {
	data, _ := db.Get(numHashKey(bodyPrefix, number, hash))
	return data
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

// GetBody retrieves the block body (transactons, uncles) corresponding to the
// hash, nil if none found.
func GetBody(db DatabaseReader, hash common.Hash, number uint64) *types.Body {
	data := GetBodyRLP(db, hash, number)
	if len(data) == 0 {
		return nil
	}
	body := new(types.Body)
	if err := rlp.Decode(bytes.NewReader(data), body); err != nil {
		log.Error("Invalid block body RLP", "hash", hash, "err", err)
		return nil
	}
	return body
}

// GetTd retrieves a block's total difficulty corresponding to the hash, nil if
// none found.
func GetTd(db DatabaseReader, hash common.Hash, number uint64) *big.Int {
	data, _ := db.Get(tdKey(number, hash))
	if len(data) == 0 {
		return nil
	}
	td := new(big.Int)
	if err := rlp.Decode(bytes.NewReader(data), td); err != nil {
		log.Error("Invalid block total difficulty RLP", "hash", hash, "err", err)
		return nil
	}
	return td
}

// GetBlock retrieves an entire block corresponding to the hash, assembling it
// back from the stored header and body. If either the header or body could not
// be retrieved nil is returned.
//
// Note, due to concurrent download of header and block body the header and thus
// canonical hash can be stored in the database but the body data not (yet).
func GetBlock(db DatabaseReader, hash common.Hash, number uint64) *types.Block {
	// Retrieve the block header and body contents
	header := GetHeader(db, hash, number)
	if header == nil {
		return nil
	}
	body := GetBody(db, hash, number)
	if body == nil {
		return nil
	}
	// Reassemble the block and return
	return types.NewBlockWith(header, body)
}

// GetBlockReceipts retrieves the receipts generated by the transactions included
// in a block given by its hash.
func GetBlockReceipts(db DatabaseReader, hash common.Hash, number uint64) types.Receipts {
	data, _ := db.Get(numHashKey(blockReceiptsPrefix, number, hash))
	if len(data) == 0 {
		return nil
	}
	storageReceipts := types.ReceiptsForStorage{}
	if err := rlp.DecodeBytes(data, &storageReceipts); err != nil {
		log.Error("Invalid receipt array RLP", "hash", hash, "err", err)
		return nil
	}
	return types.Receipts(storageReceipts)
}

// GetTxLookupEntry retrieves the positional metadata associated with a transaction
// hash to allow retrieving the transaction or receipt by hash.
func GetTxLookupEntry(db DatabaseReader, hash common.Hash) (common.Hash, uint64, uint64) {
	// Load the positional metadata from disk and bail if it fails
	data, _ := db.Get(hashKey(lookupPrefix, hash))
	if len(data) == 0 {
		return common.Hash{}, 0, 0
	}
	// Parse and return the contents of the lookup entry
	var entry TxLookupEntry
	if err := rlp.DecodeBytes(data, &entry); err != nil {
		log.Error("Invalid lookup entry RLP", "hash", hash, "err", err)
		return common.Hash{}, 0, 0
	}
	return entry.BlockHash, entry.BlockIndex, entry.Index
}

// GetTransaction retrieves a specific transaction from the database, along with
// its added positional metadata.
func GetTransaction(db DatabaseReader, hash common.Hash) (*types.Transaction, common.Hash, uint64, uint64) {
	// Retrieve the lookup metadata and resolve the transaction from the body
	blockHash, blockNumber, txIndex := GetTxLookupEntry(db, hash)

	if blockHash != (common.Hash{}) {
		body := GetBody(db, blockHash, blockNumber)
		if body == nil || len(body.Transactions) <= int(txIndex) {
			log.Error("Transaction referenced missing", "number", blockNumber, "hash", blockHash, "index", txIndex)
			return nil, common.Hash{}, 0, 0
		}
		return body.Transactions[txIndex], blockHash, blockNumber, txIndex
	}
	// Old transaction representation, load the transaction and it's metadata separately
	data, _ := db.Get(hash.Bytes())
	if len(data) == 0 {
		return nil, common.Hash{}, 0, 0
	}
	var tx types.Transaction
	if err := rlp.DecodeBytes(data, &tx); err != nil {
		return nil, common.Hash{}, 0, 0
	}
	// Retrieve the blockchain positional metadata
	data, _ = db.Get(append(hash.Bytes(), oldTxMetaSuffix...))
	if len(data) == 0 {
		return nil, common.Hash{}, 0, 0
	}
	var entry TxLookupEntry
	if err := rlp.DecodeBytes(data, &entry); err != nil {
		return nil, common.Hash{}, 0, 0
	}
	return &tx, entry.BlockHash, entry.BlockIndex, entry.Index
}

// GetReceipt retrieves a specific transaction receipt from the database, along with
// its added positional metadata.
func GetReceipt(db DatabaseReader, hash common.Hash) (*types.Receipt, common.Hash, uint64, uint64) {
	// Retrieve the lookup metadata and resolve the receipt from the receipts
	blockHash, blockNumber, receiptIndex := GetTxLookupEntry(db, hash)

	if blockHash != (common.Hash{}) {
		receipts := GetBlockReceipts(db, blockHash, blockNumber)
		if len(receipts) <= int(receiptIndex) {
			log.Error("Receipt refereced missing", "number", blockNumber, "hash", blockHash, "index", receiptIndex)
			return nil, common.Hash{}, 0, 0
		}
		return receipts[receiptIndex], blockHash, blockNumber, receiptIndex
	}
	// Old receipt representation, load the receipt and set an unknown metadata
	data, _ := db.Get(append(oldReceiptsPrefix, hash[:]...))
	if len(data) == 0 {
		return nil, common.Hash{}, 0, 0
	}
	var receipt types.ReceiptForStorage
	err := rlp.DecodeBytes(data, &receipt)
	if err != nil {
		log.Error("Invalid receipt RLP", "hash", hash, "err", err)
	}
	return (*types.Receipt)(&receipt), common.Hash{}, 0, 0
}

// GetBloomBits retrieves the compressed bloom bit vector belonging to the given
// section and bit index from the.
func GetBloomBits(db DatabaseReader, bit uint, section uint64, head common.Hash) ([]byte, error) {
	var key [43]byte
	key[0] = bloomBitsPrefix
	binary.BigEndian.PutUint16(key[1:], uint16(bit))
	binary.BigEndian.PutUint64(key[3:], section)
	copy(key[11:], head[:])
	return db.Get(key[:])
}

// WriteCanonicalHash stores the canonical hash for the given block number.
func WriteCanonicalHash(db ethdb.Putter, hash common.Hash, number uint64) error {
	key := numKey(number)
	if err := db.Put(key, hash.Bytes()); err != nil {
		log.Crit("Failed to store number to hash mapping", "err", err)
	}
	return nil
}

// WriteHeadHeaderHash stores the head header's hash.
func WriteHeadHeaderHash(db ethdb.Putter, hash common.Hash) error {
	if err := db.Put(headHeaderKey, hash.Bytes()); err != nil {
		log.Crit("Failed to store last header's hash", "err", err)
	}
	return nil
}

// WriteHeadBlockHash stores the head block's hash.
func WriteHeadBlockHash(db ethdb.Putter, hash common.Hash) error {
	if err := db.Put(headBlockKey, hash.Bytes()); err != nil {
		log.Crit("Failed to store last block's hash", "err", err)
	}
	return nil
}

// WriteHeadFastBlockHash stores the fast head block's hash.
func WriteHeadFastBlockHash(db ethdb.Putter, hash common.Hash) error {
	if err := db.Put(headFastKey, hash.Bytes()); err != nil {
		log.Crit("Failed to store last fast block's hash", "err", err)
	}
	return nil
}

// WriteHeader serializes a block header into the database.
func WriteHeader(db ethdb.Putter, header *types.Header) error {
	data, err := rlp.EncodeToBytes(header)
	if err != nil {
		return err
	}
	hash := header.Hash()
	num := header.Number.Uint64()
	encNum := encodeBlockNumber(num)
	if err := db.Put(hashKey(blockHashPrefix, hash), encNum); err != nil {
		log.Crit("Failed to store hash to number mapping", "err", err)
	}
	key := numHashKey(headerPrefix, num, hash)
	if err := db.Put(key, data); err != nil {
		log.Crit("Failed to store header", "err", err)
	}
	return nil
}

// WriteBody serializes the body of a block into the database.
func WriteBody(db ethdb.Putter, hash common.Hash, number uint64, body *types.Body) error {
	data, err := rlp.EncodeToBytes(body)
	if err != nil {
		return err
	}
	return WriteBodyRLP(db, hash, number, data)
}

// WriteBodyRLP writes a serialized body of a block into the database.
func WriteBodyRLP(db ethdb.Putter, hash common.Hash, number uint64, rlp rlp.RawValue) error {
	key := numHashKey(bodyPrefix, number, hash)
	if err := db.Put(key, rlp); err != nil {
		log.Crit("Failed to store block body", "err", err)
	}
	return nil
}

// WriteTd serializes the total difficulty of a block into the database.
func WriteTd(db ethdb.Putter, hash common.Hash, number uint64, td *big.Int) error {
	data, err := rlp.EncodeToBytes(td)
	if err != nil {
		return err
	}
	key := tdKey(number, hash)
	if err := db.Put(key, data); err != nil {
		log.Crit("Failed to store block total difficulty", "err", err)
	}
	return nil
}

// WriteBlock serializes a block into the database, header and body separately.
func WriteBlock(db ethdb.Putter, block *types.Block) error {
	// Store the body first to retain database consistency
	if err := WriteBody(db, block.Hash(), block.NumberU64(), block.Body()); err != nil {
		return err
	}
	// Store the header too, signaling full block ownership
	if err := WriteHeader(db, block.Header()); err != nil {
		return err
	}
	return nil
}

// WriteBlockReceipts stores all the transaction receipts belonging to a block
// as a single receipt slice. This is used during chain reorganisations for
// rescheduling dropped transactions.
func WriteBlockReceipts(db ethdb.Putter, hash common.Hash, number uint64, receipts types.Receipts) error {
	// Convert the receipts into their storage form and serialize them
	bytes, err := rlp.EncodeToBytes((types.ReceiptsForStorage)(receipts))
	if err != nil {
		return err
	}
	// Store the flattened receipt slice
	key := numHashKey(blockReceiptsPrefix, number, hash)
	if err := db.Put(key, bytes); err != nil {
		log.Crit("Failed to store block receipts", "err", err)
	}
	return nil
}

// WriteTxLookupEntries stores a positional metadata for every transaction from
// a block, enabling hash based transaction and receipt lookups.
func WriteTxLookupEntries(db ethdb.Putter, block *types.Block) error {
	// Iterate over each transaction and encode its metadata
	for i, tx := range block.Transactions() {
		entry := TxLookupEntry{
			BlockHash:  block.Hash(),
			BlockIndex: block.NumberU64(),
			Index:      uint64(i),
		}
		data, err := rlp.EncodeToBytes(entry)
		if err != nil {
			return err
		}
		if err := db.Put(hashKey(lookupPrefix, tx.Hash()), data); err != nil {
			return err
		}
	}
	return nil
}

// WriteBloomBits writes the compressed bloom bits vector belonging to the given
// section and bit index.
func WriteBloomBits(db ethdb.Putter, bit uint, section uint64, head common.Hash, bits []byte) {
	var key [43]byte
	key[0] = bloomBitsPrefix
	binary.BigEndian.PutUint16(key[1:], uint16(bit))
	binary.BigEndian.PutUint64(key[3:], section)
	copy(key[11:], head[:])

	if err := db.Put(key[:], bits); err != nil {
		log.Crit("Failed to store bloom bits", "err", err)
	}
}

// DeleteCanonicalHash removes the number to hash canonical mapping.
func DeleteCanonicalHash(db DatabaseDeleter, number uint64) {
	if err := db.Delete(numKey(number)); err != nil {
		log.Error("Cannot delete canonical hash", "number", number, "err", err)
	}
}

// DeleteHeader removes all block header data associated with a hash.
func DeleteHeader(db DatabaseDeleter, hash common.Hash, number uint64) {
	if err := db.Delete(hashKey(blockHashPrefix, hash)); err != nil {
		log.Error("Cannot delete block hash", "hash", hash, "err", err)
	} else if err := db.Delete(numHashKey(headerPrefix, number, hash)); err != nil {
		log.Error("Cannot delete header", "hash", hash, "number", number, "err", err)
	}
}

// DeleteBody removes all block body data associated with a hash.
func DeleteBody(db DatabaseDeleter, hash common.Hash, number uint64) {
	if err := db.Delete(numHashKey(bodyPrefix, number, hash)); err != nil {
		log.Error("Cannot delete body", "hash", hash, "number", number, "err", err)
	}
}

// DeleteTd removes all block total difficulty data associated with a hash.
func DeleteTd(db DatabaseDeleter, hash common.Hash, number uint64) {
	if err := db.Delete(tdKey(number, hash)); err != nil {
		log.Error("Cannot delete td", "hash", hash, "number", number, "err", err)
	}
}

// DeleteBlock removes all block data associated with a hash.
func DeleteBlock(db DatabaseDeleter, hash common.Hash, number uint64) {
	DeleteBlockReceipts(db, hash, number)
	DeleteHeader(db, hash, number)
	DeleteBody(db, hash, number)
	DeleteTd(db, hash, number)
}

// DeleteBlockReceipts removes all receipt data associated with a block hash.
func DeleteBlockReceipts(db DatabaseDeleter, hash common.Hash, number uint64) {
	if err := db.Delete(numHashKey(blockReceiptsPrefix, number, hash)); err != nil {
		log.Error("Cannot delete block receipts", "hash", hash, "number", number, "err", err)
	}
}

// DeleteTxLookupEntry removes all transaction data associated with a hash.
func DeleteTxLookupEntry(db DatabaseDeleter, hash common.Hash) {
	if err := db.Delete(hashKey(lookupPrefix, hash)); err != nil {
		log.Error("Cannot delete tx lookup entry", "hash", hash, "err", err)
	}
}

// PreimageTable returns a Database instance with the key prefix for preimage entries.
func PreimageTable(db ethdb.Database) ethdb.Database {
	return ethdb.NewTable(db, preimagePrefix)
}

// WritePreimages writes the provided set of preimages to the database. `number` is the
// current block number, and is used for debug messages only.
func WritePreimages(db ethdb.Database, number uint64, preimages map[common.Hash][]byte) error {
	table := PreimageTable(db)
	batch := table.NewBatch()
	hitCount := 0
	for hash, preimage := range preimages {
		if _, err := table.Get(hash.Bytes()); err != nil {
			if err := batch.Put(hash.Bytes(), preimage); err != nil {
				return err
			}
			hitCount++
		}
	}
	preimageCounter.Inc(int64(len(preimages)))
	preimageHitCounter.Inc(int64(hitCount))
	if hitCount > 0 {
		if err := batch.Write(); err != nil {
			return fmt.Errorf("preimage write fail for block %d: %v", number, err)
		}
	}
	return nil
}

// GetBlockChainVersion reads the version number from db.
func GetBlockChainVersion(db DatabaseReader) int {
	var vsn uint
	enc, _ := db.Get([]byte("BlockchainVersion"))
	if err := rlp.DecodeBytes(enc, &vsn); err != nil && err != io.EOF {
		log.Error("Cannot decode block chain version", "err", err)
	}
	return int(vsn)
}

// WriteBlockChainVersion writes vsn as the version number to db.
func WriteBlockChainVersion(db ethdb.Putter, vsn int) {
	enc, _ := rlp.EncodeToBytes(uint(vsn))
	if err := db.Put([]byte("BlockchainVersion"), enc); err != nil {
		log.Error("Cannot write block chain version", "err", err)
	}
}

// WriteChainConfig writes the chain config settings to the database.
func WriteChainConfig(db ethdb.Putter, hash common.Hash, cfg *params.ChainConfig) error {
	// short circuit and ignore if nil config. GetChainConfig
	// will return a default.
	if cfg == nil {
		return nil
	}

	jsonChainConfig, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	return db.Put(append(configPrefix, hash[:]...), jsonChainConfig)
}

// GetChainConfig will fetch the network settings based on the given hash.
func GetChainConfig(db DatabaseReader, hash common.Hash) (*params.ChainConfig, error) {
	jsonChainConfig, _ := db.Get(append(configPrefix, hash[:]...))
	if len(jsonChainConfig) == 0 {
		return nil, ErrChainConfigNotFound
	}

	var config params.ChainConfig
	if err := json.Unmarshal(jsonChainConfig, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// FindCommonAncestor returns the last common ancestor of two block headers
func FindCommonAncestor(db DatabaseReader, a, b *types.Header) *types.Header {
	for bn := b.Number.Uint64(); a.Number.Uint64() > bn; {
		a = GetHeader(db, a.ParentHash, a.Number.Uint64()-1)
		if a == nil {
			return nil
		}
	}
	for an := a.Number.Uint64(); an < b.Number.Uint64(); {
		b = GetHeader(db, b.ParentHash, b.Number.Uint64()-1)
		if b == nil {
			return nil
		}
	}
	for a.Hash() != b.Hash() {
		a = GetHeader(db, a.ParentHash, a.Number.Uint64()-1)
		if a == nil {
			return nil
		}
		b = GetHeader(db, b.ParentHash, b.Number.Uint64()-1)
		if b == nil {
			return nil
		}
	}
	return a
}
