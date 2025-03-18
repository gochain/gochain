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

package gasprice

import (
	"context"
	"math/big"
	"sort"
	"sync"

	"github.com/gochain/gochain/v4/common"
	"github.com/gochain/gochain/v4/core/types"
	"github.com/gochain/gochain/v4/log"
	"github.com/gochain/gochain/v4/params"
	"github.com/gochain/gochain/v4/rpc"
)

var (
	// Deprecated: GasPricer.GasPrice()
	Default         = new(big.Int).SetUint64(1000 * params.Shannon)
	DefaultMaxPrice = big.NewInt(500000 * params.Shannon)
)

// For testing purposes
type DefaultPricer struct {
}

func (d *DefaultPricer) GasPrice(ctx context.Context) (*big.Int, error) {
	return Default, nil
}

func (d *DefaultPricer) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return Default, nil
}

type Config struct {
	Blocks      int
	Percentile  int
	Default     *big.Int `toml:",omitempty"` // nil for default/dynamic
	MaxPrice    *big.Int `toml:",omitempty"`
	GasContract string   `toml:",omitempty"` // address of the GIP-38 contract
}

// OracleBackend includes all necessary background APIs for oracle.
type OracleBackend interface {
	HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error)
	BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error)
	ChainConfig() *params.ChainConfig
}

// Oracle recommends gas prices based on the content of recent
// blocks. Suitable for both light and full clients.
type Oracle struct {
	backend      OracleBackend
	lastHead     common.Hash
	defaultPrice *big.Int // optional user-configured default/min
	lastPrice    *big.Int
	maxPrice     *big.Int
	gasContract  string // address to ask for price
	cacheLock    sync.RWMutex
	fetchLock    sync.Mutex

	checkBlocks int
	percentile  int
}

// NewOracle returns a new gasprice oracle which can recommend suitable
// gasprice for newly created transaction.
func NewOracle(backend OracleBackend, params Config) *Oracle {
	blocks := params.Blocks
	if blocks < 1 {
		blocks = 1
		log.Warn("Sanitizing invalid gasprice oracle sample blocks", "provided", params.Blocks, "updated", blocks)
	}
	percent := params.Percentile
	if percent < 0 {
		percent = 0
		log.Warn("Sanitizing invalid gasprice oracle sample percentile", "provided", params.Percentile, "updated", percent)
	}
	if percent > 100 {
		percent = 100
		log.Warn("Sanitizing invalid gasprice oracle sample percentile", "provided", params.Percentile, "updated", percent)
	}
	maxPrice := params.MaxPrice
	if maxPrice == nil || maxPrice.Int64() <= 0 {
		maxPrice = DefaultMaxPrice
		log.Warn("Sanitizing invalid gasprice oracle price cap", "provided", params.MaxPrice, "updated", maxPrice)
	}
	lastPrice := params.Default
	if lastPrice == nil {
		lastPrice = Default
	} else if maxPrice.Int64() <= 0 {
		lastPrice = Default
		log.Warn("Sanitizing invalid gasprice oracle price default", "provided", params.Default, "updated", lastPrice)
	}
	return &Oracle{
		backend:      backend,
		defaultPrice: params.Default,
		lastPrice:    lastPrice,
		maxPrice:     maxPrice,
		checkBlocks:  blocks,
		percentile:   percent,
		gasContract:  params.GasContract,
	}
}

// SuggestPrice returns a gasprice so that newly created transaction can
// have a very high chance to be included in the following blocks.
func (gpo *Oracle) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	head, _ := gpo.backend.HeaderByNumber(ctx, rpc.LatestBlockNumber)
	headHash := head.Hash()

	// If the latest gasprice is still available, return it.
	gpo.cacheLock.RLock()
	lastHead, lastPrice := gpo.lastHead, gpo.lastPrice
	gpo.cacheLock.RUnlock()
	if headHash == lastHead {
		return lastPrice, nil
	}
	gpo.fetchLock.Lock()
	defer gpo.fetchLock.Unlock()

	// Try checking the cache again, maybe the last fetch fetched what we need
	gpo.cacheLock.RLock()
	lastHead, lastPrice = gpo.lastHead, gpo.lastPrice
	gpo.cacheLock.RUnlock()
	if headHash == lastHead {
		return lastPrice, nil
	}

	// Calculate block prices concurrently.
	results := make(chan result, gpo.checkBlocks)
	blocks := 0
	for blockNum := head.Number.Uint64(); blocks < gpo.checkBlocks && blockNum > 0; blockNum-- {
		blocks++
		go gpo.fetchMinBlockPrice(ctx, blockNum, results)
	}
	if blocks == 0 {
		return lastPrice, nil
	}

	// Collect results.
	blockPrices := make([]*big.Int, blocks)
	for i := 0; i < blocks; i++ {
		res := <-results
		if res.err != nil {
			return lastPrice, res.err
		}
		if res.price == nil {
			res.price = lastPrice
		}
		blockPrices[i] = res.price
	}
	sort.Sort(bigIntArray(blockPrices))
	price := blockPrices[(len(blockPrices)-1)*gpo.percentile/100]
	if price.Cmp(gpo.maxPrice) > 0 {
		price = new(big.Int).Set(gpo.maxPrice)
	} else if min := gpo.minPrice(head.Number); price.Cmp(min) < 0 {
		price = min
	}
	gpo.cacheLock.Lock()
	gpo.lastHead = headHash
	gpo.lastPrice = price
	gpo.cacheLock.Unlock()
	return price, nil
}

func (gpo *Oracle) minPrice(num *big.Int) *big.Int {
	if gpo.gasContract != "" {
		// TODO: CALL CONTRACT!!
	}
	if gpo.defaultPrice != nil {
		return gpo.defaultPrice
	}
	const blockOffset = 60 * 12 // look ~1 hour ahead, since we are suggesting gas for near-future txs
	return Default
}

type result struct {
	price *big.Int
	err   error
}

// fetchMinBlockPrice responds on ch with the minimum gas price required to have been included in the block.
// Sends nil price if the block is not full, or all local txs. Sends an error if block look-up fails.
func (gpo *Oracle) fetchMinBlockPrice(ctx context.Context, blockNum uint64, ch chan<- result) {
	block, err := gpo.backend.BlockByNumber(ctx, rpc.BlockNumber(blockNum))
	if block == nil || err != nil {
		ch <- result{err: err}
		return
	}
	if block.GasUsed()+params.TxGas < block.GasLimit() {
		// Block wasn't full - room for at least one more transaction.
		ch <- result{}
		return
	}
	signer := types.MakeSigner(gpo.backend.ChainConfig(), new(big.Int).SetUint64(blockNum))
	ch <- result{price: minBlockPrice(ctx, signer, block)}
}

// minBlockPrice returns the lowest-priced, non-local transaction, or nil if none can be found.
func minBlockPrice(ctx context.Context, signer types.Signer, block *types.Block) *big.Int {
	var min *big.Int
	for _, tx := range block.Transactions() {
		sender, err := types.Sender(signer, tx)
		if err != nil || sender == block.Coinbase() {
			continue
		}
		if min == nil || tx.CmpGasPrice(min) < 0 {
			min = tx.GasPrice()
		}
	}
	return min
}

type bigIntArray []*big.Int

func (s bigIntArray) Len() int           { return len(s) }
func (s bigIntArray) Less(i, j int) bool { return s[i].Cmp(s[j]) < 0 }
func (s bigIntArray) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
