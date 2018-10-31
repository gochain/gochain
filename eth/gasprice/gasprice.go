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

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/params"
	"github.com/gochain-io/gochain/rpc"
)

var (
	Default  = new(big.Int).SetUint64(2 * params.Shannon)
	maxPrice = big.NewInt(500 * params.Shannon)
)

// Backend is a subset of the methods from the interface ethapi.Backend.
type Backend interface {
	HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error)
	BlockByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Block, error)
	ChainConfig() *params.ChainConfig
}

type Config struct {
	Blocks     int
	Percentile int
	Default    *big.Int `toml:",omitempty"`
}

// Oracle recommends gas prices based on the content of recent
// blocks. Suitable for both light and full clients.
type Oracle struct {
	backend Backend
	cfg     Config

	lastMu    sync.RWMutex
	lastHead  common.Hash
	lastPrice *big.Int

	fetchLock sync.Mutex
}

// NewOracle returns a new oracle.
func NewOracle(backend Backend, cfg Config) *Oracle {
	if cfg.Blocks < 1 {
		cfg.Blocks = 1
	}
	if cfg.Percentile < 0 {
		cfg.Percentile = 0
	} else if cfg.Percentile > 100 {
		cfg.Percentile = 100
	}
	if cfg.Default == nil {
		cfg.Default = Default
	}
	return &Oracle{
		backend: backend,
		cfg:     cfg,
	}
}

// SuggestPrice returns the recommended gas price.
func (gpo *Oracle) SuggestPrice(ctx context.Context) (*big.Int, error) {
	gpo.lastMu.RLock()
	lastHead := gpo.lastHead
	lastPrice := gpo.lastPrice
	gpo.lastMu.RUnlock()

	head, _ := gpo.backend.HeaderByNumber(ctx, rpc.LatestBlockNumber)
	headHash := head.Hash()
	if headHash == lastHead {
		return lastPrice, nil
	}

	gpo.fetchLock.Lock()
	defer gpo.fetchLock.Unlock()

	// try checking the cache again, maybe the last fetch fetched what we need
	gpo.lastMu.RLock()
	lastHead = gpo.lastHead
	lastPrice = gpo.lastPrice
	gpo.lastMu.RUnlock()
	if headHash == lastHead {
		return lastPrice, nil
	}

	// Calculate block prices concurrently.
	results := make(chan result, gpo.cfg.Blocks)
	blocks := 0
	for blockNum := head.Number.Uint64(); blocks < gpo.cfg.Blocks && blockNum > 0; blockNum-- {
		blocks++
		go gpo.fetchMinBlockPrice(ctx, blockNum, results)
	}
	if blocks == 0 {
		return gpo.cfg.Default, nil
	}

	// Collect results.
	blockPrices := make([]*big.Int, blocks)
	for i := 0; i < blocks; i++ {
		res := <-results
		if res.err != nil {
			return gpo.cfg.Default, res.err
		}
		if res.price == nil {
			res.price = gpo.cfg.Default
		}
		blockPrices[i] = res.price
	}
	sort.Sort(bigIntArray(blockPrices))
	price := blockPrices[(len(blockPrices)-1)*gpo.cfg.Percentile/100]

	if price.Cmp(maxPrice) > 0 {
		price = new(big.Int).Set(maxPrice)
	}

	gpo.lastMu.Lock()
	gpo.lastHead = headHash
	gpo.lastPrice = price
	gpo.lastMu.Unlock()
	return price, nil
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
