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
	"encoding/binary"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/rlp"
)

// ReadTxLookupEntry retrieves the positional metadata associated with a transaction
// hash to allow retrieving the transaction or receipt by hash.
func ReadTxLookupEntry(db DatabaseReader, hash common.Hash) (common.Hash, uint64, uint64) {
	var data []byte
	Must("get tx lookup entry", func() (err error) {
		data, err = db.Get(hashKey(lookupPrefix, hash))
		if err == common.ErrNotFound {
			err = nil
		}
		return
	})
	if len(data) == 0 {
		return common.Hash{}, 0, 0
	}
	var entry TxLookupEntry
	if err := rlp.DecodeBytes(data, &entry); err != nil {
		log.Error("Invalid transaction lookup entry RLP", "hash", hash, "err", err)
		return common.Hash{}, 0, 0
	}
	return entry.BlockHash, entry.BlockIndex, entry.Index
}

// WriteTxLookupEntries stores a positional metadata for every transaction from
// a block, enabling hash based transaction and receipt lookups.
func WriteTxLookupEntries(db DatabaseWriter, block *types.Block) {
	for i, tx := range block.Transactions() {
		entry := TxLookupEntry{
			BlockHash:  block.Hash(),
			BlockIndex: block.NumberU64(),
			Index:      uint64(i),
		}
		data, err := rlp.EncodeToBytes(entry)
		if err != nil {
			log.Crit("Failed to encode transaction lookup entry", "err", err)
		}
		Must("put tx lookup entry", func() (err error) {
			return db.Put(hashKey(lookupPrefix, tx.Hash()), data)
		})
	}
}

// DeleteTxLookupEntry removes all transaction data associated with a hash.
func DeleteTxLookupEntry(db DatabaseDeleter, hash common.Hash) {
	Must("delete tx lookup entry", func() error {
		return db.Delete(hashKey(lookupPrefix, hash))
	})
}

// ReadTransaction retrieves a specific transaction from the database, along with
// its added positional metadata.
func ReadTransaction(db common.Database, hash common.Hash) (*types.Transaction, common.Hash, uint64, uint64) {
	blockHash, blockNumber, txIndex := ReadTxLookupEntry(db.GlobalTable(), hash)
	if blockHash == (common.Hash{}) {
		return nil, common.Hash{}, 0, 0
	}
	body := ReadBody(db.BodyTable(), blockHash, blockNumber)
	if body == nil || len(body.Transactions) <= int(txIndex) {
		log.Error("Transaction referenced missing", "number", blockNumber, "hash", blockHash, "index", txIndex)
		return nil, common.Hash{}, 0, 0
	}
	return body.Transactions[txIndex], blockHash, blockNumber, txIndex
}

// ReadReceipt retrieves a specific transaction receipt from the database, along with
// its added positional metadata.
func ReadReceipt(db common.Database, hash common.Hash) (*types.Receipt, common.Hash, uint64, uint64) {
	blockHash, blockNumber, receiptIndex := ReadTxLookupEntry(db.GlobalTable(), hash)
	if blockHash == (common.Hash{}) {
		return nil, common.Hash{}, 0, 0
	}
	receipts := ReadReceipts(db.ReceiptTable(), blockHash, blockNumber)
	if len(receipts) <= int(receiptIndex) {
		log.Error("Receipt referenced missing", "number", blockNumber, "hash", blockHash, "index", receiptIndex)
		return nil, common.Hash{}, 0, 0
	}
	return receipts[receiptIndex], blockHash, blockNumber, receiptIndex
}

// ReadBloomBits retrieves the compressed bloom bit vector belonging to the given
// section and bit index from the.
func ReadBloomBits(db DatabaseReader, bit uint, section uint64, head common.Hash) ([]byte, error) {
	var key [43]byte
	key[0] = bloomBitsPrefix
	binary.BigEndian.PutUint16(key[1:], uint16(bit))
	binary.BigEndian.PutUint64(key[3:], section)
	copy(key[11:], head[:])
	return db.Get(key[:])
}

// WriteBloomBits stores the compressed bloom bits vector belonging to the given
// section and bit index.
func WriteBloomBits(db DatabaseWriter, bit uint, section uint64, head common.Hash, bits []byte) {
	var key [43]byte
	key[0] = bloomBitsPrefix
	binary.BigEndian.PutUint16(key[1:], uint16(bit))
	binary.BigEndian.PutUint64(key[3:], section)
	copy(key[11:], head[:])

	Must("put bloom bits", func() error {
		return db.Put(key[:], bits)
	})
}
