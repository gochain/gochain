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

package miner

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/consensus"
	"github.com/gochain/gochain/v3/consensus/clique"
	"github.com/gochain/gochain/v3/core"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/core/vm"
	"github.com/gochain/gochain/v3/crypto"
	"github.com/gochain/gochain/v3/ethdb"
	"github.com/gochain/gochain/v3/params"
)

var (
	// Test chain configurations
	testTxPoolConfig  core.TxPoolConfig
	cliqueChainConfig *params.ChainConfig

	// Test accounts
	testBankKey, _  = crypto.GenerateKey()
	testBankAddress = crypto.PubkeyToAddress(testBankKey.PublicKey)
	testBankFunds   = big.NewInt(1000000000000000000)

	testUserKey, _  = crypto.GenerateKey()
	testUserAddress = crypto.PubkeyToAddress(testUserKey.PublicKey)

	// Test transactions
	pendingTxs []*types.Transaction
	newTxs     []*types.Transaction
)

func init() {
	testTxPoolConfig = core.DefaultTxPoolConfig
	testTxPoolConfig.Journal = ""
	cliqueChainConfig = params.TestChainConfig
	cliqueChainConfig.Clique = &params.CliqueConfig{
		Period: 10,
		Epoch:  30000,
	}
	tx1, _ := types.SignTx(types.NewTransaction(0, testUserAddress, big.NewInt(1000), params.TxGas, nil, nil), types.HomesteadSigner{}, testBankKey)
	pendingTxs = append(pendingTxs, tx1)
	tx2, _ := types.SignTx(types.NewTransaction(1, testUserAddress, big.NewInt(1000), params.TxGas, nil, nil), types.HomesteadSigner{}, testBankKey)
	newTxs = append(newTxs, tx2)
}

// testWorkerBackend implements worker.Backend interfaces and wraps all information needed during the testing.
type testWorkerBackend struct {
	db         common.Database
	txPool     *core.TxPool
	chain      *core.BlockChain
	uncleBlock *types.Block
}

func newTestWorkerBackend(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine, n int) *testWorkerBackend {
	var (
		db    = ethdb.NewMemDatabase()
		gspec = core.Genesis{
			Config:    chainConfig,
			Alloc:     core.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
			Signers:   []common.Address{testBankAddress},
			Voters:    []common.Address{testBankAddress},
			Signer:    make([]byte, 65),
			ExtraData: make([]byte, 32),
		}
	)

	genesis := gspec.MustCommit(db)

	chain, _ := core.NewBlockChain(db, nil, gspec.Config, engine, vm.Config{})
	txpool := core.NewTxPool(testTxPoolConfig, chainConfig, chain)

	// Generate a small n-block chain and an uncle block for it
	if n > 0 {
		blocks, _ := core.GenerateChain(chainConfig, genesis, engine, db, n, func(_ int, gen *core.BlockGen) {
			gen.SetCoinbase(testBankAddress)
		})
		if _, err := chain.InsertChain(blocks); err != nil {
			t.Fatalf("failed to insert origin chain: %v", err)
		}
	}
	parent := genesis
	if n > 0 {
		parent = chain.GetBlockByHash(chain.CurrentBlock().ParentHash())
	}
	blocks, _ := core.GenerateChain(chainConfig, parent, engine, db, 1, func(_ int, gen *core.BlockGen) {
		gen.SetCoinbase(testUserAddress)
	})

	return &testWorkerBackend{
		db:         db,
		chain:      chain,
		txPool:     txpool,
		uncleBlock: blocks[0],
	}
}

func (b *testWorkerBackend) BlockChain() *core.BlockChain { return b.chain }
func (b *testWorkerBackend) TxPool() *core.TxPool         { return b.txPool }
func (b *testWorkerBackend) PostChainEvents(events []interface{}) {
	b.chain.PostChainEvents(events, nil)
}

func newTestWorker(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine, blocks int) (*worker, *testWorkerBackend) {
	backend := newTestWorkerBackend(t, chainConfig, engine, blocks)
	backend.txPool.AddLocals(pendingTxs)
	w := newWorker(chainConfig, engine, backend, new(core.InterfaceFeed), time.Second, params.GenesisGasLimit, params.GenesisGasLimit, nil)
	w.setEtherbase(testBankAddress)
	return w, backend
}

func TestPendingStateAndBlockClique(t *testing.T) {
	testPendingStateAndBlock(t, cliqueChainConfig, clique.NewFaker())
}

func testPendingStateAndBlock(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	w, b := newTestWorker(t, chainConfig, engine, 0)
	defer w.close()

	// Ensure snapshot has been updated.
	time.Sleep(100 * time.Millisecond)
	block, state := w.pending()
	for block == nil {
		time.Sleep(100 * time.Millisecond)
		block, state = w.pending()
	}
	if block.NumberU64() != 1 {
		t.Errorf("block number mismatch: have %d, want %d", block.NumberU64(), 1)
	}
	if balance := state.GetBalance(testUserAddress); balance.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("account balance mismatch: have %d, want %d", balance, 1000)
	}
	b.txPool.AddLocals(newTxs)

	// Ensure the new tx events has been processed
	time.Sleep(100 * time.Millisecond)
	block, state = w.pending()
	if balance := state.GetBalance(testUserAddress); balance.Cmp(big.NewInt(2000)) != 0 {
		t.Errorf("account balance mismatch: have %d, want %d", balance, 2000)
	}
}

func TestEmptyWorkClique(t *testing.T) {
	testEmptyWork(t, cliqueChainConfig, clique.NewFaker())
}

func testEmptyWork(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	w, _ := newTestWorker(t, chainConfig, engine, 0)
	defer w.close()

	var (
		taskCh    = make(chan struct{}, 2)
		taskIndex int
	)

	checkEqual := func(t *testing.T, task *task, index int) {
		receiptLen, balance := 0, big.NewInt(0)
		if index == 1 {
			receiptLen, balance = 1, big.NewInt(1000)
		}
		if len(task.receipts) != receiptLen {
			t.Errorf("receipt number mismatch: have %d, want %d", len(task.receipts), receiptLen)
		}
		if task.state.GetBalance(testUserAddress).Cmp(balance) != 0 {
			t.Errorf("account balance mismatch: have %d, want %d", task.state.GetBalance(testUserAddress), balance)
		}
	}

	w.newTaskHook = func(task *task) {
		if task.block.NumberU64() == 1 {
			checkEqual(t, task, taskIndex)
			taskIndex += 1
			taskCh <- struct{}{}
		}
	}
	w.setFullTaskDelay(100 * time.Millisecond)

	// Ensure worker has finished initialization
	for {
		b := w.pendingBlock()
		if b != nil && b.NumberU64() == 1 {
			break
		}
	}

	w.start()
	for i := 0; i < 2; i += 1 {
		select {
		case <-taskCh:
		case <-time.After(10*time.Second):
			t.Error("new task timeout")
		}
	}
}

func TestRegenerateMiningBlockClique(t *testing.T) {
	testRegenerateMiningBlock(t, cliqueChainConfig, clique.NewFaker())
}

func testRegenerateMiningBlock(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	w, b := newTestWorker(t, chainConfig, engine, 0)
	defer w.close()

	var taskCh = make(chan struct{})

	taskIndex := 0
	w.newTaskHook = func(task *task) {
		if task.block.NumberU64() == 1 {
			if taskIndex == 2 {
				receiptLen, balance := 2, big.NewInt(2000)
				if len(task.receipts) != receiptLen {
					t.Errorf("receipt number mismatch: have %d, want %d", len(task.receipts), receiptLen)
				}
				if task.state.GetBalance(testUserAddress).Cmp(balance) != 0 {
					t.Errorf("account balance mismatch: have %d, want %d", task.state.GetBalance(testUserAddress), balance)
				}
			}
			taskCh <- struct{}{}
			taskIndex += 1
		}
	}
	w.skipSealHook = func(task *task) bool {
		return true
	}
	w.setFullTaskDelay(100 * time.Millisecond)

	// Ensure worker has finished initialization
	for {
		b := w.pendingBlock()
		if b != nil && b.NumberU64() == 1 {
			break
		}
	}

	w.start()
	// Ignore the first two works
	for i := 0; i < 2; i += 1 {
		select {
		case <-taskCh:
		case <-time.NewTimer(time.Second).C:
			t.Error("new task timeout")
		}
	}
	b.txPool.AddLocals(newTxs)
	time.Sleep(time.Second)

	select {
	case <-taskCh:
	case <-time.NewTimer(time.Second).C:
		t.Error("new task timeout")
	}
}

func TestAdjustIntervalClique(t *testing.T) {
	testAdjustInterval(t, cliqueChainConfig, clique.NewFaker())
}

func testAdjustInterval(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	w, _ := newTestWorker(t, chainConfig, engine, 0)
	defer w.close()

	w.skipSealHook = func(task *task) bool {
		return true
	}
	w.setFullTaskDelay(100 * time.Millisecond)
	var (
		progress = make(chan struct{}, 10)
		result   = make([]float64, 0, 10)
		index    = 0
		start    int32
	)
	w.setResubmitHook(func(minInterval time.Duration, recommitInterval time.Duration) {
		// Short circuit if interval checking hasn't started.
		if atomic.LoadInt32(&start) == 0 {
			return
		}
		var wantMinInterval, wantRecommitInterval time.Duration

		switch index {
		case 0:
			wantMinInterval, wantRecommitInterval = 3*time.Second, 3*time.Second
		case 1:
			origin := float64(3 * time.Second.Nanoseconds())
			estimate := origin*(1-intervalAdjustRatio) + intervalAdjustRatio*(origin/0.8+intervalAdjustBias)
			wantMinInterval, wantRecommitInterval = 3*time.Second, time.Duration(estimate)*time.Nanosecond
		case 2:
			estimate := result[index-1]
			min := float64(3 * time.Second.Nanoseconds())
			estimate = estimate*(1-intervalAdjustRatio) + intervalAdjustRatio*(min-intervalAdjustBias)
			wantMinInterval, wantRecommitInterval = 3*time.Second, time.Duration(estimate)*time.Nanosecond
		case 3:
			wantMinInterval, wantRecommitInterval = time.Second, time.Second
		}

		// Check interval
		if minInterval != wantMinInterval {
			t.Errorf("resubmit min interval mismatch: have %v, want %v ", minInterval, wantMinInterval)
		}
		if recommitInterval != wantRecommitInterval {
			t.Errorf("resubmit interval mismatch: have %v, want %v", recommitInterval, wantRecommitInterval)
		}
		result = append(result, float64(recommitInterval.Nanoseconds()))
		index += 1
		progress <- struct{}{}
	})
	// Ensure worker has finished initialization
	for {
		b := w.pendingBlock()
		if b != nil && b.NumberU64() == 1 {
			break
		}
	}

	w.start()

	time.Sleep(time.Second)

	atomic.StoreInt32(&start, 1)
	w.setRecommitInterval(3 * time.Second)
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}

	w.resubmitAdjustCh <- &intervalAdjust{inc: true, ratio: 0.8}
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}

	w.resubmitAdjustCh <- &intervalAdjust{inc: false}
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}

	w.setRecommitInterval(500 * time.Millisecond)
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}
}
