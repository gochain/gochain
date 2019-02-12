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

package rawdb

import (
	"bytes"
	"encoding/binary"
	"math/big"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/core/types"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/params"
	"github.com/gochain-io/gochain/v3/rlp"
)

// ReadCanonicalHash retrieves the hash assigned to a canonical block number.
func ReadCanonicalHash(db common.Database, number uint64) common.Hash {
	var data []byte
	Must("get canonical hash", func() (err error) {
		data, err = db.HeaderTable().Get(numKey(number))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// WriteCanonicalHash stores the hash assigned to a canonical block number.
func WriteCanonicalHash(db common.Database, hash common.Hash, number uint64) {
	Must("put canonical hash", func() error {
		return db.HeaderTable().Put(numKey(number), hash.Bytes())
	})
}

// DeleteCanonicalHash removes the number to hash canonical mapping.
func DeleteCanonicalHash(db common.Database, number uint64) {
	Must("delete canonical hash", func() error {
		return db.HeaderTable().Delete(numKey(number))
	})
}

// ReadHeaderNumber returns the header number assigned to a hash.
func ReadHeaderNumber(db DatabaseReader, hash common.Hash) *uint64 {
	var data []byte
	Must("get header number", func() (err error) {
		data, err = db.Get(hashKey(blockHashPrefix, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) != 8 {
		return nil
	}
	number := binary.BigEndian.Uint64(data)
	return &number
}

// ReadHeadHeaderHash retrieves the hash of the current canonical head header.
func ReadHeadHeaderHash(db DatabaseReader) common.Hash {
	var data []byte
	Must("get head header hash", func() (err error) {
		data, err = db.Get(headHeaderKey)
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// WriteHeadHeaderHash stores the hash of the current canonical head header.
func WriteHeadHeaderHash(db DatabaseWriter, hash common.Hash) {
	Must("put head header hash", func() error {
		return db.Put(headHeaderKey, hash.Bytes())
	})
}

// ReadHeadBlockHash retrieves the hash of the current canonical head block.
func ReadHeadBlockHash(db DatabaseReader) common.Hash {
	var data []byte
	Must("get head block hash", func() (err error) {
		data, err = db.Get(headBlockKey)
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// WriteHeadBlockHash stores the head block's hash.
func WriteHeadBlockHash(db DatabaseWriter, hash common.Hash) {
	Must("put head block hash", func() error {
		return db.Put(headBlockKey, hash.Bytes())
	})
}

// ReadHeadFastBlockHash retrieves the hash of the current fast-sync head block.
func ReadHeadFastBlockHash(db DatabaseReader) common.Hash {
	var data []byte
	Must("get fast head block hash", func() (err error) {
		data, err = db.Get(headFastBlockKey)
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// WriteHeadFastBlockHash stores the hash of the current fast-sync head block.
func WriteHeadFastBlockHash(db DatabaseWriter, hash common.Hash) {
	Must("put fast head block hash", func() error {
		return db.Put(headFastBlockKey, hash.Bytes())
	})
}

// ReadFastTrieProgress retrieves the number of tries nodes fast synced to allow
// reporting correct numbers across restarts.
func ReadFastTrieProgress(db DatabaseReader) uint64 {
	var data []byte
	Must("get fast trie progress", func() (err error) {
		data, err = db.Get(fastTrieProgressKey)
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return 0
	}
	return new(big.Int).SetBytes(data).Uint64()
}

// WriteFastTrieProgress stores the fast sync trie process counter to support
// retrieving it across restarts.
func WriteFastTrieProgress(db DatabaseWriter, count uint64) {
	Must("put fast trie progress", func() error {
		return db.Put(fastTrieProgressKey, new(big.Int).SetUint64(count).Bytes())
	})
}

// ReadHeaderRLP retrieves a block header in its raw RLP database encoding.
func ReadHeaderRLP(db DatabaseReader, hash common.Hash, number uint64) rlp.RawValue {
	var data []byte
	Must("get header", func() (err error) {
		data, err = db.Get(numHashKey(headerPrefix, number, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	return data
}

// HasHeader verifies the existence of a block header corresponding to the hash.
func HasHeader(db DatabaseReader, hash common.Hash, number uint64) bool {
	var has bool
	Must("has header", func() (err error) {
		has, err = db.Has(numHashKey(headerPrefix, number, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	return has
}

// ReadHeader retrieves the block header corresponding to the hash.
func ReadHeader(db DatabaseReader, hash common.Hash, number uint64) *types.Header {
	data := ReadHeaderRLP(db, hash, number)
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

// WriteHeader stores a block header into the database and also stores the hash-
// to-number mapping.
func WriteHeader(global, headerTable common.Putter, header *types.Header) {
	data, err := rlp.EncodeToBytes(header)
	if err != nil {
		log.Crit("Failed to encode header", "err", err)
		return
	}
	hash := header.Hash()
	num := header.Number.Uint64()
	encNum := encodeBlockNumber(num)
	Must("put hash to number mapping", func() error {
		return global.Put(hashKey(blockHashPrefix, hash), encNum)
	})
	Must("put header", func() error {
		return headerTable.Put(numHashKey(headerPrefix, num, hash), data)
	})
}

// DeleteHeader removes all block header data associated with a hash.
func DeleteHeader(global, headerTable common.Deleter, hash common.Hash, number uint64) {
	Must("delete header", func() error {
		return global.Delete(hashKey(blockHashPrefix, hash))
	})
	Must("delete header hash to number mapping", func() error {
		return headerTable.Delete(numHashKey(headerPrefix, number, hash))
	})
}

// ReadBodyRLP retrieves the block body (transactions and uncles) in RLP encoding.
func ReadBodyRLP(db DatabaseReader, hash common.Hash, number uint64) rlp.RawValue {
	var data []byte
	Must("read body", func() (err error) {
		data, err = db.Get(numHashKey(bodyPrefix, number, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	return data
}

// WriteBodyRLP stores an RLP encoded block body into the database.
func WriteBodyRLP(db DatabaseWriter, hash common.Hash, number uint64, rlp rlp.RawValue) {
	Must("write body", func() error {
		return db.Put(numHashKey(bodyPrefix, number, hash), rlp)
	})
}

// HasBody verifies the existence of a block body corresponding to the hash.
func HasBody(db DatabaseReader, hash common.Hash, number uint64) bool {
	var has bool
	Must("has body", func() (err error) {
		has, err = db.Has(numHashKey(bodyPrefix, number, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	return has
}

// ReadBody retrieves the block body corresponding to the hash.
func ReadBody(db DatabaseReader, hash common.Hash, number uint64) *types.Body {
	data := ReadBodyRLP(db, hash, number)
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

// WriteBody stores a block body into the database.
func WriteBody(db DatabaseWriter, hash common.Hash, number uint64, body *types.Body) {
	data, err := rlp.EncodeToBytes(body)
	if err != nil {
		log.Crit("Failed to RLP encode body", "err", err)
	}
	WriteBodyRLP(db, hash, number, data)
}

// DeleteBody removes all block body data associated with a hash.
func DeleteBody(db DatabaseDeleter, hash common.Hash, number uint64) {
	Must("delete block body", func() error {
		return db.Delete(numHashKey(bodyPrefix, number, hash))
	})
}

// ReadTd retrieves a block's total difficulty corresponding to the hash.
func ReadTd(db DatabaseReader, hash common.Hash, number uint64) *big.Int {
	var data []byte
	Must("read total difficulty", func() (err error) {
		data, err = db.Get(tdKey(number, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
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

// WriteTd stores the total difficulty of a block into the database.
func WriteTd(db DatabaseWriter, hash common.Hash, number uint64, td *big.Int) {
	data, err := rlp.EncodeToBytes(td)
	if err != nil {
		log.Crit("Failed to RLP encode block total difficulty", "err", err)
	}
	Must("put total difficulty", func() error {
		return db.Put(tdKey(number, hash), data)
	})
}

// DeleteTd removes all block total difficulty data associated with a hash.
func DeleteTd(db DatabaseDeleter, hash common.Hash, number uint64) {
	Must("delete total difficulty", func() error {
		return db.Delete(tdKey(number, hash))
	})
}

// HasReceipts verifies the existence of all the transaction receipts belonging
// to a block.
func HasReceipts(db DatabaseReader, hash common.Hash, number uint64) bool {
	var has bool
	Must("has receipts", func() (err error) {
		has, err = db.Has(numHashKey(blockReceiptsPrefix, number, hash))
		return
	})
	return has
}

// ReadRawReceipts retrieves all the transaction receipts belonging to a block.
// The receipt metadata fields are not guaranteed to be populated, so they
// should not be used. Use ReadReceipts instead if the metadata is needed.
func ReadRawReceipts(db DatabaseReader, hash common.Hash, number uint64) types.Receipts {
	// Retrieve the flattened receipt slice
	var data []byte
	Must("get receipts", func() (err error) {
		data, err = db.Get(numHashKey(blockReceiptsPrefix, number, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return nil
	}
	// Convert the receipts from their storage form to their internal representation
	var receipts types.ReceiptsForStorage
	if err := rlp.DecodeBytes(data, &receipts); err != nil {
		log.Error("Invalid receipt array RLP", "hash", hash, "err", err)
		return nil
	}
	return types.Receipts(receipts)
}

// ReadReceipts retrieves all the transaction receipts belonging to a block, including
// its corresponding metadata fields. If it is unable to populate these metadata
// fields then nil is returned.
//
// The current implementation populates these metadata fields by reading the receipts'
// corresponding block body, so if the block body is not found it will return nil even
// if the receipt itself is stored.
func ReadReceipts(db common.Database, hash common.Hash, number uint64, config *params.ChainConfig) types.Receipts {
	// We're deriving many fields from the block body, retrieve beside the receipt
	receipts := ReadRawReceipts(db.ReceiptTable(), hash, number)
	if receipts == nil {
		return nil
	}
	body := ReadBody(db.BodyTable(), hash, number)
	if body == nil {
		log.Error("Missing body but have receipt", "hash", hash, "number", number)
		return nil
	}
	if err := receipts.DeriveFields(config, hash, number, body.Transactions); err != nil {
		log.Error("Failed to derive block receipts fields", "hash", hash, "number", number, "err", err)
		return nil
	}
	return receipts
}

// WriteReceipts stores all the transaction receipts belonging to a block.
func WriteReceipts(db DatabaseWriter, hash common.Hash, number uint64, receipts types.Receipts) {
	// Convert the receipts into their storage form and serialize them
	bytes, err := rlp.EncodeToBytes((types.ReceiptsForStorage)(receipts))
	if err != nil {
		log.Crit("Failed to encode block receipts", "err", err)
	}
	// Store the flattened receipt slice
	Must("put receipts", func() error {
		return db.Put(numHashKey(blockReceiptsPrefix, number, hash), bytes)
	})
}

// DeleteReceipts removes all receipt data associated with a block hash.
func DeleteReceipts(db DatabaseDeleter, hash common.Hash, number uint64) {
	Must("delete receipts", func() error {
		return db.Delete(numHashKey(blockReceiptsPrefix, number, hash))
	})
}

// ReadBlock retrieves an entire block corresponding to the hash, assembling it
// back from the stored header and body. If either the header or body could not
// be retrieved nil is returned.
//
// Note, due to concurrent download of header and block body the header and thus
// canonical hash can be stored in the database but the body data not (yet).
func ReadBlock(db common.Database, hash common.Hash, number uint64) *types.Block {
	header := ReadHeader(db.HeaderTable(), hash, number)
	if header == nil {
		return nil
	}
	body := ReadBody(db.BodyTable(), hash, number)
	if body == nil {
		return nil
	}
	return types.NewBlockWith(header, body)
}

// WriteBlock serializes a block into the database, header and body separately.
func WriteBlock(db common.Database, block *types.Block) {
	WriteBody(db.BodyTable(), block.Hash(), block.NumberU64(), block.Body())
	WriteHeader(db.GlobalTable(), db.HeaderTable(), block.Header())
}

// DeleteBlock removes all block data associated with a hash.
func DeleteBlock(db common.Database, hash common.Hash, number uint64) {
	DeleteReceipts(db.ReceiptTable(), hash, number)
	DeleteHeader(db.GlobalTable(), db.HeaderTable(), hash, number)
	DeleteBody(db.BodyTable(), hash, number)
	DeleteTd(db.GlobalTable(), hash, number)
}

// FindCommonAncestor returns the last common ancestor of two block headers
func FindCommonAncestor(db DatabaseReader, a, b *types.Header) *types.Header {
	for bn := b.Number.Uint64(); a.Number.Uint64() > bn; {
		a = ReadHeader(db, a.ParentHash, a.Number.Uint64()-1)
		if a == nil {
			return nil
		}
	}
	for an := a.Number.Uint64(); an < b.Number.Uint64(); {
		b = ReadHeader(db, b.ParentHash, b.Number.Uint64()-1)
		if b == nil {
			return nil
		}
	}
	for a.Hash() != b.Hash() {
		a = ReadHeader(db, a.ParentHash, a.Number.Uint64()-1)
		if a == nil {
			return nil
		}
		b = ReadHeader(db, b.ParentHash, b.Number.Uint64()-1)
		if b == nil {
			return nil
		}
	}
	return a
}
