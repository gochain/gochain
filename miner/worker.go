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

package miner

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/core"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/event"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/params"
)

const (
	resultQueueSize  = 10
	miningLogAtDepth = 5

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 4096
	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 100
	// chainSideChanSize is the size of channel listening to ChainSideEvent.
	chainSideChanSize = 10
)

// Agent can register themself with the worker
type Agent interface {
	Work() chan<- *Work
	SetReturnCh(chan<- *Result)
	Stop()
	Start(ctx context.Context)
	GetHashRate() int64
}

// Work is the workers current environment and holds
// all of the current state information
type Work struct {
	config *params.ChainConfig
	signer types.Signer

	stateMu   sync.RWMutex
	state     *state.StateDB           // apply state changes here
	ancestors map[common.Hash]struct{} // ancestor set (used for checking uncle parent validity)
	family    map[common.Hash]struct{} // family set (used for checking uncle invalidity)
	uncles    map[common.Hash]struct{} // uncle set
	uncleMu   sync.RWMutex
	tcount    int           // tx count in cycle
	gasPool   *core.GasPool // available gas used to pack transactions

	Block *types.Block // the new block

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt

	createdAt time.Time
}

type Result struct {
	Work  *Work
	Block *types.Block
}

// worker is the main object which takes care of applying messages to the new state
type worker struct {
	config *params.ChainConfig
	engine consensus.Engine

	mu sync.Mutex

	// update loop
	mux          *event.TypeMux
	txsCh        chan core.NewTxsEvent
	txsSub       event.Subscription
	chainHeadCh  chan core.ChainHeadEvent
	chainHeadSub event.Subscription
	chainSideCh  chan core.ChainSideEvent
	chainSideSub event.Subscription
	wg           sync.WaitGroup

	agents map[Agent]struct{}
	recv   chan *Result

	eth     Backend
	chain   *core.BlockChain
	proc    core.Validator
	chainDb ethdb.Database

	coinbase common.Address
	extra    []byte

	currentMu sync.RWMutex
	current   *Work

	snapshotMu    sync.RWMutex
	snapshotBlock *types.Block
	snapshotState *state.StateDB

	uncleMu        sync.Mutex
	possibleUncles map[common.Hash]*types.Block

	unconfirmed *unconfirmedBlocks // set of locally mined blocks pending canonicalness confirmations

	// atomic status counters
	mining int32
	atWork int32
}

func newWorker(ctx context.Context, config *params.ChainConfig, engine consensus.Engine, coinbase common.Address, eth Backend, mux *event.TypeMux) *worker {
	worker := &worker{
		config:         config,
		engine:         engine,
		eth:            eth,
		mux:            mux,
		txsCh:          make(chan core.NewTxsEvent, txChanSize),
		chainHeadCh:    make(chan core.ChainHeadEvent, chainHeadChanSize),
		chainSideCh:    make(chan core.ChainSideEvent, chainSideChanSize),
		chainDb:        eth.ChainDb(),
		recv:           make(chan *Result, resultQueueSize),
		chain:          eth.BlockChain(),
		proc:           eth.BlockChain().Validator(),
		possibleUncles: make(map[common.Hash]*types.Block),
		coinbase:       coinbase,
		agents:         make(map[Agent]struct{}),
		unconfirmed:    newUnconfirmedBlocks(eth.BlockChain(), miningLogAtDepth),
	}
	// Subscribe NewTxsEvent for tx pool
	worker.txsSub = eth.TxPool().SubscribeNewTxsEvent(worker.txsCh)
	// Subscribe events for blockchain
	worker.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)
	worker.chainSideSub = eth.BlockChain().SubscribeChainSideEvent(worker.chainSideCh)
	go worker.update(ctx)

	go worker.wait(ctx)
	worker.commitNewWork(ctx)

	return worker
}

func (w *worker) setEtherbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extra = extra
}

func (w *worker) pending() (*types.Block, *state.StateDB) {
	if atomic.LoadInt32(&w.mining) == 0 {
		// return a snapshot to avoid contention on currentMu mutex
		w.snapshotMu.RLock()
		defer w.snapshotMu.RUnlock()
		return w.snapshotBlock, w.snapshotState.Copy()
	}

	w.currentMu.RLock()
	defer w.currentMu.RUnlock()
	w.current.stateMu.RLock()
	defer w.current.stateMu.RUnlock()
	return w.current.Block, w.current.state.Copy()
}

func (w *worker) pendingQuery(fn func(*state.StateDB) error) error {
	// Although the queries must be 'read-only', the internal state may be updated, so we have to write lock.
	if atomic.LoadInt32(&w.mining) == 0 {
		// Query a snapshot to avoid contention on currentMu mutex.
		w.snapshotMu.Lock()
		defer w.snapshotMu.Unlock()
		return fn(w.snapshotState)
	}

	w.currentMu.Lock()
	defer w.currentMu.Unlock()
	w.current.stateMu.Lock()
	defer w.current.stateMu.Unlock()
	return fn(w.current.state)
}

func (w *worker) pendingBlock() *types.Block {
	if atomic.LoadInt32(&w.mining) == 0 {
		// return a snapshot to avoid contention on currentMu mutex
		w.snapshotMu.RLock()
		defer w.snapshotMu.RUnlock()
		return w.snapshotBlock
	}

	w.currentMu.RLock()
	defer w.currentMu.RUnlock()
	if w.current == nil {
		return nil
	}
	return w.current.Block
}

func (w *worker) start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	atomic.StoreInt32(&w.mining, 1)

	// spin up agents
	for agent := range w.agents {
		agent.Start(ctx)
	}
}

func (w *worker) stop() {
	w.wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()
	if atomic.LoadInt32(&w.mining) == 1 {
		for agent := range w.agents {
			agent.Stop()
		}
	}
	atomic.StoreInt32(&w.mining, 0)
	atomic.StoreInt32(&w.atWork, 0)
}

func (w *worker) register(agent Agent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agents[agent] = struct{}{}
	agent.SetReturnCh(w.recv)
}

func (w *worker) unregister(agent Agent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.agents, agent)
	agent.Stop()
}

func (w *worker) update(ctx context.Context) {
	defer w.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	defer w.chainSideSub.Unsubscribe()

	for {
		// A real event arrived, process interesting content
		select {
		// Handle ChainHeadEvent
		case <-w.chainHeadCh:
			w.commitNewWork(ctx)

		// Handle ChainSideEvent
		case ev := <-w.chainSideCh:
			w.uncleMu.Lock()
			w.possibleUncles[ev.Block.Hash()] = ev.Block
			w.uncleMu.Unlock()

		// Handle NewTxsEvent
		case ev := <-w.txsCh:
			// Apply transaction to the pending state if we're not mining
			//
			// Note all transactions received may not be continuous with transactions
			// already included in the current mining block. These transactions will
			// be automatically eliminated.
			if atomic.LoadInt32(&w.mining) == 0 {
				w.currentMu.Lock()
				w.current.stateMu.Lock()

				txs := make(map[common.Address]types.Transactions)
				for _, tx := range ev.Txs {
					acc, _ := types.Sender(ctx, w.current.signer, tx)
					txs[acc] = append(txs[acc], tx)
				}
				txset := types.NewTransactionsByPriceAndNonce(ctx, w.current.signer, txs)
				w.current.commitTransactions(ctx, w.mux, txset, w.chain, w.coinbase)
				w.updateSnapshot()

				w.current.stateMu.Unlock()
				w.currentMu.Unlock()
			} else {
				// If we're mining, but nothing is being processed, wake on new transactions
				if w.config.Clique != nil && w.config.Clique.Period == 0 {
					w.commitNewWork(ctx)
				}
			}

		// System stopped
		case <-w.txsSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		case <-w.chainSideSub.Err():
			return
		}
	}
}

func (w *worker) wait(ctx context.Context) {
	for {
		mustCommitNewWork := true
		for result := range w.recv {
			atomic.AddInt32(&w.atWork, -1)

			if result == nil {
				continue
			}
			block := result.Block
			work := result.Work

			// Update the block hash in all logs since it is now available and not when the
			// receipt/log of individual transactions were created.
			for _, r := range work.receipts {
				for _, l := range r.Logs {
					l.BlockHash = block.Hash()
				}
			}

			work.stateMu.Lock()
			for _, log := range work.state.Logs() {
				log.BlockHash = block.Hash()
			}
			stat, err := w.chain.WriteBlockWithState(block, work.receipts, work.state)
			work.stateMu.Unlock()
			if err != nil {
				log.Error("Failed writing block to chain", "err", err)
				continue
			}
			// check if canon block and write transactions
			if stat == core.CanonStatTy {
				// implicit by posting ChainHeadEvent
				mustCommitNewWork = false
			}
			// Broadcast the block and announce chain insertion event
			if err := w.mux.Post(core.NewMinedBlockEvent{Block: block}); err != nil {
				log.Error("Cannot post new mined block event", "err", err)
			}

			var events []interface{}

			work.stateMu.Lock()
			logs := work.state.Logs()
			work.stateMu.Unlock()

			events = append(events, core.ChainEvent{Block: block, Hash: block.Hash(), Logs: logs})
			if stat == core.CanonStatTy {
				events = append(events, core.ChainHeadEvent{Block: block})
			}
			w.chain.PostChainEvents(events, logs)

			// Insert the block into the set of pending ones to wait for confirmations
			w.unconfirmed.Insert(block.NumberU64(), block.Hash())

			if mustCommitNewWork {
				w.commitNewWork(ctx)
			}
		}
	}
}

// push sends a new work task to currently live miner agents.
func (w *worker) push(work *Work) {
	if atomic.LoadInt32(&w.mining) != 1 {
		return
	}
	for agent := range w.agents {
		atomic.AddInt32(&w.atWork, 1)
		if ch := agent.Work(); ch != nil {
			ch <- work
		}
	}
}

// makeCurrent creates a new environment for the current cycle.
func (w *worker) makeCurrent(parent *types.Block, header *types.Header) error {
	state, err := w.chain.StateAt(parent.Root())
	if err != nil {
		return err
	}
	work := &Work{
		config:    w.config,
		signer:    types.NewEIP155Signer(w.config.ChainId),
		state:     state,
		ancestors: make(map[common.Hash]struct{}),
		family:    make(map[common.Hash]struct{}),
		uncles:    make(map[common.Hash]struct{}),
		header:    header,
		createdAt: time.Now(),
	}

	// when 08 is processed ancestors contain 07 (quick block)
	for _, ancestor := range w.chain.GetBlocksFromHash(parent.Hash(), 7) {
		for _, uncle := range ancestor.Uncles() {
			work.family[uncle.Hash()] = struct{}{}
		}
		ah := ancestor.Hash()
		work.family[ah] = struct{}{}
		work.ancestors[ah] = struct{}{}
	}

	// Keep track of transactions which return errors so they can be removed
	work.tcount = 0
	w.current = work
	return nil
}

func (w *worker) commitNewWork(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.uncleMu.Lock()
	defer w.uncleMu.Unlock()
	w.currentMu.Lock()
	defer w.currentMu.Unlock()

	tstart := time.Now()
	parent := w.chain.CurrentBlock()

	tstamp := tstart.Unix()
	if parent.Time().Cmp(new(big.Int).SetInt64(tstamp)) >= 0 {
		tstamp = parent.Time().Int64() + 1
	}
	// this will ensure we're not going off too far in the future
	if now := time.Now().Unix(); tstamp > now+1 {
		wait := time.Duration(tstamp-now) * time.Second
		log.Info("Mining too far in the future", "wait", common.PrettyDuration(wait))
		time.Sleep(wait)
	}

	num := parent.Number()
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     num.Add(num, common.Big1),
		GasLimit:   core.CalcGasLimit(parent),
		Extra:      w.extra,
		Time:       big.NewInt(tstamp),
	}
	// Only set the coinbase if we are mining (avoid spurious block rewards)
	if atomic.LoadInt32(&w.mining) == 1 {
		header.Coinbase = w.coinbase
	}
	if err := w.engine.Prepare(ctx, w.chain, header); err != nil {
		log.Error("Failed to prepare header for mining", "err", err)
		return
	}
	// Could potentially happen if starting to mine in an odd state.
	err := w.makeCurrent(parent, header)
	if err != nil {
		log.Error("Failed to create mining context", "err", err)
		return
	}

	// Obtain current work's state lock after we receive new work assignment.
	w.current.stateMu.Lock()
	defer w.current.stateMu.Unlock()

	// Create the current work task and check any fork transitions needed
	work := w.current
	pending := w.eth.TxPool().Pending(ctx)
	txs := types.NewTransactionsByPriceAndNonce(ctx, w.current.signer, pending)
	work.commitTransactions(ctx, w.mux, txs, w.chain, w.coinbase)

	// compute uncles for the new block.
	var (
		uncles    []*types.Header
		badUncles []common.Hash
		tracing   = log.Tracing()
	)
	for hash, uncle := range w.possibleUncles {
		if len(uncles) == 2 {
			break
		}
		if err := w.commitUncle(work, uncle.Header()); err != nil {
			if tracing {
				log.Trace("Bad uncle found and will be removed", "hash", hash)
				log.Trace(fmt.Sprint(uncle))
			}

			badUncles = append(badUncles, hash)
		} else {
			log.Debug("Committing new uncle to block", "hash", hash)
			uncles = append(uncles, uncle.Header())
		}
	}
	for _, hash := range badUncles {
		delete(w.possibleUncles, hash)
	}
	// Create the new block to seal with the consensus engine
	work.Block = w.engine.Finalize(ctx, w.chain, header, work.state, work.txs, uncles, work.receipts, true)
	// We only care about logging if we're actually mining.
	if atomic.LoadInt32(&w.mining) == 1 {
		log.Info("Commit new mining work", "number", work.Block.Number(), "txs", work.tcount, "uncles", len(uncles), "elapsed", common.PrettyDuration(time.Since(tstart)))
		w.unconfirmed.Shift(work.Block.NumberU64() - 1)
	}
	w.push(work)
	w.updateSnapshot()
}

func (*worker) commitUncle(work *Work, uncle *types.Header) error {
	hash := uncle.Hash()
	work.uncleMu.RLock()
	_, ok := work.uncles[hash]
	work.uncleMu.RUnlock()
	if ok {
		return fmt.Errorf("uncle not unique")
	}
	if _, ok := work.ancestors[uncle.ParentHash]; !ok {
		return fmt.Errorf("uncle's parent unknown (%x)", uncle.ParentHash[0:4])
	}
	if _, ok := work.family[hash]; ok {
		return fmt.Errorf("uncle already in family (%x)", hash)
	}
	work.uncleMu.Lock()
	work.uncles[hash] = struct{}{}
	work.uncleMu.Unlock()
	return nil
}

// updateSnapshot updates snapshotState. Caller must hold currentMu.
func (w *worker) updateSnapshot() {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = types.NewBlock(
		w.current.header,
		w.current.txs,
		nil,
		w.current.receipts,
	)
	w.snapshotState = w.current.state.Copy()
}

func (env *Work) commitTransactions(ctx context.Context, mux *event.TypeMux, txs *types.TransactionsByPriceAndNonce, bc *core.BlockChain, coinbase common.Address) {
	if env.gasPool == nil {
		env.gasPool = new(core.GasPool).AddGas(env.header.GasLimit)
	}

	tracing := log.Tracing()
	// Create a new emv context and environment.
	evmContext := core.NewEVMContextLite(env.header, bc, &coinbase)
	vmenv := vm.NewEVM(evmContext, env.state, env.config, vm.Config{})
	var coalescedLogs []*types.Log
	for {
		// If we don't have enough gas for any further transactions then we're done
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}
		// Retrieve the next transaction and abort if all done
		tx := txs.Peek()
		if tx == nil {
			break
		}
		// Error may be ignored here. The error has already been checked
		// during transaction acceptance is the transaction pool.
		//
		// We use the eip155 signer regardless of the current hf.
		from, _ := types.Sender(ctx, env.signer, tx)
		// Check whether the tx is replay protected. If we're not in the EIP155 hf
		// phase, start ignoring the sender until we do.
		if tx.Protected() && !env.config.IsEIP155(env.header.Number) {
			if tracing {
				log.Trace("Ignoring reply protected transaction", "hash", tx.Hash(), "eip155", env.config.EIP155Block)
			}

			txs.Pop()
			continue
		}
		// Start executing the transaction
		env.state.Prepare(tx.Hash(), common.Hash{}, env.tcount)

		err, logs := env.commitTransaction(ctx, vmenv, tx, env.gasPool)
		switch err {
		case core.ErrGasLimitReached:
			// Pop the current out-of-gas transaction without shifting in the next from the account
			if tracing {
				log.Trace("Gas limit exceeded for current block", "sender", from)
			}
			txs.Pop()

		case core.ErrNonceTooLow:
			// New head notification data race between the transaction pool and miner, shift
			if tracing {
				log.Trace("Skipping transaction with low nonce", "sender", from, "nonce", tx.Nonce())
			}
			txs.Shift(ctx)

		case core.ErrNonceTooHigh:
			// Reorg notification data race between the transaction pool and miner, skip account =
			if tracing {
				log.Trace("Skipping account with high nonce", "sender", from, "nonce", tx.Nonce())
			}
			txs.Pop()

		case nil:
			// Everything ok, collect the logs and shift in the next transaction from the same account
			coalescedLogs = append(coalescedLogs, logs...)
			env.tcount++
			txs.Shift(ctx)

		default:
			// Strange error, discard the transaction and get the next in line (note, the
			// nonce-too-high clause will prevent us from executing in vain).
			log.Debug("Transaction failed, account skipped", "hash", tx.Hash(), "err", err)
			txs.Shift(ctx)
		}
	}

	if len(coalescedLogs) > 0 || env.tcount > 0 {
		// make a copy, the state caches the logs and these logs get "upgraded" from pending to mined
		// logs by filling in the block hash when the block was mined by the local miner. This can
		// cause a race condition if a log was "upgraded" before the PendingLogsEvent is processed.
		cpy := make([]*types.Log, len(coalescedLogs))
		for i, l := range coalescedLogs {
			cpy[i] = new(types.Log)
			*cpy[i] = *l
		}
		go func(logs []*types.Log, tcount int) {
			if len(logs) > 0 {
				if err := mux.Post(core.PendingLogsEvent{Logs: logs}); err != nil {
					log.Error("Cannot post pending logs event", "err", err)
				}
			}
			if tcount > 0 {
				if err := mux.Post(core.PendingStateEvent{}); err != nil {
					log.Error("Cannot post pending state event", "err", err)
				}
			}
		}(cpy, env.tcount)
	}
}

func (env *Work) commitTransaction(ctx context.Context, vmenv *vm.EVM, tx *types.Transaction, gp *core.GasPool) (error, []*types.Log) {
	snap := env.state.Snapshot()
	signer := types.MakeSigner(env.config, env.header.Number)
	receipt, _, err := core.ApplyTransaction(ctx, vmenv, env.config, gp, env.state, env.header, tx, &env.header.GasUsed, signer)
	if err != nil {
		env.state.RevertToSnapshot(snap)
		return err, nil
	}
	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)

	return nil, receipt.Logs
}
