// Copyright 2016 The go-ethereum Authors
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
	"math/rand"
	"testing"

	"github.com/gochain/gochain/v4/core/types"
	"github.com/gochain/gochain/v4/crypto"
)

// Tests that transactions can be added to strict lists and list contents and
// nonce boundaries are correctly maintained.
func TestStrictTxListAdd(t *testing.T) {
	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 1024)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}
	// Insert the transactions in a random order
	list := newTxList(true)
	for _, v := range rand.Perm(len(txs)) {
		list.Add(txs[v], DefaultTxPoolConfig.PriceBump)
	}
	// Verify internal state
	if len(list.txs.items) != len(txs) {
		t.Errorf("transaction count mismatch: have %d, want %d", len(list.txs.items), len(txs))
	}
	for i, tx := range txs {
		if list.txs.items[tx.Nonce()] != tx {
			t.Errorf("item %d: transaction mismatch: have %v, want %v", i, list.txs.items[tx.Nonce()], tx)
		}
	}
}

func TestTxSortedMap_Cap(t *testing.T) {
	txSortedMap := newTxSortedMap()

	txs := make(types.Transactions, 1024)
	key, _ := crypto.GenerateKey()
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
		txSortedMap.Put(txs[i])
	}

	var removed int
	txSortedMap.Cap(500, func(*types.Transaction) {
		removed++
	})
	if removed != 524 {
		t.Fatalf("expected to remove 524 but got %d", removed)
	}

	removed = 0
	txSortedMap.Cap(1, func(*types.Transaction) {
		removed++
	})
	if removed != 499 {
		t.Fatalf("expected to remove 499 but got %d", removed)
	}

	txSortedMap.cache = nil

	removed = 0
	txSortedMap.Cap(0, func(*types.Transaction) {
		removed++
	})
	if removed != 1 {
		t.Errorf("expected to remove 1 but got %d", removed)
	}

	if len(txSortedMap.items) > 0 || len(txSortedMap.cache) > 0 || len(*txSortedMap.index) > 0 {
		t.Fatalf("expected empty txSortedMap but got %#v", txSortedMap)
	}
}

func TestTxSortedMap_Ready(t *testing.T) {
	txSortedMap := newTxSortedMap()

	txs := make(types.Transactions, 10)
	key, _ := crypto.GenerateKey()
	for i := 1; i < 5; i++ {
		txs[i] = transaction(uint64(i), 0, key)
		txSortedMap.Put(txs[i])
	}
	for i := 6; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
		txSortedMap.Put(txs[i])
	}

	var removed int
	txSortedMap.Ready(0, func(*types.Transaction) {
		removed++
	})
	if removed != 0 {
		t.Fatalf("expected to remove none but got %d", removed)
	}

	txSortedMap.cache = nil

	removed = 0
	txSortedMap.Ready(1, func(*types.Transaction) {
		removed++
	})
	if removed != 4 {
		t.Fatalf("expected to remove 4 but got %d", removed)
	}

	removed = 0
	txSortedMap.Ready(5, func(*types.Transaction) {
		removed++
	})
	if removed != 0 {
		t.Fatalf("expected to remove none but got %d", removed)
	}

	txSortedMap.cache = nil

	removed = 0
	txSortedMap.Ready(6, func(*types.Transaction) {
		removed++
	})
	if removed != 4 {
		t.Errorf("expected to remove 4 but got %d", removed)
	}

	if len(txSortedMap.items) > 0 || len(txSortedMap.cache) > 0 || len(*txSortedMap.index) > 0 {
		t.Fatalf("expected empty txSortedMap but got %#v", txSortedMap)
	}
}

func TestTxSortedMap_Forward(t *testing.T) {
	txSortedMap := newTxSortedMap()

	txs := make(types.Transactions, 10)
	key, _ := crypto.GenerateKey()
	for i := 1; i < 5; i++ {
		txs[i] = transaction(uint64(i), 0, key)
		txSortedMap.Put(txs[i])
	}
	for i := 6; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
		txSortedMap.Put(txs[i])
	}

	var removed int
	txSortedMap.Forward(1, func(*types.Transaction) {
		removed++
	})
	if removed != 0 {
		t.Fatalf("expected to remove none but got %d", removed)
	}

	removed = 0
	txSortedMap.Forward(5, func(*types.Transaction) {
		removed++
	})
	if removed != 4 {
		t.Fatalf("expected to remove 4 but got %d", removed)
	}

	txSortedMap.cache = nil

	removed = 0
	txSortedMap.Forward(10, func(*types.Transaction) {
		removed++
	})
	if removed != 4 {
		t.Errorf("expected to remove 4 but got %d", removed)
	}

	if len(txSortedMap.items) > 0 || len(txSortedMap.cache) > 0 || len(*txSortedMap.index) > 0 {
		t.Fatalf("expected empty txSortedMap but got %#v", txSortedMap)
	}
}

func TestTxSortedMap_Filter(t *testing.T) {
	txSortedMap := newTxSortedMap()

	txs := make(types.Transactions, 10)
	key, _ := crypto.GenerateKey()
	for i := 1; i < 5; i++ {
		txs[i] = transaction(uint64(i), 100, key)
		txSortedMap.Put(txs[i])
	}
	for i := 6; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 200, key)
		txSortedMap.Put(txs[i])
	}

	var removed, invalid int
	txSortedMap.Filter(func(tx *types.Transaction) bool {
		return tx.Gas() == 100
	}, false, func(*types.Transaction) { removed++ }, func(*types.Transaction) { invalid++ })

	if removed != 4 || invalid != 0 {
		t.Fatalf("expected 4 removal and 0 invalid but got %d removals and %d invalid", removed, invalid)
	}

	removed, invalid = 0, 0
	txSortedMap.Filter(func(tx *types.Transaction) bool {
		return tx.Gas() == 200
	}, true, func(*types.Transaction) { removed++ }, func(*types.Transaction) { invalid++ })
	if removed != 1 || invalid != 3 {
		t.Fatalf("expected 1 removal and 3 invalid but got %d removals and %d invalid", removed, invalid)
	}

	if len(txSortedMap.items) > 0 || len(txSortedMap.cache) > 0 || len(*txSortedMap.index) > 0 {
		t.Fatalf("expected empty txSortedMap but got %#v", txSortedMap)
	}
}

func TestTxSortedMap_ForLast(t *testing.T) {
	txSortedMap := newTxSortedMap()

	txs := make(types.Transactions, 10)
	key, _ := crypto.GenerateKey()
	for i := 1; i < 5; i++ {
		txs[i] = transaction(uint64(i), 100, key)
		txSortedMap.Put(txs[i])
	}
	for i := 6; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 200, key)
		txSortedMap.Put(txs[i])
	}

	var cnt int
	fn := func(*types.Transaction) { cnt++ }
	txSortedMap.ForLast(2, fn)
	if cnt != 2 {
		t.Errorf("expected 2 but got %d", cnt)
	}

	txSortedMap.cache = nil

	cnt = 0
	txSortedMap.ForLast(4, fn)
	if cnt != 4 {
		t.Errorf("expected 6 but got %d", cnt)
	}

	txSortedMap.cache = nil

	cnt = 0
	txSortedMap.ForLast(2, fn)
	if cnt != 2 {
		t.Errorf("expected 8 but got %d", cnt)
	}

	if len(txSortedMap.items) > 0 || len(txSortedMap.cache) > 0 || len(*txSortedMap.index) > 0 {
		t.Fatalf("expected empty txSortedMap but got %#v", txSortedMap)
	}
}
