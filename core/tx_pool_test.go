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
	"crypto/ecdsa"
	"fmt"
	"io/ioutil"
	"math/big"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gochain/gochain/v4/common"
	"github.com/gochain/gochain/v4/core/state"
	"github.com/gochain/gochain/v4/core/types"
	"github.com/gochain/gochain/v4/crypto"
	"github.com/gochain/gochain/v4/ethdb"
	"github.com/gochain/gochain/v4/params"
)

// testTxPoolConfig is a transaction pool configuration without stateful disk
// sideeffects used during testing.
var testTxPoolConfig TxPoolConfig

func init() {
	testTxPoolConfig = DefaultTxPoolConfig
	testTxPoolConfig.AccountSlots = 512
	testTxPoolConfig.GlobalSlots = 4096
	testTxPoolConfig.AccountQueue = 256
	testTxPoolConfig.GlobalQueue = 1024
	testTxPoolConfig.Journal = ""
	testTxPoolConfig.PriceLimit = 1
}

type testBlockChain struct {
	statedb *state.StateDB

	chainHeadFeed ChainHeadFeed

	mu       sync.Mutex
	gasLimit uint64
}

func newTestBlockChain(statedb *state.StateDB, gasLimit uint64) *testBlockChain {
	bc := &testBlockChain{
		statedb: statedb,
	}
	bc.setGasLimit(gasLimit)
	return bc
}

func (bc *testBlockChain) setGasLimit(gl uint64) {
	bc.mu.Lock()
	bc.gasLimit = gl
	bc.mu.Unlock()
}

func (bc *testBlockChain) CurrentBlock() *types.Block {
	bc.mu.Lock()
	gl := bc.gasLimit
	bc.mu.Unlock()
	return types.NewBlock(&types.Header{
		GasLimit: gl,
	}, nil, nil, nil)
}

func (bc *testBlockChain) GetBlock(hash common.Hash, number uint64) *types.Block {
	return bc.CurrentBlock()
}

func (bc *testBlockChain) StateAt(common.Hash) (*state.StateDB, error) {
	return bc.statedb, nil
}

func (bc *testBlockChain) SubscribeChainHeadEvent(ch chan<- ChainHeadEvent, name string) {
	bc.chainHeadFeed.Subscribe(ch, name)
}

func (bc *testBlockChain) UnsubscribeChainHeadEvent(ch chan<- ChainHeadEvent) {
	bc.chainHeadFeed.Unsubscribe(ch)
}

func transaction(nonce uint64, gaslimit uint64, key *ecdsa.PrivateKey) *types.Transaction {
	return pricedTransaction(nonce, gaslimit, big.NewInt(1), key)
}

func pricedTransaction(nonce uint64, gaslimit uint64, gasprice *big.Int, key *ecdsa.PrivateKey) *types.Transaction {
	tx, _ := types.SignTx(types.NewTransaction(nonce, common.Address{}, big.NewInt(100), gaslimit, gasprice, nil), types.HomesteadSigner{}, key)
	return tx
}

func setupTxPool() (*TxPool, *ecdsa.PrivateKey) {
	return setupTxPoolState(nil)
}

func setupTxPoolState(stFn func(*state.StateDB, *ecdsa.PrivateKey)) (*TxPool, *ecdsa.PrivateKey) {
	diskdb := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(diskdb))
	key, _ := crypto.GenerateKey()
	if stFn != nil {
		stFn(statedb, key)
	}
	blockchain := newTestBlockChain(statedb, 1000000)

	pool := NewTxPool(testTxPoolConfig, params.TestChainConfig, blockchain)

	return pool, key
}

func resetTxPool(pool *TxPool) {
	pool.pending = make(map[common.Address]*txList)
	pool.queue = make(map[common.Address]*txList)
	pool.beats = make(map[common.Address]time.Time)
	pool.all = newTxLookup(0)
}

// validateTxPoolInternals checks various consistency invariants within the pool.
func validateTxPoolInternals(pool *TxPool) error {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	// Ensure the total transaction set is consistent with pending + queued
	pending, queued := pool.stats()
	if total := pool.all.Count(); total != pending+queued {
		return fmt.Errorf("total transaction count %d != %d pending + %d queued", total, pending, queued)
	}
	// Ensure the next nonce to assign is the correct one
	for addr, txs := range pool.pending {
		// Find the last transaction
		var last uint64
		for nonce := range txs.txs.items {
			if last < nonce {
				last = nonce
			}
		}
		if nonce := pool.pendingState.GetNonce(addr); nonce != last+1 {
			return fmt.Errorf("pending nonce mismatch: have %v, want %v", nonce, last+1)
		}
	}
	return nil
}

// validateEvents checks that the correct number of transactions
// were fired on the pool's event feed.
func validateEvents(events chan NewTxsEvent, expTxs int) error {
	var received types.Transactions

	for len(received) < expTxs {
		select {
		case ev := <-events:
			received = append(received, ev.Txs...)
		case <-time.After(time.Second):
			return fmt.Errorf("timed out waiting for event after %d txs", len(received))
		}
	}
	if len(received) > expTxs {
		return fmt.Errorf("expected %d txs but got %d: %v", expTxs, len(received), received)
	}
	select {
	case ev := <-events:
		received = append(received, ev.Txs...)
		return fmt.Errorf("expected %d txs but got more: %v", expTxs, received)

	case <-time.After(50 * time.Millisecond):
		// This branch should be "default", but it's a data race between goroutines,
		// reading the event channel and pushing into it, so better wait a bit ensuring
		// really nothing gets injected.
	}
	return nil
}

func deriveSender(tx *types.Transaction) (common.Address, error) {
	return types.Sender(types.HomesteadSigner{}, tx)
}

type testChain struct {
	*testBlockChain
	address common.Address
	trigger *bool
}

// testChain.State() is used multiple times to reset the pending state.
// when simulate is true it will create a state that indicates
// that tx0 and tx1 are included in the chain.
func (c *testChain) State() (*state.StateDB, error) {
	// delay "state change" by one. The tx pool fetches the
	// state multiple times and by delaying it a bit we simulate
	// a state change between those fetches.
	stdb := c.statedb
	if *c.trigger {
		db := ethdb.NewMemDatabase()
		c.statedb, _ = state.New(common.Hash{}, state.NewDatabase(db))
		// simulate that the new head block included tx0 and tx1
		c.statedb.SetNonce(c.address, 2)
		c.statedb.SetBalance(c.address, new(big.Int).SetUint64(params.Ether))
		*c.trigger = false
	}
	return stdb, nil
}

// This test simulates a scenario where a new block is imported during a
// state reset and tests whether the pending state is in sync with the
// block head event that initiated the resetState().
func TestStateChangeDuringTransactionPoolReset(t *testing.T) {
	t.Parallel()

	var (
		db         = ethdb.NewMemDatabase()
		key, _     = crypto.GenerateKey()
		address    = crypto.PubkeyToAddress(key.PublicKey)
		statedb, _ = state.New(common.Hash{}, state.NewDatabase(db))
		trigger    = false
	)

	// setup pool with 2 transaction in it
	statedb.SetBalance(address, new(big.Int).SetUint64(params.Ether))
	blockchain := &testChain{newTestBlockChain(statedb, 1000000000), address, &trigger}

	tx0 := transaction(0, 100000, key)
	tx1 := transaction(1, 100000, key)

	pool := NewTxPool(testTxPoolConfig, params.TestChainConfig, blockchain)
	defer pool.Stop()

	nonce := pool.State().GetNonce(address)
	if nonce != 0 {
		t.Fatalf("Invalid nonce, want 0, got %d", nonce)
	}

	pool.AddRemotes(types.Transactions{tx0, tx1})

	nonce = pool.State().GetNonce(address)
	if nonce != 2 {
		t.Fatalf("Invalid nonce, want 2, got %d", nonce)
	}

	// trigger state change in the background
	trigger = true

	pool.mu.Lock()
	pool.reset(nil, nil)
	pool.mu.Unlock()

	for addr, txs := range pool.Pending() {
		t.Logf("%0x: %d\n", addr, len(txs))
	}

	nonce = pool.State().GetNonce(address)
	if nonce != 2 {
		t.Fatalf("Invalid nonce, want 2, got %d", nonce)
	}
}

func TestInvalidTransactions(t *testing.T) {
	t.Parallel()

	pool, key := setupTxPool()
	defer pool.Stop()

	tx := transaction(0, 100, key)
	from, _ := deriveSender(tx)

	pool.mu.Lock()
	pool.currentState.AddBalance(from, big.NewInt(1))
	pool.mu.Unlock()
	if err := pool.AddRemote(tx); err != ErrInsufficientFunds {
		t.Error("expected", ErrInsufficientFunds)
	}

	balance := new(big.Int).Add(tx.Value(), new(big.Int).Mul(new(big.Int).SetUint64(tx.Gas()), tx.GasPrice()))
	pool.mu.Lock()
	pool.currentState.AddBalance(from, balance)
	pool.mu.Unlock()
	if err := pool.AddRemote(tx); err != ErrIntrinsicGas {
		t.Error("expected", ErrIntrinsicGas, "got", err)
	}

	pool.mu.Lock()
	pool.currentState.SetNonce(from, 1)
	pool.currentState.AddBalance(from, big.NewInt(0xffffffffffffff))
	pool.mu.Unlock()
	tx = transaction(0, 100000, key)
	if err := pool.AddRemote(tx); err != ErrNonceTooLow {
		t.Error("expected", ErrNonceTooLow)
	}

	tx = transaction(1, 100000, key)
	pool.mu.Lock()
	pool.gasPrice = big.NewInt(1000)
	pool.mu.Unlock()
	if err := pool.AddRemote(tx); err != ErrUnderpriced {
		t.Error("expected", ErrUnderpriced, "got", err)
	}
	if err := pool.AddLocal(tx); err != nil {
		t.Error("expected", nil, "got", err)
	}
}

func TestTransactionQueue(t *testing.T) {
	t.Parallel()

	t.Run("low1", func(t *testing.T) {
		pool, key := setupTxPool()
		defer pool.Stop()

		tx := transaction(0, 100, key)
		from, _ := deriveSender(tx)
		pool.mu.Lock()
		defer pool.mu.Unlock()
		pool.currentState.AddBalance(from, big.NewInt(1000))
		pool.reset(nil, nil)
		if _, err := pool.enqueueTx(tx); err != nil {
			t.Fatal(err)
		}

		pool.promoteExecutables(from)
		if len(pool.pending) != 1 {
			t.Error("expected valid txs to be 1 is", len(pool.pending))
		}

		tx = transaction(1, 100, key)
		from, _ = deriveSender(tx)
		pool.currentState.SetNonce(from, 2)
		if _, err := pool.enqueueTx(tx); err != nil {
			t.Fatal(err)
		}
		pool.promoteExecutables(from)
		if _, ok := pool.pending[from].txs.items[tx.Nonce()]; ok {
			t.Error("expected transaction to be dropped")
		}

		if len(pool.queue) > 0 {
			t.Error("expected transaction queue to be empty. is", len(pool.queue))
		}
	})

	t.Run("low2", func(t *testing.T) {
		pool, key := setupTxPool()
		defer pool.Stop()

		tx1 := transaction(0, 100, key)
		tx2 := transaction(10, 100, key)
		tx3 := transaction(11, 100, key)
		from, _ := deriveSender(tx1)
		pool.mu.Lock()
		defer pool.mu.Unlock()
		pool.currentState.AddBalance(from, big.NewInt(1000))
		pool.reset(nil, nil)

		if _, err := pool.enqueueTx(tx1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.enqueueTx(tx2); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.enqueueTx(tx3); err != nil {
			t.Fatal(err)
		}

		pool.promoteExecutables(from)

		if len(pool.pending) != 1 {
			t.Error("expected tx pool to be 1, got", len(pool.pending))
		}
		if pool.queue[from].Len() != 2 {
			t.Error("expected len(queue) == 2, got", pool.queue[from].Len())
		}
	})

	const gasLimit = 90000
	t.Run("high1", func(t *testing.T) {
		pool, key := setupTxPool()
		defer pool.Stop()

		tx := transaction(0, gasLimit, key)
		from, _ := deriveSender(tx)
		pool.mu.Lock()
		pool.currentState.AddBalance(from, big.NewInt(1000000000000000))
		pool.reset(nil, nil)
		pool.mu.Unlock()
		if err := pool.AddRemote(tx); err != nil {
			t.Fatal(err)
		}

		pool.mu.Lock()
		if len(pool.pending) != 1 {
			t.Error("expected valid txs to be 1 is", len(pool.pending))
		}
		pool.mu.Unlock()

		tx = transaction(1, gasLimit, key)
		from, _ = deriveSender(tx)
		pool.mu.Lock()
		pool.currentState.SetNonce(from, 2)
		pool.mu.Unlock()
		if err := pool.AddRemote(tx); err != ErrNonceTooLow {
			t.Fatalf("expected 'nonce too low' but got %q", err)
		}

		pool.mu.Lock()
		if pending := pool.pending[from]; pending == nil {
			t.Errorf("expected pending txs for %s", from.Hex())
		} else if _, ok := pending.txs.items[tx.Nonce()]; ok {
			t.Error("expected transaction to be dropped:", pending.txs.items)
		}
		if len(pool.queue) > 0 {
			t.Error("expected transaction queue to be empty. is", len(pool.queue))
		}
		pool.mu.Unlock()
	})

	t.Run("high2", func(t *testing.T) {
		pool, key := setupTxPool()
		defer pool.Stop()

		tx1 := transaction(0, gasLimit, key)
		tx2 := transaction(10, gasLimit, key)
		tx3 := transaction(11, gasLimit, key)
		from, _ := deriveSender(tx1)

		pool.mu.Lock()
		pool.currentState.AddBalance(from, big.NewInt(1000000000000000))
		pool.reset(nil, nil)
		pool.mu.Unlock()

		if err := pool.AddRemotes([]*types.Transaction{tx1, tx2, tx3}); err != nil {
			t.Fatal(err)
		}

		pool.mu.Lock()
		if len(pool.pending) != 1 {
			t.Error("expected tx pool to be 1, got", len(pool.pending))
		}
		if queue := pool.queue[from]; queue == nil {
			t.Errorf("expected queued txs for %s", from.Hex())
		} else if queue.Len() != 2 {
			t.Error("expected len(queue) == 2, got", pool.queue[from].Len())
		}
		pool.mu.Unlock()
	})
}

func TestTransactionNegativeValue(t *testing.T) {
	t.Parallel()

	pool, key := setupTxPool()
	defer pool.Stop()

	tx, _ := types.SignTx(types.NewTransaction(0, common.Address{}, big.NewInt(-1), 100, big.NewInt(1), nil), types.HomesteadSigner{}, key)
	from, _ := deriveSender(tx)
	pool.mu.Lock()
	pool.currentState.AddBalance(from, big.NewInt(1))
	pool.mu.Unlock()
	if err := pool.AddRemote(tx); err != ErrNegativeValue {
		t.Error("expected", ErrNegativeValue, "got", err)
	}
}

func TestTransactionChainFork(t *testing.T) {
	t.Parallel()

	pool, key := setupTxPoolState(func(statedb *state.StateDB, key *ecdsa.PrivateKey) {
		addr := crypto.PubkeyToAddress(key.PublicKey)
		statedb.AddBalance(addr, big.NewInt(100000000000000))
	})
	defer pool.Stop()

	tx := transaction(0, 100000, key)
	pool.mu.Lock()
	if _, err := pool.add(tx, false); err != nil {
		t.Error("didn't expect error", err)
	}
	pool.all.mu.Lock()
	pool.removeTx(tx)
	pool.all.mu.Unlock()
	pool.mu.Unlock()

	pool.reset(nil, nil)

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if _, err := pool.add(tx, false); err != nil {
		t.Error("didn't expect error", err)
	}
}

func TestTransactionDoubleNonce(t *testing.T) {
	t.Parallel()

	pool, key := setupTxPoolState(func(statedb *state.StateDB, key *ecdsa.PrivateKey) {
		addr := crypto.PubkeyToAddress(key.PublicKey)
		statedb.AddBalance(addr, big.NewInt(100000000000000))
	})
	defer pool.Stop()

	addr := crypto.PubkeyToAddress(key.PublicKey)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.reset(nil, nil)

	signer := types.HomesteadSigner{}
	tx1, _ := types.SignTx(types.NewTransaction(0, common.Address{}, big.NewInt(100), 100000, big.NewInt(1), nil), signer, key)
	tx2, _ := types.SignTx(types.NewTransaction(0, common.Address{}, big.NewInt(100), 1000000, big.NewInt(2), nil), signer, key)
	tx3, _ := types.SignTx(types.NewTransaction(0, common.Address{}, big.NewInt(100), 1000000, big.NewInt(1), nil), signer, key)

	// Add the first two transaction, ensure higher priced stays only
	if replace, err := pool.add(tx1, false); err != nil || replace {
		t.Errorf("first transaction insert failed (%v) or reported replacement (%v)", err, replace)
	}
	if replace, err := pool.add(tx2, false); err != nil || !replace {
		t.Errorf("second transaction insert failed (%v) or not reported replacement (%v)", err, replace)
	}
	pool.promoteExecutables(addr)

	if pool.pending[addr].Len() != 1 {
		t.Error("expected 1 pending transactions, got", pool.pending[addr].Len())
	}
	if tx := pool.pending[addr].txs.items[0]; tx.Hash() != tx2.Hash() {
		t.Errorf("transaction mismatch: have %x, want %x", tx.Hash(), tx2.Hash())
	}

	// Add the third transaction and ensure it's not saved (smaller price)
	pool.add(tx3, false)
	pool.promoteExecutables(addr)

	if pool.pending[addr].Len() != 1 {
		t.Error("expected 1 pending transactions, got", pool.pending[addr].Len())
	}
	if tx := pool.pending[addr].txs.items[0]; tx.Hash() != tx2.Hash() {
		t.Errorf("transaction mismatch: have %x, want %x", tx.Hash(), tx2.Hash())
	}
	// Ensure the total transaction count is correct
	if total := pool.all.Count(); total != 1 {
		t.Error("expected 1 total transactions, got", total)
	}
}

func TestTransactionMissingNonce(t *testing.T) {
	t.Parallel()

	pool, key := setupTxPool()
	defer pool.Stop()

	addr := crypto.PubkeyToAddress(key.PublicKey)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.currentState.AddBalance(addr, big.NewInt(100000000000000))
	tx := transaction(1, 100000, key)
	if _, err := pool.add(tx, false); err != nil {
		t.Error("didn't expect error", err)
	}
	if len(pool.pending) != 0 {
		t.Error("expected 0 pending transactions, got", len(pool.pending))
	}
	if pool.queue[addr].Len() != 1 {
		t.Error("expected 1 queued transaction, got", pool.queue[addr].Len())
	}
	if total := pool.all.Count(); total != 1 {
		t.Error("expected 1 total transactions, got", total)
	}
}

func TestTransactionNonceRecovery(t *testing.T) {
	t.Parallel()

	const n = 10
	pool, key := setupTxPool()
	defer pool.Stop()

	addr := crypto.PubkeyToAddress(key.PublicKey)
	pool.mu.Lock()
	pool.currentState.SetNonce(addr, n)
	pool.currentState.AddBalance(addr, big.NewInt(100000000000000))
	pool.reset(nil, nil)
	pool.mu.Unlock()

	tx := transaction(n, 100000, key)
	if err := pool.AddRemote(tx); err != nil {
		t.Error(err)
	}
	// simulate some weird re-order of transactions and missing nonce(s)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.currentState.SetNonce(addr, n-1)
	pool.reset(nil, nil)
	if got := pool.pendingState.GetNonce(addr); got != n-1 {
		t.Errorf("expected nonce to be %d, got %d", n-1, got)
	}
}

// Tests that if an account runs out of funds, any pending and queued transactions
// are dropped.
func TestTransactionDropping(t *testing.T) {
	t.Parallel()

	// Create a test account and fund it
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.currentState.AddBalance(account, big.NewInt(1000))

	// Add some pending and some queued transactions
	var (
		tx0  = transaction(0, 100, key)
		tx1  = transaction(1, 200, key)
		tx2  = transaction(2, 300, key)
		tx10 = transaction(10, 100, key)
		tx11 = transaction(11, 200, key)
		tx12 = transaction(12, 300, key)
	)
	pool.promoteTx(account, tx0.Hash(), tx0)
	pool.promoteTx(account, tx1.Hash(), tx1)
	pool.promoteTx(account, tx2.Hash(), tx2)
	pool.enqueueTx(tx10)
	pool.enqueueTx(tx11)
	pool.enqueueTx(tx12)

	// Check that pre and post validations leave the pool as is
	if pool.pending[account].Len() != 3 {
		t.Errorf("pending transaction mismatch: have %d, want %d", pool.pending[account].Len(), 3)
	}
	if pool.queue[account].Len() != 3 {
		t.Errorf("queued transaction mismatch: have %d, want %d", pool.queue[account].Len(), 3)
	}
	if total := pool.all.Count(); total != 6 {
		t.Errorf("total transaction mismatch: have %d, want %d", total, 6)
	}
	pool.reset(nil, nil)
	if pool.pending[account].Len() != 3 {
		t.Errorf("pending transaction mismatch: have %d, want %d", pool.pending[account].Len(), 3)
	}
	if pool.queue[account].Len() != 3 {
		t.Errorf("queued transaction mismatch: have %d, want %d", pool.queue[account].Len(), 3)
	}
	if total := pool.all.Count(); total != 6 {
		t.Errorf("total transaction mismatch: have %d, want %d", total, 6)
	}
	// Reduce the balance of the account, and check that invalidated transactions are dropped
	pool.currentState.AddBalance(account, big.NewInt(-650))
	pool.reset(nil, nil)

	if _, ok := pool.pending[account].txs.items[tx0.Nonce()]; !ok {
		t.Errorf("funded pending transaction missing: %v", tx0)
	}
	if _, ok := pool.pending[account].txs.items[tx1.Nonce()]; !ok {
		t.Errorf("funded pending transaction missing: %v", tx0)
	}
	if _, ok := pool.pending[account].txs.items[tx2.Nonce()]; ok {
		t.Errorf("out-of-fund pending transaction present: %v", tx1)
	}
	if _, ok := pool.queue[account].txs.items[tx10.Nonce()]; !ok {
		t.Errorf("funded queued transaction missing: %v", tx10)
	}
	if _, ok := pool.queue[account].txs.items[tx11.Nonce()]; !ok {
		t.Errorf("funded queued transaction missing: %v", tx10)
	}
	if _, ok := pool.queue[account].txs.items[tx12.Nonce()]; ok {
		t.Errorf("out-of-fund queued transaction present: %v", tx11)
	}
	if total := pool.all.Count(); total != 4 {
		t.Errorf("total transaction mismatch: have %d, want %d", total, 4)
	}
	// Reduce the block gas limit, check that invalidated transactions are dropped
	pool.chain.(*testBlockChain).setGasLimit(100)
	pool.reset(nil, nil)

	if _, ok := pool.pending[account].txs.items[tx0.Nonce()]; !ok {
		t.Errorf("funded pending transaction missing: %v", tx0)
	}
	if _, ok := pool.pending[account].txs.items[tx1.Nonce()]; ok {
		t.Errorf("over-gased pending transaction present: %v", tx1)
	}
	if _, ok := pool.queue[account].txs.items[tx10.Nonce()]; !ok {
		t.Errorf("funded queued transaction missing: %v", tx10)
	}
	if _, ok := pool.queue[account].txs.items[tx11.Nonce()]; ok {
		t.Errorf("over-gased queued transaction present: %v", tx11)
	}
	if pool.all.Count() != 2 {
		t.Errorf("total transaction mismatch: have %d, want %d", pool.all.Count(), 2)
	}
}

// Tests that if a transaction is dropped from the current pending pool (e.g. out
// of fund), all consecutive (still valid, but not executable) transactions are
// postponed back into the future queue to prevent broadcasting them.
func TestTransactionPostponing(t *testing.T) {
	t.Parallel()

	// Create a test account and fund it
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.currentState.AddBalance(account, big.NewInt(1000))

	// Add a batch consecutive pending transactions for validation
	txns := []*types.Transaction{}
	for i := 0; i < 100; i++ {
		var tx *types.Transaction
		if i%2 == 0 {
			tx = transaction(uint64(i), 100, key)
		} else {
			tx = transaction(uint64(i), 500, key)
		}
		pool.promoteTx(account, tx.Hash(), tx)
		txns = append(txns, tx)
	}
	// Check that pre and post validations leave the pool as is
	if pool.pending[account].Len() != len(txns) {
		t.Errorf("pending transaction mismatch: have %d, want %d", pool.pending[account].Len(), len(txns))
	}
	if len(pool.queue) != 0 {
		t.Errorf("queued transaction mismatch: have %d, want %d", pool.queue[account].Len(), 0)
	}
	if total := pool.all.Count(); total != len(txns) {
		t.Errorf("total transaction mismatch: have %d, want %d", total, len(txns))
	}
	pool.reset(nil, nil)
	if pool.pending[account].Len() != len(txns) {
		t.Errorf("pending transaction mismatch: have %d, want %d", pool.pending[account].Len(), len(txns))
	}
	if len(pool.queue) != 0 {
		t.Errorf("queued transaction mismatch: have %d, want %d", pool.queue[account].Len(), 0)
	}
	if total := pool.all.Count(); total != len(txns) {
		t.Errorf("total transaction mismatch: have %d, want %d", total, len(txns))
	}
	// Reduce the balance of the account, and check that transactions are reorganised
	pool.currentState.AddBalance(account, big.NewInt(-750))
	pool.reset(nil, nil)

	if _, ok := pool.pending[account].txs.items[txns[0].Nonce()]; !ok {
		t.Errorf("tx %d: valid and funded transaction missing from pending pool: %v", 0, txns[0])
	}
	if _, ok := pool.queue[account].txs.items[txns[0].Nonce()]; ok {
		t.Errorf("tx %d: valid and funded transaction present in future queue: %v", 0, txns[0])
	}
	for i, tx := range txns[1:] {
		if i%2 == 1 {
			if _, ok := pool.pending[account].txs.items[tx.Nonce()]; ok {
				t.Errorf("tx %d: valid but future transaction present in pending pool: %v", i+1, tx)
			}
			if _, ok := pool.queue[account].txs.items[tx.Nonce()]; !ok {
				t.Errorf("tx %d: valid but future transaction missing from future queue: %v", i+1, tx)
			}
		} else {
			if _, ok := pool.pending[account].txs.items[tx.Nonce()]; ok {
				t.Errorf("tx %d: out-of-fund transaction present in pending pool: %v", i+1, tx)
			}
			if _, ok := pool.queue[account].txs.items[tx.Nonce()]; ok {
				t.Errorf("tx %d: out-of-fund transaction present in future queue: %v", i+1, tx)
			}
		}
	}
	if total := pool.all.Count(); total != len(txns)/2 {
		t.Errorf("total transaction mismatch: have %d, want %d", total, len(txns)/2)
	}
}

// Tests that if the transaction pool has both executable and non-executable
// transactions from an origin account, filling the nonce gap moves all queued
// ones into the pending pool.
func TestTransactionGapFilling(t *testing.T) {
	t.Parallel()

	// Create a test account and fund it
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	pool.currentState.AddBalance(account, big.NewInt(1000000))

	// Keep track of transaction events to ensure all executables get announced
	events := make(chan NewTxsEvent, testTxPoolConfig.AccountQueue+5)
	pool.SubscribeNewTxsEvent(events, "test")
	pool.mu.Unlock()
	defer pool.UnsubscribeNewTxsEvent(events)

	// Create a pending and a queued transaction with a nonce-gap in between
	if err := pool.AddRemote(transaction(0, 100000, key)); err != nil {
		t.Fatalf("failed to add pending transaction: %v", err)
	}
	if err := pool.AddRemote(transaction(2, 100000, key)); err != nil {
		t.Fatalf("failed to add queued transaction: %v", err)
	}
	pending, queued := pool.Stats()
	if pending != 1 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 1)
	}
	if queued != 1 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 1)
	}
	if err := validateEvents(events, 1); err != nil {
		t.Fatalf("original event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Fill the nonce gap and ensure all transactions become pending
	if err := pool.AddRemote(transaction(1, 100000, key)); err != nil {
		t.Fatalf("failed to add gapped transaction: %v", err)
	}
	pending, queued = pool.Stats()
	if pending != 3 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 3)
	}
	if queued != 0 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 0)
	}
	if err := validateEvents(events, 2); err != nil {
		t.Fatalf("gap-filling event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that if the transaction count belonging to a single account goes above
// some threshold, the higher transactions are dropped to prevent DOS attacks.
func TestTransactionQueueAccountLimiting(t *testing.T) {
	t.Parallel()

	// Create a test account and fund it
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	pool.currentState.AddBalance(account, big.NewInt(1000000))
	pool.mu.Unlock()

	// Keep queuing up transactions and make sure all above a limit are dropped
	for i := uint64(1); i <= testTxPoolConfig.AccountQueue+5; i++ {
		if err := pool.AddRemote(transaction(i, 100000, key)); err != nil {
			t.Fatalf("tx %d: failed to add transaction: %v", i, err)
		}
		pool.mu.Lock()
		if len(pool.pending) != 0 {
			t.Errorf("tx %d: pending pool size mismatch: have %d, want %d", i, len(pool.pending), 0)
		}
		if i <= testTxPoolConfig.AccountQueue {
			if pool.queue[account].Len() != int(i) {
				t.Errorf("tx %d: queue size mismatch: have %d, want %d", i, pool.queue[account].Len(), i)
			}
		} else {
			if pool.queue[account].Len() != int(testTxPoolConfig.AccountQueue) {
				t.Errorf("tx %d: queue limit mismatch: have %d, want %d", i, pool.queue[account].Len(), testTxPoolConfig.AccountQueue)
			}
		}
		pool.mu.Unlock()
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if total := pool.all.Count(); total != int(testTxPoolConfig.AccountQueue) {
		t.Errorf("total transaction mismatch: have %d, want %d", total, testTxPoolConfig.AccountQueue)
	}
}

// Tests that if the transaction count belonging to multiple accounts go above
// some threshold, the higher transactions are dropped to prevent DOS attacks.
//
// This logic should not hold for local transactions, unless the local tracking
// mechanism is disabled.
func TestTransactionQueueGlobalLimiting(t *testing.T) {
	testTransactionQueueGlobalLimiting(t, false)
}
func TestTransactionQueueGlobalLimitingNoLocals(t *testing.T) {
	testTransactionQueueGlobalLimiting(t, true)
}

func testTransactionQueueGlobalLimiting(t *testing.T, nolocals bool) {
	t.Parallel()

	// Create the pool to test the limit enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.NoLocals = nolocals
	config.AccountQueue = 5
	config.GlobalQueue = config.AccountQueue*3 - 1 // reduce the queue limits to shorten test time (-1 to make it non divisible)

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	pool.mu.Lock()
	// Create a number of test accounts and fund them (last one will be the local)
	keys := make([]*ecdsa.PrivateKey, 5)
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	pool.mu.Unlock()
	local := keys[len(keys)-1]

	// Generate and queue a batch of transactions
	nonces := make(map[common.Address]uint64)

	txs := make(types.Transactions, 0, 3*config.GlobalQueue)
	for len(txs) < cap(txs) {
		key := keys[rand.Intn(len(keys)-1)] // skip adding transactions with the local account
		addr := crypto.PubkeyToAddress(key.PublicKey)

		txs = append(txs, transaction(nonces[addr]+1, 100000, key))
		nonces[addr]++
	}
	// Import the batch and verify that limits have been enforced
	if errs := pool.AddRemotes(txs); len(errs) > 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}

	pool.mu.Lock()
	queued := 0
	for addr, list := range pool.queue {
		if list.Len() > int(config.AccountQueue) {
			t.Errorf("addr %x: queued accounts overflown allowance: %d > %d", addr, list.Len(), config.AccountQueue)
		}
		queued += list.Len()
	}
	pool.mu.Unlock()
	if queued > int(config.GlobalQueue) {
		t.Fatalf("queued transactions overflow allowance: %d > %d", queued, config.GlobalQueue)
	}
	// Generate a batch of transactions from the local account and import them
	txs = txs[:0]
	for i := uint64(0); i < 3*config.GlobalQueue; i++ {
		txs = append(txs, transaction(i+1, 100000, local))
	}
	if errs := pool.AddLocals(txs); len(errs) > 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}

	// If locals are disabled, the previous eviction algorithm should apply here too
	if nolocals {
		queued := 0
		pool.mu.Lock()
		for addr, list := range pool.queue {
			if list.Len() > int(config.AccountQueue) {
				t.Errorf("addr %x: queued accounts overflown allowance: %d > %d", addr, list.Len(), config.AccountQueue)
			}
			queued += list.Len()
		}
		pool.mu.Unlock()
		if queued > int(config.GlobalQueue) {
			t.Fatalf("queued transactions overflow allowance: %d > %d", queued, config.GlobalQueue)
		}
	} else {
		pool.mu.Lock()
		// Local exemptions are enabled, make sure the local account owned the queue
		if len(pool.queue) != 1 {
			t.Errorf("multiple accounts in queue: have %v, want %v", len(pool.queue), 1)
		}
		// Also ensure no local transactions are ever dropped, even if above global limits
		if queued := pool.queue[crypto.PubkeyToAddress(local.PublicKey)].Len(); uint64(queued) != 3*config.GlobalQueue {
			t.Fatalf("local account queued transaction count mismatch: have %v, want %v", queued, 3*config.GlobalQueue)
		}
		pool.mu.Unlock()
	}
}

// Tests that if an account remains idle for a prolonged amount of time, any
// non-executable transactions queued up are dropped to prevent wasting resources
// on shuffling them around.
//
// This logic should not hold for local transactions, unless the local tracking
// mechanism is disabled.
func TestTransactionQueueTimeLimiting(t *testing.T) { testTransactionQueueTimeLimiting(t, false) }
func TestTransactionQueueTimeLimitingNoLocals(t *testing.T) {
	testTransactionQueueTimeLimiting(t, true)
}

func testTransactionQueueTimeLimiting(t *testing.T, nolocals bool) {
	// Reduce the eviction interval to a testable amount
	defer func(old time.Duration) { evictionInterval = old }(evictionInterval)
	evictionInterval = time.Second

	// Create the pool to test the non-expiration enforcement
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.Lifetime = time.Second
	config.NoLocals = nolocals

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Create two test accounts to ensure remotes expire but locals do not
	local, _ := crypto.GenerateKey()
	remote, _ := crypto.GenerateKey()

	pool.mu.Lock()
	pool.currentState.AddBalance(crypto.PubkeyToAddress(local.PublicKey), big.NewInt(1000000000))
	pool.currentState.AddBalance(crypto.PubkeyToAddress(remote.PublicKey), big.NewInt(1000000000))
	pool.mu.Unlock()

	// Add the two transactions and ensure they both are queued up
	if err := pool.AddLocal(pricedTransaction(1, 100000, big.NewInt(1), local)); err != nil {
		t.Fatalf("failed to add local transaction: %v", err)
	}
	if err := pool.AddRemote(pricedTransaction(1, 100000, big.NewInt(1), remote)); err != nil {
		t.Fatalf("failed to add remote transaction: %v", err)
	}
	pending, queued := pool.Stats()
	if pending != 0 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 0)
	}
	if queued != 2 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 2)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Wait a bit for eviction to run and clean up any leftovers, and ensure only the local remains
	time.Sleep(10 * config.Lifetime)

	pending, queued = pool.Stats()
	if pending != 0 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 0)
	}
	if nolocals {
		if queued != 0 {
			t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 0)
		}
	} else {
		if queued != 1 {
			t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 1)
		}
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that even if the transaction count belonging to a single account goes
// above some threshold, as long as the transactions are executable, they are
// accepted.
func TestTransactionPendingLimiting(t *testing.T) {
	t.Parallel()

	// Create a test account and fund it
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	pool.currentState.AddBalance(account, big.NewInt(1000000))

	// Keep track of transaction events to ensure all executables get announced
	events := make(chan NewTxsEvent, testTxPoolConfig.AccountQueue+5)
	pool.SubscribeNewTxsEvent(events, "test")
	pool.mu.Unlock()
	defer pool.UnsubscribeNewTxsEvent(events)

	// Keep queuing up transactions and make sure all above a limit are dropped
	for i := uint64(0); i < testTxPoolConfig.AccountQueue+5; i++ {
		if err := pool.AddRemote(transaction(i, 100000, key)); err != nil {
			t.Fatalf("tx %d: failed to add transaction: %v", i, err)
		}
		pool.mu.Lock()
		if pool.pending[account].Len() != int(i)+1 {
			t.Errorf("tx %d: pending pool size mismatch: have %d, want %d", i, pool.pending[account].Len(), i+1)
		}
		if len(pool.queue) != 0 {
			t.Errorf("tx %d: queue size mismatch: have %d, want %d", i, pool.queue[account].Len(), 0)
		}
		pool.mu.Unlock()
	}
	pool.mu.Lock()
	if total := pool.all.Count(); total != int(testTxPoolConfig.AccountQueue+5) {
		t.Errorf("total transaction mismatch: have %d, want %d", total, testTxPoolConfig.AccountQueue+5)
	}
	pool.mu.Unlock()
	if err := validateEvents(events, int(testTxPoolConfig.AccountQueue+5)); err != nil {
		t.Fatalf("event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that the transaction limits are enforced the same way irrelevant whether
// the transactions are added one by one or in batches.
func TestTransactionQueueLimitingEquivalency(t *testing.T) { testTransactionLimitingEquivalency(t, 1) }
func TestTransactionPendingLimitingEquivalency(t *testing.T) {
	testTransactionLimitingEquivalency(t, 0)
}

func testTransactionLimitingEquivalency(t *testing.T, origin uint64) {
	t.Parallel()

	// Add a batch of transactions to a pool one by one
	pool1, key1 := setupTxPool()
	defer pool1.Stop()

	account1, _ := deriveSender(transaction(0, 0, key1))
	pool1.mu.Lock()
	pool1.currentState.AddBalance(account1, big.NewInt(1000000))
	pool1.mu.Unlock()

	for i := uint64(0); i < testTxPoolConfig.AccountQueue+5; i++ {
		if err := pool1.AddRemote(transaction(origin+i, 100000, key1)); err != nil {
			t.Fatalf("tx %d: failed to add transaction: %v", i, err)
		}
	}
	// Add a batch of transactions to a pool in one big batch
	pool2, key2 := setupTxPool()
	defer pool2.Stop()

	account2, _ := deriveSender(transaction(0, 0, key2))
	pool2.mu.Lock()
	pool2.currentState.AddBalance(account2, big.NewInt(1000000))
	pool2.mu.Unlock()

	txns := []*types.Transaction{}
	for i := uint64(0); i < testTxPoolConfig.AccountQueue+5; i++ {
		txns = append(txns, transaction(origin+i, 100000, key2))
	}
	pool2.AddRemotes(txns)

	pool1.mu.Lock()
	pool2.mu.Lock()
	// Ensure the batch optimization honors the same pool mechanics
	if len(pool1.pending) != len(pool2.pending) {
		t.Errorf("pending transaction count mismatch: one-by-one algo: %d, batch algo: %d", len(pool1.pending), len(pool2.pending))
	}
	if len(pool1.queue) != len(pool2.queue) {
		t.Errorf("queued transaction count mismatch: one-by-one algo: %d, batch algo: %d", len(pool1.queue), len(pool2.queue))
	}
	if total1, total2 := pool1.all.Count(), pool2.all.Count(); total1 != total2 {
		t.Errorf("total transaction count mismatch: one-by-one algo %d, batch algo %d", total1, total2)
	}
	pool1.mu.Unlock()
	pool2.mu.Unlock()

	if err := validateTxPoolInternals(pool1); err != nil {
		t.Errorf("pool 1 internal state corrupted: %v", err)
	}
	if err := validateTxPoolInternals(pool2); err != nil {
		t.Errorf("pool 2 internal state corrupted: %v", err)
	}
}

// Tests that if the transaction count belonging to multiple accounts go above
// some hard threshold, the higher transactions are dropped to prevent DOS
// attacks.
func TestTransactionPendingGlobalLimiting(t *testing.T) {
	t.Parallel()

	// Create the pool to test the limit enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.GlobalSlots = config.AccountSlots * 10

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Create a number of test accounts and fund them
	pool.mu.Lock()
	keys := make([]*ecdsa.PrivateKey, 5)
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	pool.mu.Unlock()
	// Generate and queue a batch of transactions
	nonces := make(map[common.Address]uint64)

	txs := types.Transactions{}
	for _, key := range keys {
		addr := crypto.PubkeyToAddress(key.PublicKey)
		for j := 0; j < int(config.GlobalSlots)/len(keys)*2; j++ {
			txs = append(txs, transaction(nonces[addr], 100000, key))
			nonces[addr]++
		}
	}
	// Import the batch and verify that limits have been enforced
	pool.AddRemotes(txs)

	pending := 0
	pool.mu.Lock()
	for _, list := range pool.pending {
		pending += list.Len()
	}
	pool.mu.Unlock()
	if pending > int(config.GlobalSlots) {
		t.Fatalf("total pending transactions overflow allowance: %d > %d", pending, config.GlobalSlots)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that if transactions start being capped, transactions are also removed from 'all'
func TestTransactionCapClearsFromAll(t *testing.T) {
	t.Parallel()

	// Create the pool to test the limit enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.AccountSlots = 2
	config.AccountQueue = 2
	config.GlobalSlots = 8

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Create a number of test accounts and fund them
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	pool.mu.Lock()
	pool.currentState.AddBalance(addr, big.NewInt(1000000))
	pool.mu.Unlock()

	txs := types.Transactions{}
	for j := 0; j < int(config.GlobalSlots)*2; j++ {
		txs = append(txs, transaction(uint64(j), 100000, key))
	}
	// Import the batch and verify that limits have been enforced
	pool.AddRemotes(txs)
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that if the transaction count belonging to multiple accounts go above
// some hard threshold, if they are under the minimum guaranteed slot count then
// the transactions are still kept.
func TestTransactionPendingMinimumAllowance(t *testing.T) {
	t.Parallel()

	// Create the pool to test the limit enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.GlobalSlots = config.AccountSlots

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Create a number of test accounts and fund them
	pool.mu.Lock()
	keys := make([]*ecdsa.PrivateKey, 5)
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	pool.mu.Unlock()
	// Generate and queue a batch of transactions
	nonces := make(map[common.Address]uint64)

	txs := types.Transactions{}
	for _, key := range keys {
		addr := crypto.PubkeyToAddress(key.PublicKey)
		for j := 0; j < int(config.AccountSlots)*2; j++ {
			txs = append(txs, transaction(nonces[addr], 100000, key))
			nonces[addr]++
		}
	}
	// Import the batch and verify that limits have been enforced
	pool.AddRemotes(txs)

	pool.mu.Lock()
	for addr, list := range pool.pending {
		if list.Len() != int(config.AccountSlots) {
			t.Errorf("addr %x: total pending transactions mismatch: have %d, want %d", addr, list.Len(), config.AccountSlots)
		}
	}
	pool.mu.Unlock()
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that setting the transaction pool gas price to a higher value correctly
// discards everything cheaper than that and moves any gapped transactions back
// from the pending pool to the queue.
//
// Note, local transactions are never allowed to be dropped.
func TestTransactionPoolRepricing(t *testing.T) {
	t.Parallel()

	// Create the pool to test the pricing enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	pool := NewTxPool(testTxPoolConfig, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Keep track of transaction events to ensure all executables get announced
	events := make(chan NewTxsEvent, 32)
	pool.mu.Lock()
	pool.SubscribeNewTxsEvent(events, "test")
	pool.mu.Unlock()
	defer pool.UnsubscribeNewTxsEvent(events)

	// Create a number of test accounts and fund them
	pool.mu.Lock()
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	pool.mu.Unlock()
	// Generate and queue a batch of transactions, both pending and queued
	txs := types.Transactions{}

	txs = append(txs, pricedTransaction(0, 100000, big.NewInt(2), keys[0]))
	txs = append(txs, pricedTransaction(1, 100000, big.NewInt(1), keys[0]))
	txs = append(txs, pricedTransaction(2, 100000, big.NewInt(2), keys[0]))

	txs = append(txs, pricedTransaction(1, 100000, big.NewInt(2), keys[1]))
	txs = append(txs, pricedTransaction(2, 100000, big.NewInt(1), keys[1]))
	txs = append(txs, pricedTransaction(3, 100000, big.NewInt(2), keys[1]))

	ltx := pricedTransaction(0, 100000, big.NewInt(1), keys[2])

	// Import the batch and that both pending and queued transactions match up
	pool.AddRemotes(txs)
	pool.AddLocal(ltx)

	pending, queued := pool.Stats()
	if pending != 4 || queued != 3 {
		t.Errorf("transactions mismatched: want pending 4 queued 3, have pending %d queued %d", pending, queued)
	}
	if err := validateEvents(events, 4); err != nil {
		t.Fatalf("original event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Reprice the pool and check that underpriced transactions get dropped
	pool.SetGasPrice(big.NewInt(2))

	pending, queued = pool.Stats()
	if pending != 2 || queued != 3 {
		t.Fatalf("transactions mismatched: want pending 2 queued 3, have pending %d queued %d", pending, queued)
	}
	if err := validateEvents(events, 0); err != nil {
		t.Fatalf("reprice event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Check that we can't add the old transactions back
	if err := pool.AddRemote(pricedTransaction(1, 100000, big.NewInt(1), keys[0])); err != ErrUnderpriced {
		t.Fatalf("adding underpriced pending transaction error mismatch: have %v, want %v", err, ErrUnderpriced)
	}
	if err := pool.AddRemote(pricedTransaction(2, 100000, big.NewInt(1), keys[1])); err != ErrUnderpriced {
		t.Fatalf("adding underpriced queued transaction error mismatch: have %v, want %v", err, ErrUnderpriced)
	}
	if err := validateEvents(events, 0); err != nil {
		t.Fatalf("post-reprice event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// However we can add local underpriced transactions
	tx := pricedTransaction(1, 100000, big.NewInt(1), keys[2])
	if err := pool.AddLocal(tx); err != nil {
		t.Fatalf("failed to add underpriced local transaction: %v", err)
	}
	if pending, _ = pool.Stats(); pending != 3 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 3)
	}
	if err := validateEvents(events, 1); err != nil {
		t.Fatalf("post-reprice local event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that setting the transaction pool gas price to a higher value does not
// remove local transactions.
func TestTransactionPoolRepricingKeepsLocals(t *testing.T) {
	t.Parallel()

	// Create the pool to test the pricing enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	pool := NewTxPool(testTxPoolConfig, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Create a number of test accounts and fund them
	pool.mu.Lock()
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000*1000000))
	}
	pool.mu.Unlock()
	// Create transaction (both pending and queued) with a linearly growing gasprice
	for i := uint64(0); i < 500; i++ {
		// Add pending
		p_tx := pricedTransaction(i, 100000, big.NewInt(int64(i)), keys[2])
		if err := pool.AddLocal(p_tx); err != nil {
			t.Fatal(err)
		}
		// Add queued
		q_tx := pricedTransaction(i+501, 100000, big.NewInt(int64(i)), keys[2])
		if err := pool.AddLocal(q_tx); err != nil {
			t.Fatal(err)
		}
	}
	pending, queued := pool.Stats()
	expPending, expQueued := 500, 500
	validate := func() {
		pending, queued = pool.Stats()
		if pending != expPending {
			t.Fatalf("pending transactions mismatched: have %d, want %d", pending, expPending)
		}
		if queued != expQueued {
			t.Fatalf("queued transactions mismatched: have %d, want %d", queued, expQueued)
		}

		if err := validateTxPoolInternals(pool); err != nil {
			t.Fatalf("pool internal state corrupted: %v", err)
		}
	}
	validate()

	// Reprice the pool and check that nothing is dropped
	pool.SetGasPrice(big.NewInt(2))
	validate()

	pool.SetGasPrice(big.NewInt(2))
	pool.SetGasPrice(big.NewInt(4))
	pool.SetGasPrice(big.NewInt(8))
	pool.SetGasPrice(big.NewInt(100))
	validate()
}

// Tests that when the pool reaches its global transaction limit, underpriced
// transactions are gradually shifted out for more expensive ones and any gapped
// pending transactions are moved into the queue.
//
// Note, local transactions are never allowed to be dropped.
func TestTransactionPoolUnderpricing(t *testing.T) {
	t.Parallel()

	// Create the pool to test the pricing enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.GlobalSlots = 2
	config.AccountSlots = 2
	config.GlobalQueue = 2
	config.AccountQueue = 2

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Keep track of transaction events to ensure all executables get announced
	events := make(chan NewTxsEvent, 32)
	pool.mu.Lock()
	pool.SubscribeNewTxsEvent(events, "test")
	pool.mu.Unlock()
	defer pool.UnsubscribeNewTxsEvent(events)

	// Create a number of test accounts and fund them
	keys := make([]*ecdsa.PrivateKey, 3)
	pool.mu.Lock()
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	pool.mu.Unlock()
	// Generate and queue a batch of transactions, both pending and queued
	txs := types.Transactions{}

	txs = append(txs, pricedTransaction(0, 100000, big.NewInt(1), keys[0]))
	txs = append(txs, pricedTransaction(1, 100000, big.NewInt(2), keys[0]))

	txs = append(txs, pricedTransaction(1, 100000, big.NewInt(1), keys[1]))

	ltx := pricedTransaction(0, 100000, big.NewInt(1), keys[2])

	// Import the batch and that both pending and queued transactions match up
	pool.AddRemotes(txs)
	pool.AddLocal(ltx)

	pending, queued := pool.Stats()
	if pending != 3 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 3)
	}
	if queued != 1 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 1)
	}
	if err := validateEvents(events, 3); err != nil {
		t.Fatalf("original event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Ensure that adding a transaction into full pool fails.
	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(1), keys[1])); err != ErrPoolLimit {
		t.Fatalf("transaction pool limit error mismatch: have %v, want %v", err, ErrPoolLimit)
	}
	// Ensure that adding local transactions are rejected too.
	tx := pricedTransaction(1, 100000, big.NewInt(0), keys[2])
	if err := pool.AddLocal(tx); err != ErrPoolLimit {
		t.Fatalf("transaction pool limit error mismatch: have %v, want %v", err, ErrPoolLimit)
	}
	pending, queued = pool.Stats()
	if pending != 3 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 3)
	}
	if queued != 1 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 1)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that more expensive transactions push out cheap ones from the pool, but
// without producing instability by creating gaps that start jumping transactions
// back and forth between queued/pending.
func TestTransactionPoolStableUnderpricing(t *testing.T) {
	t.Parallel()

	// Create the pool to test the pricing enforcement with
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(ethdb.NewMemDatabase()))
	blockchain := &testBlockChain{statedb: statedb, gasLimit: 1000000}

	config := testTxPoolConfig
	config.GlobalSlots = 128
	config.AccountSlots = config.GlobalSlots / 3
	config.GlobalQueue = 0
	config.AccountQueue = 0

	pool := NewTxPool(config, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Keep track of transaction events to ensure all executables get announced
	events := make(chan NewTxsEvent, 1024)
	pool.SubscribeNewTxsEvent(events, "test")
	defer pool.UnsubscribeNewTxsEvent(events)

	// Create a number of test accounts and fund them
	keys := make([]*ecdsa.PrivateKey, 2)
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	// Fill up the entire queue with the same transaction price points
	txs := types.Transactions{}
	for i := uint64(0); i < config.GlobalSlots; i++ {
		txs = append(txs, pricedTransaction(i, 100000, big.NewInt(1), keys[0]))
	}
	if errs := pool.AddRemotes(txs); len(errs) > 0 {
		for _, err := range errs {
			t.Error(err)
		}
		t.FailNow()
	}

	pending, queued := pool.Stats()
	if pending != int(config.GlobalSlots) {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, config.GlobalSlots)
	}
	if queued != 0 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 0)
	}
	if err := validateEvents(events, int(config.GlobalSlots)); err != nil {
		t.Fatalf("original event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Ensure that adding high priced transactions drops a cheap, but doesn't produce a gap
	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(3), keys[1])); err != nil {
		t.Fatalf("failed to add well priced transaction: %v", err)
	}
	pending, queued = pool.Stats()
	if pending != int(config.GlobalSlots) {
		t.Errorf("pending transactions mismatched: have %d, want %d", pending, config.GlobalSlots)
	}
	if queued != 0 {
		t.Errorf("queued transactions mismatched: have %d, want %d", queued, 0)
	}
	if err := validateEvents(events, 1); err != nil {
		t.Errorf("additional event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Errorf("pool internal state corrupted: %v", err)
	}
}

// Tests that the pool rejects replacement transactions that don't meet the minimum
// price bump required.
func TestTransactionReplacement(t *testing.T) {
	t.Parallel()

	// Create the pool to test the pricing enforcement with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	pool := NewTxPool(testTxPoolConfig, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Keep track of transaction events to ensure all executables get announced
	events := make(chan NewTxsEvent, 32)
	pool.mu.Lock()
	pool.SubscribeNewTxsEvent(events, "test")
	defer pool.UnsubscribeNewTxsEvent(events)

	// Create a test account to add transactions with
	key, _ := crypto.GenerateKey()
	pool.currentState.AddBalance(crypto.PubkeyToAddress(key.PublicKey), big.NewInt(1000000000))
	pool.mu.Unlock()

	// Add pending transactions, ensuring the minimum price bump is enforced for replacement (for ultra low prices too)
	price := int64(100)
	threshold := (price * (100 + int64(testTxPoolConfig.PriceBump))) / 100

	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(1), key)); err != nil {
		t.Fatalf("failed to add original cheap pending transaction: %v", err)
	}
	if err := pool.AddRemote(pricedTransaction(0, 100001, big.NewInt(1), key)); err != ErrReplaceUnderpriced {
		t.Fatalf("original cheap pending transaction replacement error mismatch: have %v, want %v", err, ErrReplaceUnderpriced)
	}
	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(2), key)); err != nil {
		t.Fatalf("failed to replace original cheap pending transaction: %v", err)
	}
	for len(pool.txFeedBuf) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if err := validateEvents(events, 2); err != nil {
		t.Fatalf("cheap replacement event firing failed: %v", err)
	}

	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(price), key)); err != nil {
		t.Fatalf("failed to add original proper pending transaction: %v", err)
	}
	if err := pool.AddRemote(pricedTransaction(0, 100001, big.NewInt(threshold-1), key)); err != ErrReplaceUnderpriced {
		t.Fatalf("original proper pending transaction replacement error mismatch: have %v, want %v", err, ErrReplaceUnderpriced)
	}
	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(threshold), key)); err != nil {
		t.Fatalf("failed to replace original proper pending transaction: %v", err)
	}
	for len(pool.txFeedBuf) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if err := validateEvents(events, 2); err != nil {
		t.Fatalf("proper replacement event firing failed: %v", err)
	}
	// Add queued transactions, ensuring the minimum price bump is enforced for replacement (for ultra low prices too)
	if err := pool.AddRemote(pricedTransaction(2, 100000, big.NewInt(1), key)); err != nil {
		t.Fatalf("failed to add original cheap queued transaction: %v", err)
	}
	if err := pool.AddRemote(pricedTransaction(2, 100001, big.NewInt(1), key)); err != ErrReplaceUnderpriced {
		t.Fatalf("original cheap queued transaction replacement error mismatch: have %v, want %v", err, ErrReplaceUnderpriced)
	}
	if err := pool.AddRemote(pricedTransaction(2, 100000, big.NewInt(2), key)); err != nil {
		t.Fatalf("failed to replace original cheap queued transaction: %v", err)
	}

	if err := pool.AddRemote(pricedTransaction(2, 100000, big.NewInt(price), key)); err != nil {
		t.Fatalf("failed to add original proper queued transaction: %v", err)
	}
	if err := pool.AddRemote(pricedTransaction(2, 100001, big.NewInt(threshold-1), key)); err != ErrReplaceUnderpriced {
		t.Fatalf("original proper queued transaction replacement error mismatch: have %v, want %v", err, ErrReplaceUnderpriced)
	}
	if err := pool.AddRemote(pricedTransaction(2, 100000, big.NewInt(threshold), key)); err != nil {
		t.Fatalf("failed to replace original proper queued transaction: %v", err)
	}
	for len(pool.txFeedBuf) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if err := validateEvents(events, 0); err != nil {
		t.Fatalf("queued replacement event firing failed: %v", err)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// Tests that local transactions are journaled to disk, but remote transactions
// get discarded between restarts.
func TestTransactionJournaling(t *testing.T)         { testTransactionJournaling(t, false) }
func TestTransactionJournalingNoLocals(t *testing.T) { testTransactionJournaling(t, true) }

func testTransactionJournaling(t *testing.T, nolocals bool) {
	t.Parallel()

	// Create a temporary file for the journal
	file, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatalf("failed to create temporary journal: %v", err)
	}
	journal := file.Name()
	defer os.Remove(journal)

	// Clean up the temporary file, we only need the path for now
	file.Close()
	os.Remove(journal)

	// Create the original pool to inject transaction into the journal
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	config := testTxPoolConfig
	config.NoLocals = nolocals
	config.Journal = journal
	config.Rejournal = time.Second

	pool := NewTxPool(config, params.TestChainConfig, blockchain)

	// Create two test accounts to ensure remotes expire but locals do not
	local, _ := crypto.GenerateKey()
	remote, _ := crypto.GenerateKey()

	pool.mu.Lock()
	pool.currentState.AddBalance(crypto.PubkeyToAddress(local.PublicKey), big.NewInt(1000000000))
	pool.currentState.AddBalance(crypto.PubkeyToAddress(remote.PublicKey), big.NewInt(1000000000))
	pool.mu.Unlock()

	// Add three local and a remote transactions and ensure they are queued up
	if err := pool.AddLocal(pricedTransaction(0, 100000, big.NewInt(1), local)); err != nil {
		t.Fatalf("failed to add local transaction: %v", err)
	}
	if err := pool.AddLocal(pricedTransaction(1, 100000, big.NewInt(1), local)); err != nil {
		t.Fatalf("failed to add local transaction: %v", err)
	}
	if err := pool.AddLocal(pricedTransaction(2, 100000, big.NewInt(1), local)); err != nil {
		t.Fatalf("failed to add local transaction: %v", err)
	}
	if err := pool.AddRemote(pricedTransaction(0, 100000, big.NewInt(1), remote)); err != nil {
		t.Fatalf("failed to add remote transaction: %v", err)
	}
	pending, queued := pool.Stats()
	if pending != 4 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 4)
	}
	if queued != 0 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 0)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Terminate the old pool, bump the local nonce, create a new pool and ensure relevant transaction survive
	pool.Stop()
	statedb.SetNonce(crypto.PubkeyToAddress(local.PublicKey), 1)
	blockchain = newTestBlockChain(statedb, 1000000)

	pool = NewTxPool(config, params.TestChainConfig, blockchain)

	pending, queued = pool.Stats()
	if queued != 0 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 0)
	}
	if nolocals {
		if pending != 0 {
			t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 0)
		}
	} else {
		if pending != 2 {
			t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 2)
		}
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Bump the nonce temporarily and ensure the newly invalidated transaction is removed
	statedb.SetNonce(crypto.PubkeyToAddress(local.PublicKey), 2)
	pool.mu.Lock()
	pool.reset(nil, nil)
	pool.mu.Unlock()
	time.Sleep(2 * config.Rejournal)
	pool.Stop()

	statedb.SetNonce(crypto.PubkeyToAddress(local.PublicKey), 1)
	blockchain = newTestBlockChain(statedb, 1000000)
	pool = NewTxPool(config, params.TestChainConfig, blockchain)

	pending, queued = pool.Stats()
	if pending != 0 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 0)
	}
	if nolocals {
		if queued != 0 {
			t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 0)
		}
	} else {
		if queued != 1 {
			t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 1)
		}
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	pool.Stop()
}

// TestTransactionStatusCheck tests that the pool can correctly retrieve the
// pending status of individual transactions.
func TestTransactionStatusCheck(t *testing.T) {
	t.Parallel()

	// Create the pool to test the status retrievals with
	db := ethdb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	blockchain := newTestBlockChain(statedb, 1000000)

	pool := NewTxPool(testTxPoolConfig, params.TestChainConfig, blockchain)
	defer pool.Stop()

	// Create the test accounts to check various transaction statuses with
	keys := make([]*ecdsa.PrivateKey, 3)
	pool.mu.Lock()
	for i := 0; i < len(keys); i++ {
		keys[i], _ = crypto.GenerateKey()
		pool.currentState.AddBalance(crypto.PubkeyToAddress(keys[i].PublicKey), big.NewInt(1000000))
	}
	pool.mu.Unlock()
	// Generate and queue a batch of transactions, both pending and queued
	txs := types.Transactions{}

	txs = append(txs, pricedTransaction(0, 100000, big.NewInt(1), keys[0])) // Pending only
	txs = append(txs, pricedTransaction(0, 100000, big.NewInt(1), keys[1])) // Pending and queued
	txs = append(txs, pricedTransaction(2, 100000, big.NewInt(1), keys[1]))
	txs = append(txs, pricedTransaction(2, 100000, big.NewInt(1), keys[2])) // Queued only

	// Import the transaction and ensure they are correctly added
	pool.AddRemotes(txs)

	pending, queued := pool.Stats()
	if pending != 2 {
		t.Fatalf("pending transactions mismatched: have %d, want %d", pending, 2)
	}
	if queued != 2 {
		t.Fatalf("queued transactions mismatched: have %d, want %d", queued, 2)
	}
	if err := validateTxPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Retrieve the status of each transaction and validate them
	hashes := make([]common.Hash, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash()
	}
	hashes = append(hashes, common.Hash{})

	statuses := pool.Status(hashes)
	expect := []TxStatus{TxStatusPending, TxStatusPending, TxStatusQueued, TxStatusQueued, TxStatusUnknown}

	for i := 0; i < len(statuses); i++ {
		if statuses[i] != expect[i] {
			t.Errorf("transaction %d: status mismatch: have %v, want %v", i, statuses[i], expect[i])
		}
	}
}

// Benchmarks the speed of validating the contents of the pending queue of the
// transaction pool.
func BenchmarkTxPool_promoteTx(b *testing.B) {
	b.Run("10", func(b *testing.B) {
		benchmarkTxPool_promoteTx(b, 10)
	})
	b.Run("100", func(b *testing.B) {
		benchmarkTxPool_promoteTx(b, 100)
	})
	b.Run("1000", func(b *testing.B) {
		benchmarkTxPool_promoteTx(b, 1000)
	})
	b.Run("10000", func(b *testing.B) {
		benchmarkTxPool_promoteTx(b, 10000)
	})
}

func benchmarkTxPool_promoteTx(b *testing.B, size int) {
	pool, key := setupTxPool()
	defer pool.Stop()
	pool.mu.Lock()
	defer pool.mu.Unlock()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.currentState.AddBalance(account, big.NewInt(1000000))
	txs := make([]*types.Transaction, size)
	for i := range txs {
		txs[i] = transaction(uint64(1+i), 100000, key)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resetTxPool(pool)
		for _, tx := range txs {
			pool.promoteTx(account, tx.Hash(), tx)
		}
	}
}

// Benchmarks the speed of scheduling the contents of the future queue of the
// transaction pool.
func BenchmarkTxPool_enqueueTx(b *testing.B) {
	b.Run("10", func(b *testing.B) {
		benchmarkTxPool_enqueueTx(b, 10)
	})
	b.Run("100", func(b *testing.B) {
		benchmarkTxPool_enqueueTx(b, 100)
	})
	b.Run("1000", func(b *testing.B) {
		benchmarkTxPool_enqueueTx(b, 1000)
	})
	b.Run("10000", func(b *testing.B) {
		benchmarkTxPool_enqueueTx(b, 10000)
	})
}

func benchmarkTxPool_enqueueTx(b *testing.B, size int) {
	// Add a batch of transactions to a pool one by one
	pool, key := setupTxPool()
	defer pool.Stop()
	pool.mu.Lock()
	defer pool.mu.Unlock()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.currentState.AddBalance(account, big.NewInt(1000000))
	txs := make([]*types.Transaction, size)
	for i := range txs {
		txs[i] = transaction(uint64(i), 100000, key)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resetTxPool(pool)
		for i, tx := range txs {
			if _, err := pool.enqueueTx(tx); err != nil {
				b.Fatalf("failed to enqueue tx %d %s: %s", i, tx.Hash().Hex(), err)
			}
		}
	}
}

// Benchmarks the speed of iterative transaction insertion.
func BenchmarkTxPool_AddRemote(b *testing.B) {
	// Generate a batch of transactions to enqueue into the pool
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	pool.currentState.AddBalance(account, big.NewInt(1000000))
	pool.mu.Unlock()

	txs := make(types.Transactions, b.N)
	for i := 0; i < b.N; i++ {
		txs[i] = transaction(uint64(i), 100000, key)
	}
	// Benchmark importing the transactions into the queue
	b.ResetTimer()
	for _, tx := range txs {
		pool.AddRemote(tx)
	}
}

// Benchmarks the speed of batched transaction insertion.
func BenchmarkTxPool_AddRemotes(b *testing.B) {
	b.Run("100", func(b *testing.B) {
		benchmarkTxPool_AddRemotes(b, 100)
	})
	b.Run("1000", func(b *testing.B) {
		benchmarkTxPool_AddRemotes(b, 1000)
	})
	b.Run("10000", func(b *testing.B) {
		benchmarkTxPool_AddRemotes(b, 10000)
	})
}

func benchmarkTxPool_AddRemotes(b *testing.B, size int) {
	// Generate a batch of transactions to enqueue into the pool
	pool, key := setupTxPool()
	defer pool.Stop()

	account, _ := deriveSender(transaction(0, 0, key))
	pool.mu.Lock()
	pool.currentState.AddBalance(account, big.NewInt(1000000))
	pool.mu.Unlock()

	batches := make([]types.Transactions, b.N)
	for i := 0; i < b.N; i++ {
		batches[i] = make(types.Transactions, size)
		for j := 0; j < size; j++ {
			batches[i][j] = transaction(uint64(size*i+j), 100000, key)
		}
	}
	// Benchmark importing the transactions into the queue
	b.ResetTimer()
	for _, batch := range batches {
		pool.AddRemotes(batch)
	}
}
