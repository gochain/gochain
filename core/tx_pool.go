// Copyright 2014 The go-ethereum Authors
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
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"go.opencensus.io/trace"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/common/prque"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/eth/gasprice"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/metrics"
	"github.com/gochain-io/gochain/params"
)

const (
	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 32
)

var (
	// ErrInvalidSender is returned if the transaction contains an invalid signature.
	ErrInvalidSender = errors.New("invalid sender")

	// ErrNonceTooLow is returned if the nonce of a transaction is lower than the
	// one present in the local chain.
	ErrNonceTooLow = errors.New("nonce too low")

	// ErrUnderpriced is returned if a transaction's gas price is below the minimum
	// configured for the transaction pool.
	ErrUnderpriced = errors.New("transaction underpriced")

	// ErrPoolLimit is returned if the pool is full.
	ErrPoolLimit = errors.New("transaction pool limit reached")

	// ErrReplaceUnderpriced is returned if a transaction is attempted to be replaced
	// with a different one without the required price bump.
	ErrReplaceUnderpriced = errors.New("replacement transaction underpriced")

	// ErrInsufficientFunds is returned if the total cost of executing a transaction
	// is higher than the balance of the user's account.
	ErrInsufficientFunds = errors.New("insufficient funds for gas * price + value")

	// ErrIntrinsicGas is returned if the transaction is specified to use less gas
	// than required to start the invocation.
	ErrIntrinsicGas = errors.New("intrinsic gas too low")

	// ErrGasLimit is returned if a transaction's requested gas limit exceeds the
	// maximum allowance of the current block.
	ErrGasLimit = errors.New("exceeds block gas limit")

	// ErrNegativeValue is a sanity error to ensure noone is able to specify a
	// transaction with a negative value.
	ErrNegativeValue = errors.New("negative value")

	// ErrOversizedData is returned if the input data of a transaction is greater
	// than some meaningful limit a user might use. This is not a consensus error
	// making the transaction invalid, rather a DOS protection.
	ErrOversizedData = errors.New("oversized data")
)

var (
	evictionInterval    = time.Minute      // Time interval to check for evictable transactions
	statsReportInterval = 10 * time.Second // Time interval to report transaction pool stats
	resetInterval       = 1 * time.Second  // Time interval to reset the txpool
)

var (
	// Metrics for the pending pool
	pendingGauge            = metrics.NewRegisteredGauge("txpool/pending", nil)
	pendingDiscardCounter   = metrics.NewRegisteredCounter("txpool/pending/discard", nil)
	pendingReplaceCounter   = metrics.NewRegisteredCounter("txpool/pending/replace", nil)
	pendingRateLimitCounter = metrics.NewRegisteredCounter("txpool/pending/ratelimit", nil) // Dropped due to rate limiting
	pendingNofundsCounter   = metrics.NewRegisteredCounter("txpool/pending/nofunds", nil)   // Dropped due to out-of-funds

	// Metrics for the queued pool
	queuedGauge            = metrics.NewRegisteredGauge("txpool/queued", nil)
	queuedDiscardCounter   = metrics.NewRegisteredCounter("txpool/queued/discard", nil)
	queuedReplaceCounter   = metrics.NewRegisteredCounter("txpool/queued/replace", nil)
	queuedRateLimitCounter = metrics.NewRegisteredCounter("txpool/queued/ratelimit", nil) // Dropped due to rate limiting
	queuedNofundsCounter   = metrics.NewRegisteredCounter("txpool/queued/nofunds", nil)   // Dropped due to out-of-funds

	// General tx metrics
	invalidTxCounter     = metrics.NewRegisteredCounter("txpool/invalid", nil)
	underpricedTxCounter = metrics.NewRegisteredCounter("txpool/underpriced", nil)
	globalSlotsGauge     = metrics.NewRegisteredGauge("txpool/slots", nil)
	globalQueueGauge     = metrics.NewRegisteredGauge("txpool/queue", nil)
	poolAddTimer         = metrics.NewRegisteredTimer("txpool/add", nil)
	journalInsertTimer   = metrics.NewRegisteredTimer("txpool/journal/insert", nil)
	chainHeadGauge       = metrics.NewRegisteredGauge("txpool/chain/head", nil)
	chainHeadTxsGauge    = metrics.NewRegisteredGauge("txpool/chain/head/txs", nil)
)

// TxStatus is the current status of a transaction as seen by the pool.
type TxStatus uint

const (
	TxStatusUnknown TxStatus = iota
	TxStatusQueued
	TxStatusPending
	TxStatusIncluded
)

// blockChain provides the state of blockchain and current gas limit to do
// some pre checks in tx pool and event subscribers.
type blockChain interface {
	CurrentBlock() *types.Block
	GetBlock(hash common.Hash, number uint64) *types.Block
	StateAt(root common.Hash) (*state.StateDB, error)

	SubscribeChainHeadEvent(ch chan<- ChainHeadEvent, name string)
	UnsubscribeChainHeadEvent(ch chan<- ChainHeadEvent)
}

// TxPoolConfig are the configuration parameters of the transaction pool.
type TxPoolConfig struct {
	NoLocals  bool          `toml:",omitempty"` // Whether local transaction handling should be disabled
	Journal   string        `toml:",omitempty"` // Journal of local transactions to survive node restarts
	Rejournal time.Duration `toml:",omitempty"` // Time interval to regenerate the local transaction journal

	PriceLimit uint64 `toml:",omitempty"` // Minimum gas price to enforce for acceptance into the pool
	PriceBump  uint64 `toml:",omitempty"` // Minimum price bump percentage to replace an already existing transaction (nonce)

	AccountSlots uint64 `toml:",omitempty"` // Minimum number of executable transaction slots guaranteed per account
	GlobalSlots  uint64 `toml:",omitempty"` // Maximum number of executable transaction slots for all accounts
	AccountQueue uint64 `toml:",omitempty"` // Maximum number of non-executable transaction slots permitted per account
	GlobalQueue  uint64 `toml:",omitempty"` // Maximum number of non-executable transaction slots for all accounts

	Lifetime time.Duration `toml:",omitempty"` // Maximum amount of time non-executable transaction are queued
}

// DefaultTxPoolConfig contains the default configurations for the transaction
// pool.
var DefaultTxPoolConfig = TxPoolConfig{
	Journal:   "transactions.rlp",
	Rejournal: time.Hour,

	PriceLimit: gasprice.Default.Uint64(),
	PriceBump:  10,

	AccountSlots: 8192,
	GlobalSlots:  131072,
	AccountQueue: 4096,
	GlobalQueue:  32768,

	Lifetime: 3 * time.Hour,
}

// sanitize checks the provided user configurations and changes anything that's
// unreasonable or unworkable.
func (config *TxPoolConfig) sanitize() TxPoolConfig {
	conf := *config
	if conf.Rejournal < time.Second {
		log.Warn("Sanitizing invalid txpool journal time", "provided", conf.Rejournal, "updated", time.Second)
		conf.Rejournal = time.Second
	}
	if conf.PriceLimit < 1 {
		log.Warn("Sanitizing invalid txpool price limit", "provided", conf.PriceLimit, "updated", DefaultTxPoolConfig.PriceLimit)
		conf.PriceLimit = DefaultTxPoolConfig.PriceLimit
	}
	if conf.PriceBump < 1 {
		log.Warn("Sanitizing invalid txpool price bump", "provided", conf.PriceBump, "updated", DefaultTxPoolConfig.PriceBump)
		conf.PriceBump = DefaultTxPoolConfig.PriceBump
	}
	if conf.AccountSlots <= 0 {
		log.Warn("Sanitizing invalid txpool account slots", "provided", conf.AccountSlots, "updated", DefaultTxPoolConfig.AccountSlots)
		conf.AccountSlots = DefaultTxPoolConfig.AccountSlots
	}
	if conf.GlobalSlots <= 0 {
		log.Warn("Sanitizing invalid txpool global slots", "provided", conf.GlobalSlots, "updated", DefaultTxPoolConfig.GlobalSlots)
		conf.GlobalSlots = DefaultTxPoolConfig.GlobalSlots
	}
	if conf.AccountQueue <= 0 {
		log.Warn("Sanitizing invalid txpool account queue", "provided", conf.AccountQueue, "updated", DefaultTxPoolConfig.AccountQueue)
		conf.AccountQueue = DefaultTxPoolConfig.AccountQueue
	}
	if conf.GlobalQueue <= 0 {
		log.Warn("Sanitizing invalid txpool global queue", "provided", conf.GlobalQueue, "updated", DefaultTxPoolConfig.GlobalQueue)
		conf.GlobalQueue = DefaultTxPoolConfig.GlobalQueue
	}
	if conf.Lifetime <= 0 {
		log.Warn("Sanitizing invalid txpool lifetime", "provided", conf.Lifetime, "updated", DefaultTxPoolConfig.Lifetime)
		conf.Lifetime = DefaultTxPoolConfig.Lifetime
	}
	return conf
}

// TxPool contains all currently known transactions. Transactions
// enter the pool when they are received from the network or submitted
// locally. They exit the pool when they are included in the blockchain.
//
// The pool separates processable transactions (which can be applied to the
// current state) and future transactions. Transactions move between those
// two states over time as they are received and processed.
type TxPool struct {
	config      TxPoolConfig
	chainconfig *params.ChainConfig

	chain    blockChain
	gasPrice *big.Int

	txFeedBuf chan *types.Transaction

	txFeed NewTxsFeed

	chainHeadCh chan ChainHeadEvent

	signer types.Signer
	mu     sync.RWMutex

	currentState  *state.StateDB      // Current state in the blockchain head
	pendingState  *state.ManagedState // Pending state tracking virtual nonces
	currentMaxGas uint64              // Current gas limit for transaction caps

	locals  *accountSet // Set of local transaction to exempt from eviction rules
	journal *txJournal  // Journal of local transaction to back up to disk

	pending map[common.Address]*txList   // All currently processable transactions
	queue   map[common.Address]*txList   // Queued but non-processable transactions
	beats   map[common.Address]time.Time // Last heartbeat from each known account
	all     *txLookup                    // All transactions to allow lookups

	wg sync.WaitGroup // for shutdown sync

	homestead bool

	stop chan struct{}
}

// NewTxPool creates a new transaction pool to gather, sort and filter inbound
// transactions from the network.
func NewTxPool(config TxPoolConfig, chainconfig *params.ChainConfig, chain blockChain) *TxPool {
	ctx, span := trace.StartSpan(context.Background(), "NewTxPool")
	defer span.End()

	// Sanitize the input to ensure no vulnerable gas prices are set
	config = (&config).sanitize()

	// Create the transaction pool with its initial settings
	pool := &TxPool{
		config:      config,
		chainconfig: chainconfig,
		chain:       chain,
		signer:      types.NewEIP155Signer(chainconfig.ChainId),
		pending:     make(map[common.Address]*txList),
		queue:       make(map[common.Address]*txList),
		beats:       make(map[common.Address]time.Time),
		all:         newTxLookup(int(config.GlobalSlots / 2)),
		chainHeadCh: make(chan ChainHeadEvent, chainHeadChanSize),
		gasPrice:    new(big.Int).SetUint64(config.PriceLimit),
		txFeedBuf:   make(chan *types.Transaction, config.GlobalSlots/4),
	}
	pool.locals = newAccountSet(pool.signer)
	pool.reset(ctx, nil, chain.CurrentBlock())

	// If local transactions and journaling is enabled, load from disk
	if !config.NoLocals && config.Journal != "" {
		ctx, span := trace.StartSpan(ctx, "NewTxPool-journal")
		pool.journal = newTxJournal(config.Journal)
		if err := pool.journal.load(func(txs types.Transactions) []error {
			// No need to lock since we're still setting up.
			return pool.addTxsLocked(ctx, txs, !pool.config.NoLocals)
		}); err != nil {
			log.Warn("Failed to load transaction journal", "err", err)
		}
		if err := pool.journal.rotate(pool.local()); err != nil {
			log.Warn("Failed to rotate transaction journal", "err", err)
		}
		span.End()
	}

	// Subscribe events from blockchain.
	pool.chain.SubscribeChainHeadEvent(pool.chainHeadCh, "core.TxPool")
	// Spawn worker routines to run until chainHeadSub unsub.
	pool.wg.Add(3)
	pool.stop = make(chan struct{})
	go pool.loop()
	go pool.feedLoop()

	return pool
}

// loop is the transaction pool's main event loop, waiting for and reacting to
// outside blockchain events as well as for various reporting and transaction
// eviction events.
func (pool *TxPool) loop() {
	defer pool.wg.Done()

	// Start the stats reporting and transaction eviction tickers
	var prevPending, prevQueued int

	report := time.NewTicker(statsReportInterval)
	defer report.Stop()

	evict := time.NewTicker(evictionInterval)
	defer evict.Stop()

	reset := time.NewTicker(resetInterval)
	defer reset.Stop()

	journal := time.NewTicker(pool.config.Rejournal)
	defer journal.Stop()

	// Track the current and latest blocks for transaction reorgs.
	b := pool.chain.CurrentBlock()
	blocks := struct {
		sync.RWMutex
		current, latest *types.Block
	}{
		current: b,
		latest:  b,
	}

	chainHeadGauge.Update(int64(blocks.current.NumberU64()))
	chainHeadTxsGauge.Update(int64(len(blocks.current.Transactions())))

	globalSlotsGauge.Update(int64(pool.config.GlobalSlots))
	globalQueueGauge.Update(int64(pool.config.GlobalQueue))

	// Handle ChainHeadEvents separately
	go func() {
		defer pool.wg.Done()
		defer pool.chain.UnsubscribeChainHeadEvent(pool.chainHeadCh)

		for {
			select {
			case <-pool.stop:
				return
			case ev, ok := <-pool.chainHeadCh:
				if !ok {
					return
				}
				if ev.Block == nil {
					continue
				}
				blocks.RLock()
				chain := blocks.latest
				blocks.RUnlock()

				n := ev.Block.Number()
				if n.Cmp(chain.Number()) <= 0 {
					continue
				}
				// Update latestBlock for next reset.
				blocks.Lock()
				blocks.latest = ev.Block
				blocks.Unlock()

				if !pool.chainconfig.IsHomestead(n) {
					continue
				}
				pool.mu.Lock()
				pool.homestead = true
				pool.mu.Unlock()
			}
		}
	}()

	// Handle tickers.
	for {
		select {
		case <-pool.stop:
			return

		// Periodically reset to latest chain head.
		case <-reset.C:
			ctx, span := trace.StartSpan(context.Background(), "TxPool.loop-reset")
			blocks.RLock()
			latest, current := blocks.latest, blocks.current
			blocks.RUnlock()

			if latest.NumberU64() > current.NumberU64() {
				pool.mu.Lock()
				pool.reset(ctx, current, latest)
				pool.mu.Unlock()

				blocks.Lock()
				blocks.current = latest
				blocks.Unlock()

				chainHeadGauge.Update(int64(latest.NumberU64()))
				chainHeadTxsGauge.Update(int64(len(latest.Transactions())))
			}
			span.End()

		// Handle stats reporting ticks
		case <-report.C:
			ctx, span := trace.StartSpan(context.Background(), "TxPool.loop-report")
			pending, queued := pool.StatsCtx(ctx)

			pendingGauge.Update(int64(pending))
			queuedGauge.Update(int64(queued))

			if pending != prevPending || queued != prevQueued {
				log.Debug("Transaction pool status report", "executable", pending, "queued", queued)
				prevPending, prevQueued = pending, queued
			}
			span.End()

		// Handle inactive account transaction eviction
		case <-evict.C:
			_, span := trace.StartSpan(context.Background(), "TxPool.loop-evict")
			pool.mu.Lock()
			for addr := range pool.queue {
				// Skip local transactions from the eviction mechanism
				if pool.locals.contains(addr) {
					continue
				}
				// Any non-locals old enough should be removed
				if time.Since(pool.beats[addr]) > pool.config.Lifetime {
					queued := pool.queue[addr]
					for _, tx := range queued.txs.items {
						pool.all.Remove(tx.Hash())
					}
					delete(pool.queue, addr)
				}
			}
			pool.mu.Unlock()
			span.End()

		// Handle local transaction journal rotation
		case <-journal.C:
			if pool.journal != nil {
				_, span := trace.StartSpan(context.Background(), "TxPool.loop-journal")
				pool.mu.Lock()
				if err := pool.journal.rotate(pool.local()); err != nil {
					log.Warn("Failed to rotate local tx journal", "err", err)
				}
				pool.mu.Unlock()
				span.End()
			}
		}
	}
}

// queueFeedSend queues tx to eventually be sent on the txFeed.
func (pool *TxPool) queueFeedSend(tx *types.Transaction) {
	select {
	case <-pool.stop:
		return
	case pool.txFeedBuf <- tx:
		return
	default:
		go func() {
			select {
			case <-pool.stop:
				return
			case pool.txFeedBuf <- tx:
			}
		}()
	}
}

// feedLoop continuously sends batches of txs from the txFeedBuf to the txFeed.
func (pool *TxPool) feedLoop() {
	defer pool.wg.Done()

	const batchSize = 1000
	for {
		select {
		case <-pool.stop:
			return
		case tx := <-pool.txFeedBuf:
			var event NewTxsEvent
			event.Txs = append(event.Txs, tx)
		batchLoop:
			for i := 1; i < batchSize; i++ {
				select {
				case tx := <-pool.txFeedBuf:
					event.Txs = append(event.Txs, tx)
				default:
					break batchLoop
				}
			}
			pool.txFeed.Send(event)
		}
	}
}

const maxReorgDepth = 16

// reset retrieves the current state of the blockchain and ensures the content
// of the transaction pool is valid with regard to the chain state.
func (pool *TxPool) reset(ctx context.Context, oldBlock, newBlock *types.Block) {
	ctx, span := trace.StartSpan(ctx, "TxPool.reset")
	defer span.End()

	// If we're reorging an old state, reinject all dropped transactions
	reinject := make(map[common.Hash]*types.Transaction)

	if oldBlock != nil && oldBlock.Hash() != newBlock.ParentHash() {
		// If the reorg is too deep, avoid doing it (will happen during fast sync)
		branchNum := oldBlock.NumberU64()
		mainNum := newBlock.NumberU64()

		var depth uint64
		if branchNum > mainNum {
			depth = branchNum - mainNum
		} else {
			depth = mainNum - branchNum
		}
		if depth > maxReorgDepth {
			// Too deep to pull all transactions into memory.
			log.Debug("Skipping deep transaction reorg", "depth", depth)
		} else {
			// Set of txs included in the main chain.
			included := make(map[common.Hash]struct{})
			var (
				branch = oldBlock
				main   = newBlock
			)
			// Rewind main up the chain to a possible common ancestor.
			for main.NumberU64() > branchNum {
				for _, tx := range main.Transactions() {
					included[tx.Hash()] = struct{}{}
				}
				if main = pool.chain.GetBlock(main.ParentHash(), main.NumberU64()-1); main == nil {
					log.Error("Unrooted new chain seen by tx pool", "block", mainNum, "hash", newBlock.Hash())
					return
				}
			}
			// Rewind branch up the chain to a possible common ancestor.
			for branch.NumberU64() > mainNum {
				for _, tx := range branch.Transactions() {
					if _, ok := included[tx.Hash()]; !ok {
						reinject[tx.Hash()] = tx
					}
				}
				if branch = pool.chain.GetBlock(branch.ParentHash(), branch.NumberU64()-1); branch == nil {
					log.Error("Unrooted old chain seen by tx pool", "block", branchNum, "hash", oldBlock.Hash())
					return
				}
			}
			// Continue up the chain until a common ancestor is found.
			for branch.Hash() != main.Hash() {
				for _, tx := range main.Transactions() {
					hash := tx.Hash()
					if _, ok := reinject[hash]; ok {
						delete(reinject, hash)
					}
					included[hash] = struct{}{}
				}
				if main = pool.chain.GetBlock(main.ParentHash(), main.NumberU64()-1); main == nil {
					log.Error("Unrooted new chain seen by tx pool", "block", mainNum, "hash", newBlock.Hash())
					return
				}
				for _, tx := range branch.Transactions() {
					if _, ok := included[tx.Hash()]; !ok {
						reinject[tx.Hash()] = tx
					}
				}
				if branch = pool.chain.GetBlock(branch.ParentHash(), branch.NumberU64()-1); branch == nil {
					log.Error("Unrooted old chain seen by tx pool", "block", branchNum, "hash", oldBlock.Hash())
					return
				}
			}
		}
	}
	// Initialize the internal state to the current head
	if newBlock == nil {
		newBlock = pool.chain.CurrentBlock() // Special case during testing
	}
	statedb, err := pool.chain.StateAt(newBlock.Root())
	if err != nil {
		log.Error("Failed to reset txpool state", "err", err)
		return
	}
	pool.currentState = statedb
	pool.pendingState = state.ManageState(ctx, statedb)
	pool.currentMaxGas = newBlock.GasLimit()

	if l := len(reinject); l > 0 {
		// Inject any transactions discarded due to reorgs.
		log.Debug("Reinjecting stale transactions", "count", l)
		if errs := pool.reinject(ctx, reinject); len(errs) > 0 {
			log.Error("Failed to reinject txs during pool reset", "total", l, "errs", len(errs))
			if log.Tracing() {
				for i, err := range errs {
					log.Trace("Failed to reinject tx", "num", i, "err", err)
				}
			}
		}
	}

	// validate the pool of pending transactions, this will remove
	// any transactions that have been included in the block or
	// have been invalidated because of another transaction (e.g.
	// higher gas price)
	pool.demoteUnexecutables(ctx)

	// Update all accounts to the latest known pending nonce
	for addr, list := range pool.pending {
		pool.pendingState.SetNonce(addr, list.Last().Nonce()+1)
	}
	// Check the queue and move transactions over to the pending if possible
	// or remove those that have become invalid
	pool.promoteExecutablesAll(ctx)
}

// Stop terminates the transaction pool.
func (pool *TxPool) Stop() {
	close(pool.stop)
	// Unsubscribe all subscriptions.
	pool.txFeed.Close()
	pool.wg.Wait()

	if pool.journal != nil {
		if err := pool.journal.close(); err != nil {
			log.Error("Cannot close tx pool journal", "err", err)
		}
	}
	log.Info("Transaction pool stopped")
}

// SubscribeNewTxsEvent registers a subscription of NewTxsEvent and
// starts sending event to the given channel.
func (pool *TxPool) SubscribeNewTxsEvent(ch chan<- NewTxsEvent, name string) {
	pool.txFeed.Subscribe(ch, name)
}

func (pool *TxPool) UnsubscribeNewTxsEvent(ch chan<- NewTxsEvent) {
	pool.txFeed.Unsubscribe(ch)
}

// SetGasPrice updates the minimum price required by the transaction pool for a
// new transaction, and drops all transactions below this threshold.
func (pool *TxPool) SetGasPrice(ctx context.Context, price *big.Int) {
	ctx, span := trace.StartSpan(ctx, "TxPool.SetGasPrice")
	defer span.End()

	pool.mu.Lock()
	defer pool.mu.Unlock()

	pool.gasPrice = price
	pool.all.ForEach(func(tx *types.Transaction) {
		if tx.CmpGasPrice(price) < 0 && !pool.locals.containsTx(ctx, tx) {
			pool.removeTx(ctx, tx)
		}
	})
	log.Info("Transaction pool price threshold updated", "price", price)
}

// State returns the virtual managed state of the transaction pool.
func (pool *TxPool) State() *state.ManagedState {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.pendingState
}

// Stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
func (pool *TxPool) Stats() (int, int) {
	return pool.StatsCtx(context.Background())
}
func (pool *TxPool) StatsCtx(ctx context.Context) (int, int) {
	ctx, span := trace.StartSpan(ctx, "TxPool.StatsCtx")
	defer span.End()

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.stats(ctx)
}

// stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
func (pool *TxPool) stats(ctx context.Context) (int, int) {
	ctx, span := trace.StartSpan(ctx, "TxPool.stats")
	defer span.End()

	pending := 0
	for _, list := range pool.pending {
		pending += list.Len()
	}
	queued := 0
	for _, list := range pool.queue {
		queued += list.Len()
	}
	return pending, queued
}

// Content retrieves the data content of the transaction pool, returning all the
// pending as well as queued transactions, grouped by account and sorted by nonce.
func (pool *TxPool) Content(ctx context.Context) (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	ctx, span := trace.StartSpan(ctx, "TxPool.Content")
	defer span.End()
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[common.Address]types.Transactions)
	for addr, list := range pool.pending {
		pending[addr] = list.Flatten()
	}
	queued := make(map[common.Address]types.Transactions)
	for addr, list := range pool.queue {
		queued[addr] = list.Flatten()
	}
	return pending, queued
}

// Pending retrieves all currently processable transactions, groupped by origin
// account and sorted by nonce. The returned transaction set is a copy and can be
// freely modified by calling code.
func (pool *TxPool) Pending(ctx context.Context) map[common.Address]types.Transactions {
	ctx, span := trace.StartSpan(ctx, "TxPool.Pending")
	defer span.End()
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[common.Address]types.Transactions, len(pool.pending))
	for addr, list := range pool.pending {
		pending[addr] = list.Flatten()
	}
	return pending
}

// PendingList is like Pending, but only txs.
func (pool *TxPool) PendingList(ctx context.Context) types.Transactions {
	ctx, span := trace.StartSpan(ctx, "TxPool.PendingList")
	defer span.End()
	pool.mu.Lock()
	defer pool.mu.Unlock()

	var pending types.Transactions
	for _, list := range pool.pending {
		list.txs.ensureCache()
		pending = append(pending, list.txs.cache...)
	}
	return pending
}

// local retrieves all currently known local transactions. The returned
// transaction set is a copy and can be freely modified by calling code.
func (pool *TxPool) local() (int, types.Transactions) {
	var acts int
	var txs types.Transactions
	for addr := range pool.locals.accounts {
		var ok bool
		if pending := pool.pending[addr]; pending != nil {
			ok = true
			pending.txs.ensureCache()
			txs = append(txs, pending.txs.cache...)
		}
		if queued := pool.queue[addr]; queued != nil {
			ok = true
			queued.txs.ensureCache()
			txs = append(txs, queued.txs.cache...)
		}
		if ok {
			acts++
		}
	}
	return acts, txs
}

// preValidateTx does preliminary transaction validation (a subset of validateTx), without requiring pool.mu to be held.
func (pool *TxPool) preValidateTx(ctx context.Context, tx *types.Transaction, local bool) error {
	// Heuristic limit, reject transactions over 32KB to prevent DOS attacks
	if tx.Size() > 32*1024 {
		return ErrOversizedData
	}
	// Transactions can't be negative. This may never happen using RLP decoded
	// transactions but may occur if you create a transaction using the RPC.
	if tx.Value().Sign() < 0 {
		return ErrNegativeValue
	}
	// Make sure the transaction is signed properly
	_, err := types.Sender(ctx, pool.signer, tx)
	if err != nil {
		return ErrInvalidSender
	}
	return nil
}

// validateTx checks whether a transaction is valid according to the consensus
// rules and adheres to some heuristic limits of the local node (price and size).
// The caller must hold pool.mu.
func (pool *TxPool) validateTx(ctx context.Context, tx *types.Transaction, local bool) error {
	ctx, span := trace.StartSpan(ctx, "TxPool.validateTx")
	defer span.End()

	// Heuristic limit, reject transactions over 32KB to prevent DOS attacks
	if tx.Size() > 32*1024 {
		return ErrOversizedData
	}
	// Transactions can't be negative. This may never happen using RLP decoded
	// transactions but may occur if you create a transaction using the RPC.
	if tx.Value().Sign() < 0 {
		return ErrNegativeValue
	}
	// Ensure the transaction doesn't exceed the current block limit gas.
	if pool.currentMaxGas < tx.Gas() {
		return ErrGasLimit
	}
	// Make sure the transaction is signed properly
	from, err := types.Sender(ctx, pool.signer, tx)
	if err != nil {
		return ErrInvalidSender
	}
	// Drop non-local transactions under our own minimal accepted gas price
	local = local || pool.locals.contains(from) // account may be local even if the transaction arrived from the network
	if !local && tx.CmpGasPrice(pool.gasPrice) < 0 {
		return ErrUnderpriced
	}
	// Ensure the transaction adheres to nonce ordering
	if pool.currentState.GetNonce(from) > tx.Nonce() {
		return ErrNonceTooLow
	}
	// Transactor should have enough funds to cover the costs
	// cost == V + GP * GL
	if pool.currentState.GetBalance(from).Cmp(tx.Cost()) < 0 {
		return ErrInsufficientFunds
	}
	intrGas, err := IntrinsicGas(tx.Data(), tx.To() == nil, pool.homestead)
	if err != nil {
		return err
	}
	if tx.Gas() < intrGas {
		return ErrIntrinsicGas
	}
	return nil
}

// add validates a transaction and inserts it into the non-executable queue for
// later pending promotion and execution. If the transaction is a replacement for
// an already pending or queued one, it overwrites the previous and returns this
// so outer code doesn't uselessly call promote.
//
// If a newly added transaction is marked as local, its sending account will be
// whitelisted, preventing any associated transaction from being dropped out of
// the pool due to pricing constraints.
func (pool *TxPool) add(ctx context.Context, tx *types.Transaction, local bool) (bool, error) {
	ctx, span := trace.StartSpan(ctx, "TxPool.add")
	defer span.End()

	t := time.Now()
	// If the transaction is already known, discard it.
	hash := tx.Hash()
	if pool.all.Get(hash) != nil {
		if log.Tracing() {
			log.Trace("Discarding already known transaction", "hash", hash)
		}
		return false, fmt.Errorf("known transaction: %x", hash)
	}
	// If the transaction fails basic validation, discard it.
	if err := pool.validateTx(ctx, tx, local); err != nil {
		if log.Tracing() {
			log.Trace("Discarding invalid transaction", "hash", hash, "err", err)
		}
		invalidTxCounter.Inc(1)
		span.SetStatus(trace.Status{
			Code:    trace.StatusCodeFailedPrecondition,
			Message: err.Error(),
		})
		return false, err
	}
	// If the transaction pool is full, reject.
	if uint64(pool.all.Count()) >= pool.config.GlobalSlots+pool.config.GlobalQueue {
		return false, ErrPoolLimit
	}
	// If the transaction is replacing an already pending one, do directly
	from, _ := types.Sender(ctx, pool.signer, tx) // already validated
	if pending := pool.pending[from]; pending != nil && pending.Overlaps(tx) {
		// Nonce already pending, check if required price bump is met
		inserted, old := pending.Add(tx, pool.config.PriceBump)
		if !inserted {
			pendingDiscardCounter.Inc(1)
			return false, ErrReplaceUnderpriced
		}
		// New transaction is better, replace old one
		if old != nil {
			pool.all.Remove(old.Hash())
			pendingReplaceCounter.Inc(1)
		}
		pool.all.Add(tx)
		pool.journalTx(from, tx)

		if log.Tracing() {
			log.Trace("Pooled new executable transaction", "hash", hash, "from", from, "to", tx.To())
		}
		poolAddTimer.UpdateSince(t)

		// We've directly injected a replacement transaction, notify subsystems
		pool.queueFeedSend(tx)

		return old != nil, nil
	}
	// New transaction isn't replacing a pending one, push into queue
	replace, err := pool.enqueueTx(ctx, tx)
	if err != nil {
		return false, err
	}
	// Mark local addresses and journal local transactions
	if local {
		pool.locals.add(from)
	}
	pool.journalTx(from, tx)

	if log.Tracing() {
		log.Trace("Pooled new future transaction", "hash", hash, "from", from, "to", tx.To())
	}
	poolAddTimer.UpdateSince(t)

	return replace, nil
}

// enqueueTx inserts a new transaction into the non-executable transaction queue.
//
// Caller must hold pool.mu.
func (pool *TxPool) enqueueTx(ctx context.Context, tx *types.Transaction) (bool, error) {
	ctx, span := trace.StartSpan(ctx, "TxPool.enqueueTx")
	defer span.End()

	// Try to insert the transaction into the future queue
	from, _ := types.Sender(ctx, pool.signer, tx) // already validated
	if pool.queue[from] == nil {
		pool.queue[from] = newTxList(false)
	}
	inserted, old := pool.queue[from].Add(tx, pool.config.PriceBump)
	if !inserted {
		// An older transaction was better, discard this
		queuedDiscardCounter.Inc(1)
		return false, ErrReplaceUnderpriced
	}
	// Discard any previous transaction and mark this
	if old != nil {
		pool.all.Remove(old.Hash())
		queuedReplaceCounter.Inc(1)
	}
	pool.all.Add(tx)
	return old != nil, nil
}

// journalTx adds the specified transaction to the local disk journal if it is
// deemed to have been sent from a local account.
func (pool *TxPool) journalTx(from common.Address, tx *types.Transaction) {
	// Only journal if it's enabled and the transaction is local
	if pool.journal == nil || !pool.locals.contains(from) {
		return
	}

	t := time.Now()
	if err := pool.journal.insert(tx); err != nil {
		log.Warn("Failed to journal local transaction", "err", err)
		return
	}
	journalInsertTimer.UpdateSince(t)
}

// promoteTx adds a transaction to the pending (processable) list of transactions
// and returns whether it was inserted or an older was better.
//
// Note, this method assumes the pool lock is held!
func (pool *TxPool) promoteTx(ctx context.Context, addr common.Address, hash common.Hash, tx *types.Transaction) bool {
	ctx, span := trace.StartSpan(ctx, "TxPool.promoteTx")
	defer span.End()

	// Try to insert the transaction into the pending queue
	if pool.pending[addr] == nil {
		pool.pending[addr] = newTxList(true)
	}
	inserted, old := pool.pending[addr].Add(tx, pool.config.PriceBump)
	if !inserted {
		// An older transaction was better, discard this
		pool.all.Remove(hash)

		pendingDiscardCounter.Inc(1)
		return false
	}
	// Otherwise discard any previous transaction and mark this
	if old != nil {
		pool.all.Remove(old.Hash())

		pendingReplaceCounter.Inc(1)
	}
	// Failsafe to work around direct pending inserts (tests)
	if pool.all.Get(hash) == nil {
		pool.all.Add(tx)
	}
	// Set the potentially new pending nonce and notify any subsystems of the new tx
	pool.beats[addr] = time.Now()
	pool.pendingState.SetNonce(addr, tx.Nonce()+1)

	return true
}

// AddLocal enqueues a single transaction into the pool if it is valid, marking
// the sender as a local one in the mean time, ensuring it goes around the local
// pricing constraints.
func (pool *TxPool) AddLocal(ctx context.Context, tx *types.Transaction) error {
	return pool.addTx(ctx, tx, !pool.config.NoLocals)
}

// AddRemote enqueues a single transaction into the pool if it is valid. If the
// sender is not among the locally tracked ones, full pricing constraints will
// apply.
func (pool *TxPool) AddRemote(ctx context.Context, tx *types.Transaction) error {
	return pool.addTx(ctx, tx, false)
}

// AddLocals enqueues a batch of transactions into the pool if they are valid,
// marking the senders as a local ones in the mean time, ensuring they go around
// the local pricing constraints.
func (pool *TxPool) AddLocals(ctx context.Context, txs []*types.Transaction) []error {
	return pool.addTxs(ctx, txs, !pool.config.NoLocals)
}

// AddRemotes enqueues a batch of transactions into the pool if they are valid.
// If the senders are not among the locally tracked ones, full pricing constraints
// will apply.
func (pool *TxPool) AddRemotes(ctx context.Context, txs []*types.Transaction) []error {
	return pool.addTxs(ctx, txs, false)
}

// addTx enqueues a single transaction into the pool if it is valid.
func (pool *TxPool) addTx(ctx context.Context, tx *types.Transaction, local bool) error {
	ctx, span := trace.StartSpan(ctx, "TxPool.addTx")
	defer span.End()

	// Check if the transaction is already known, before locking the whole pool.
	if pool.all.Get(tx.Hash()) != nil {
		return fmt.Errorf("known tx: %x", tx.Hash())
	}
	// If the transaction fails basic validation, discard it.
	if err := pool.preValidateTx(ctx, tx, local); err != nil {
		if log.Tracing() {
			log.Trace("Discarding invalid transaction", "hash", tx.Hash(), "err", err)
		}
		invalidTxCounter.Inc(1)
		span.SetStatus(trace.Status{
			Code:    trace.StatusCodeFailedPrecondition,
			Message: err.Error(),
		})
		return err
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Try to inject the transaction and update any state
	replace, err := pool.add(ctx, tx, local)
	if err != nil {
		return err
	}
	// If we added a new transaction, run promotion checks and return
	if !replace {
		from, _ := types.Sender(ctx, pool.signer, tx) // already validated
		pool.promoteExecutables(ctx, from)
	}
	return nil
}

// addTxs attempts to queue a batch of transactions if they are valid.
func (pool *TxPool) addTxs(ctx context.Context, txs []*types.Transaction, local bool) []error {
	ctx, span := trace.StartSpan(ctx, "TxPool.addTxs")
	defer span.End()

	var add []*types.Transaction
	// Filter out known, and pre-compute/cache signer before locking.
	for _, tx := range txs {
		// If the transaction is already known, discard it.
		if pool.all.Get(tx.Hash()) != nil {
			continue
		}
		// If the transaction fails basic validation, discard it.
		if err := pool.preValidateTx(ctx, tx, local); err != nil {
			if log.Tracing() {
				log.Trace("Discarding invalid transaction", "hash", tx.Hash(), "err", err)
			}
			invalidTxCounter.Inc(1)
			continue
		}
		add = append(add, tx)
	}
	if len(add) == 0 {
		return nil
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	return pool.addTxsLocked(ctx, add, local)
}

// addTxsLocked attempts to queue a batch of transactions if they are valid,
// whilst assuming the transaction pool lock is already held.
func (pool *TxPool) addTxsLocked(ctx context.Context, txs []*types.Transaction, local bool) []error {
	ctx, span := trace.StartSpan(ctx, "TxPool.addTxsLocked")
	defer span.End()

	// Add the batch of transaction, tracking the accepted ones
	dirty := make(map[common.Address]struct{})
	var errs []error

	for _, tx := range txs {
		replace, err := pool.add(ctx, tx, local)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !replace {
			from, _ := types.Sender(ctx, pool.signer, tx) // already validated
			dirty[from] = struct{}{}
		}
	}
	// Only reprocess the internal state if something was actually added
	if len(dirty) > 0 {
		addrs := make([]common.Address, 0, len(dirty))
		for addr := range dirty {
			addrs = append(addrs, addr)
		}
		pool.promoteExecutables(ctx, addrs...)
	}
	return errs
}

// reinject is like addTxsLocked but with a map and local false.
func (pool *TxPool) reinject(ctx context.Context, txs map[common.Hash]*types.Transaction) []error {
	ctx, span := trace.StartSpan(ctx, "TxPool.reinject")
	defer span.End()

	// Add the batch of transaction, tracking the accepted ones
	dirty := make(map[common.Address]struct{})
	var errs []error

	for _, tx := range txs {
		replace, err := pool.add(ctx, tx, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !replace {
			from, _ := types.Sender(ctx, pool.signer, tx) // already validated
			dirty[from] = struct{}{}
		}
	}
	// Only reprocess the internal state if something was actually added
	if len(dirty) > 0 {
		addrs := make([]common.Address, 0, len(dirty))
		for addr := range dirty {
			addrs = append(addrs, addr)
		}
		pool.promoteExecutables(ctx, addrs...)
	}
	return errs
}

// Status returns the status (unknown/pending/queued) of a batch of transactions
// identified by their hashes.
func (pool *TxPool) Status(ctx context.Context, hashes []common.Hash) []TxStatus {
	ctx, span := trace.StartSpan(ctx, "TxPool.Status")
	defer span.End()

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	status := make([]TxStatus, len(hashes))
	for i, hash := range hashes {
		if tx := pool.all.Get(hash); tx != nil {
			from, _ := types.Sender(ctx, pool.signer, tx) // already validated
			if pool.pending[from] != nil && pool.pending[from].txs.items[tx.Nonce()] != nil {
				status[i] = TxStatusPending
			} else {
				status[i] = TxStatusQueued
			}
		}
	}
	return status
}

// Get returns a transaction if it is contained in the pool
// and nil otherwise.
func (pool *TxPool) Get(hash common.Hash) *types.Transaction {
	return pool.all.Get(hash)
}

// removeTx removes a single transaction from pending or queue, moving all subsequent
// transactions back to the future queue.
// The caller must hold pool.mu and pool.all.mu.
func (pool *TxPool) removeTx(ctx context.Context, tx *types.Transaction) {
	ctx, span := trace.StartSpan(ctx, "TxPool.removeTx")
	defer span.End()

	delete(pool.all.all, tx.Hash())

	addr, _ := types.Sender(ctx, pool.signer, tx) // already validated during insertion

	// Remove the transaction from the pending lists and reset the account nonce.
	if pending := pool.pending[addr]; pending != nil {
		queue := pool.queue[addr]
		if queue == nil {
			// Create a new queue for any invalidated pending txs. Will be discarded before returning if unused.
			queue = newTxList(false)
			pool.queue[addr] = queue
		}
		// Remove from pending, and demote any invalidated to the queue.
		if pending.Remove(tx, queue.add) {
			// If no more transactions are left, remove the list.
			if pending.Empty() {
				delete(pool.pending, addr)
				delete(pool.beats, addr)
			}
			// If we created a new queue, but no txs were invalidated, then remove it.
			if queue.Empty() {
				delete(pool.queue, addr)
			}
			// Update the account nonce, if necessary.
			stNonce := pool.pendingState.GetNonce(addr)
			if nonce := tx.Nonce(); stNonce > nonce {
				pool.pendingState.SetNonce(addr, nonce)
			}
			return
		}
	}
	// Transaction is in the future queue.
	if queue := pool.queue[addr]; queue != nil {
		_ = queue.Remove(tx, func(tx *types.Transaction) {})
		if queue.Empty() {
			delete(pool.queue, addr)
		}
	}
}

// promoteExecutablesAll is like promoteExecutables, but for the entire queue.
func (pool *TxPool) promoteExecutablesAll(ctx context.Context) {
	ctx, span := trace.StartSpan(ctx, "TxPool.promoteExecutablesAll")
	defer span.End()

	for addr, queued := range pool.queue {
		pool.promoteExecutable(ctx, addr, queued)
	}
	pool.finishPromotion(ctx)
}

// promoteExecutables moves transactions that have become processable from the
// future queue to the set of pending transactions. During this process, all
// invalidated transactions (low nonce, low balance) are deleted.
func (pool *TxPool) promoteExecutables(ctx context.Context, accounts ...common.Address) {
	ctx, span := trace.StartSpan(ctx, "TxPool.promoteExecutables")
	defer span.End()

	for _, addr := range accounts {
		queued := pool.queue[addr]
		if queued == nil {
			continue // Just in case someone calls with a non existing account
		}
		pool.promoteExecutable(ctx, addr, queued)
	}
	pool.finishPromotion(ctx)
}

func (pool *TxPool) promoteExecutable(ctx context.Context, addr common.Address, queued *txList) {
	ctx, span := trace.StartSpan(ctx, "TxPool.promoteExecutable")
	defer span.End()

	tracing := log.Tracing()
	// Drop all transactions that are deemed too old (low nonce)
	remove := func(tx *types.Transaction) {
		pool.all.Remove(tx.Hash())
	}
	if tracing {
		remove = func(tx *types.Transaction) {
			hash := tx.Hash()
			pool.all.Remove(hash)
			log.Trace("Removed old queued transaction", "hash", hash)
		}
	}
	queued.Forward(pool.currentState.GetNonce(addr), remove)

	// Drop all transactions that are too costly (low balance or out of gas)
	remove = func(tx *types.Transaction) {
		pool.all.Remove(tx.Hash())
		queuedNofundsCounter.Inc(1)
	}
	if tracing {
		remove = func(tx *types.Transaction) {
			hash := tx.Hash()
			pool.all.Remove(hash)
			queuedNofundsCounter.Inc(1)
			log.Trace("Removed unpayable queued transaction", "hash", hash)
		}
	}
	queued.Filter(pool.currentState.GetBalance(addr), pool.currentMaxGas, remove, func(*types.Transaction) {})

	// Gather all executable transactions and promote them
	promote := func(tx *types.Transaction) {
		if pool.promoteTx(ctx, addr, tx.Hash(), tx) {
			pool.queueFeedSend(tx)
		}
	}
	if tracing {
		promote = func(tx *types.Transaction) {
			hash := tx.Hash()
			if pool.promoteTx(ctx, addr, hash, tx) {
				log.Trace("Promoting queued transaction", "hash", hash)
				pool.queueFeedSend(tx)
			} else {
				log.Trace("Removed old queued transaction", "hash", hash)
			}
		}
	}
	queued.Ready(pool.pendingState.GetNonce(addr), promote)

	// Drop all transactions over the allowed limit
	if !pool.locals.contains(addr) {
		remove := func(tx *types.Transaction) {
			pool.all.Remove(tx.Hash())
			queuedRateLimitCounter.Inc(1)
		}
		if tracing {
			remove = func(tx *types.Transaction) {
				hash := tx.Hash()
				pool.all.Remove(hash)
				queuedRateLimitCounter.Inc(1)
				log.Trace("Removed cap-exceeding queued transaction", "hash", hash)
			}
		}
		queued.Cap(int(pool.config.AccountQueue), remove)
	}

	// Delete the entire queued entry if it became empty.
	if queued.Empty() {
		delete(pool.queue, addr)
	}
}

func (pool *TxPool) finishPromotion(ctx context.Context) {
	ctx, span := trace.StartSpan(ctx, "TxPool.finishPromotion")
	defer span.End()

	// If the pending limit is overflown, start equalizing allowances
	pending := uint64(0)
	for _, list := range pool.pending {
		pending += uint64(list.Len())
	}
	if pending > pool.config.GlobalSlots {
		pendingBeforeCap := pending
		// Assemble a spam order to penalize large transactors first
		spammers := prque.New(nil)
		for addr, list := range pool.pending {
			// Only evict transactions from high rollers
			if !pool.locals.contains(addr) && uint64(list.Len()) > pool.config.AccountSlots {
				spammers.Push(addr, int64(list.Len()))
			}
		}
		tracing := log.Tracing()
		// Gradually drop transactions from offenders
		offenders := []common.Address{}
		for pending > pool.config.GlobalSlots && !spammers.Empty() {
			// Retrieve the next offender if not local address
			offender, _ := spammers.Pop()
			offenders = append(offenders, offender.(common.Address))

			// Equalize balances until all the same or below threshold
			if len(offenders) > 1 {
				// Calculate the equalization threshold for all current offenders
				threshold := pool.pending[offender.(common.Address)].Len()

				// Iteratively reduce all offenders until below limit or threshold reached
				for pending > pool.config.GlobalSlots && pool.pending[offenders[len(offenders)-2]].Len() > threshold {
					pending -= pool.limitOffenders(offenders, tracing)
				}
			}
		}
		// If still above threshold, reduce to limit or min allowance
		if pending > pool.config.GlobalSlots && len(offenders) > 0 {
			for pending > pool.config.GlobalSlots && uint64(pool.pending[offenders[len(offenders)-1]].Len()) > pool.config.AccountSlots {
				pending -= pool.limitOffenders(offenders, tracing)
			}
		}
		pendingRateLimitCounter.Inc(int64(pendingBeforeCap - pending))
	}
	// If we've queued more transactions than the hard limit, drop oldest ones
	queued := uint64(0)
	for _, list := range pool.queue {
		queued += uint64(list.Len())
	}
	if queued > pool.config.GlobalQueue {
		// Sort all accounts with queued transactions by heartbeat
		addresses := make(addresssByHeartbeat, 0, len(pool.queue))
		for addr := range pool.queue {
			if !pool.locals.contains(addr) { // don't drop locals
				addresses = append(addresses, addressByHeartbeat{addr, pool.beats[addr]})
			}
		}
		sort.Sort(addresses)

		// Drop transactions until the total is below the limit or only locals remain
		for drop := queued - pool.config.GlobalQueue; drop > 0 && len(addresses) > 0; {
			addr := addresses[len(addresses)-1]
			list := pool.queue[addr.address]

			addresses = addresses[:len(addresses)-1]

			// Drop all transactions if they are less than the overflow
			if size := uint64(list.Len()); size <= drop {
				for _, tx := range list.txs.items {
					pool.all.Remove(tx.Hash())
				}
				delete(pool.queue, addr.address)
				drop -= size
				queuedRateLimitCounter.Inc(int64(size))
				continue
			}
			// Otherwise drop only last few transactions
			list.ForLast(int(drop), func(tx *types.Transaction) {
				pool.all.Remove(tx.Hash())
				drop--
				queuedRateLimitCounter.Inc(1)
			})
		}
	}
}

func (pool *TxPool) limitOffenders(offenders []common.Address, tracing bool) (removed uint64) {
	var nonce uint64
	remove := func(tx *types.Transaction) {
		pool.all.Remove(tx.Hash())
		if tx.Nonce() < nonce {
			nonce = tx.Nonce()
		}
	}
	if tracing {
		remove = func(tx *types.Transaction) {
			hash := tx.Hash()
			pool.all.Remove(hash)
			if tx.Nonce() < nonce {
				nonce = tx.Nonce()
			}
			log.Trace("Removed fairness-exceeding pending transaction", "hash", hash)
		}
	}
	for _, addr := range offenders {
		removed++
		pending := pool.pending[addr]
		nonce = math.MaxUint64
		pending.Cap(pending.Len()-1, remove)
		// Update the account nonce to the dropped transaction
		stNonce := pool.pendingState.GetNonce(addr)
		if stNonce > nonce {
			pool.pendingState.SetNonce(addr, nonce)
		}
	}
	return
}

// demoteUnexecutables removes invalid and processed transactions from the pools
// executable/pending queue and any subsequent transactions that become unexecutable
// are moved back into the future queue.
func (pool *TxPool) demoteUnexecutables(ctx context.Context) {
	ctx, span := trace.StartSpan(ctx, "TxPool.demoteUnexecutables")
	defer span.End()

	// Iterate over all accounts and demote any non-executable transactions
	for addr, pending := range pool.pending {
		nonce := pool.currentState.GetNonce(addr)

		// Drop all transactions that are deemed too old (low nonce)
		tracing := log.Tracing()
		remove := func(tx *types.Transaction) {
			pool.all.Remove(tx.Hash())
		}
		if tracing {
			remove = func(tx *types.Transaction) {
				hash := tx.Hash()
				pool.all.Remove(hash)
				log.Trace("Removed old pending transaction", "hash", hash)
			}
		}
		pending.Forward(nonce, remove)

		// Drop all transactions that are too costly (low balance or out of gas), and queue any invalids back for later
		bal := pool.currentState.GetBalance(addr)
		remove = func(tx *types.Transaction) {
			pool.all.Remove(tx.Hash())
			pendingNofundsCounter.Inc(1)
		}
		queue := pool.queue[addr]
		if queue == nil {
			queue = newTxList(false)
			pool.queue[addr] = queue
		}
		invalid := queue.add
		if tracing {
			remove = func(tx *types.Transaction) {
				hash := tx.Hash()
				pool.all.Remove(hash)
				pendingNofundsCounter.Inc(1)
				log.Trace("Removed unpayable pending transaction", "hash", hash)
			}
			invalid = func(tx *types.Transaction) {
				log.Trace("Demoting pending transaction", "hash", tx.Hash())
				queue.add(tx)
			}
		}
		pending.Filter(bal, pool.currentMaxGas, remove, invalid)

		// If there's a gap in front, warn (should never happen) and postpone all transactions
		if pending.Len() > 0 && pending.txs.Get(nonce) == nil {
			for _, tx := range pending.txs.items {
				log.Trace("Demoting invalidated transaction", "hash", tx.Hash())
				queue.add(tx)
			}
			delete(pool.pending, addr)
			delete(pool.beats, addr)
			continue
		}
		if pending.Empty() {
			delete(pool.pending, addr)
			delete(pool.beats, addr)
		}
		if queue.Empty() {
			delete(pool.queue, addr)
		}
	}
}

// addressByHeartbeat is an account address tagged with its last activity timestamp.
type addressByHeartbeat struct {
	address   common.Address
	heartbeat time.Time
}

type addresssByHeartbeat []addressByHeartbeat

func (a addresssByHeartbeat) Len() int           { return len(a) }
func (a addresssByHeartbeat) Less(i, j int) bool { return a[i].heartbeat.Before(a[j].heartbeat) }
func (a addresssByHeartbeat) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// accountSet is simply a set of addresses to check for existence, and a signer
// capable of deriving addresses from transactions.
type accountSet struct {
	accounts map[common.Address]struct{}
	signer   types.Signer
}

// newAccountSet creates a new address set with an associated signer for sender
// derivations.
func newAccountSet(signer types.Signer) *accountSet {
	return &accountSet{
		accounts: make(map[common.Address]struct{}),
		signer:   signer,
	}
}

// contains checks if a given address is contained within the set.
func (as *accountSet) contains(addr common.Address) bool {
	_, exist := as.accounts[addr]
	return exist
}

// containsTx checks if the sender of a given tx is within the set. If the sender
// cannot be derived, this method returns false.
func (as *accountSet) containsTx(ctx context.Context, tx *types.Transaction) bool {
	if addr, err := types.Sender(ctx, as.signer, tx); err == nil {
		return as.contains(addr)
	}
	return false
}

// add inserts a new address into the set to track.
func (as *accountSet) add(addr common.Address) {
	as.accounts[addr] = struct{}{}
}

// txLookup is used internally by TxPool to track transactions while allowing lookup without
// mutex contention.
//
// Note, although this type is properly protected against concurrent access, it
// is **not** a type that should ever be mutated or even exposed outside of the
// transaction pool, since its internal state is tightly coupled with the pools
// internal mechanisms. The sole purpose of the type is to permit out-of-bound
// peeking into the pool in TxPool.Get without having to acquire the widely scoped
// TxPool.mu mutex.
type txLookup struct {
	all map[common.Hash]*types.Transaction
	mu  sync.RWMutex
}

// newTxLookup returns a new txLookup structure.
func newTxLookup(cap int) *txLookup {
	return &txLookup{
		all: make(map[common.Hash]*types.Transaction, cap),
	}
}

// ForEach calls f for each tx, while holding the write lock.
func (t *txLookup) ForEach(f func(tx *types.Transaction)) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, value := range t.all {
		f(value)
	}
}

// Get returns a transaction if it exists in the lookup, or nil if not found.
func (t *txLookup) Get(hash common.Hash) *types.Transaction {
	t.mu.RLock()
	tx := t.all[hash]
	t.mu.RUnlock()
	return tx
}

// Count returns the current number of items in the lookup.
func (t *txLookup) Count() int {
	t.mu.RLock()
	l := len(t.all)
	t.mu.RUnlock()
	return l
}

// Add adds a transaction to the lookup.
func (t *txLookup) Add(tx *types.Transaction) {
	t.mu.Lock()
	t.all[tx.Hash()] = tx
	t.mu.Unlock()
}

// Remove removes a transaction from the lookup.
func (t *txLookup) Remove(hash common.Hash) {
	t.mu.Lock()
	delete(t.all, hash)
	t.mu.Unlock()
}
