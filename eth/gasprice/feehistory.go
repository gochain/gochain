// Copyright 2021 The go-ethereum Authors
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
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/core/types"
	"github.com/gochain/gochain/v5/rpc"
)

var (
	errInvalidPercentile = errors.New("invalid reward percentile")
	errRequestBeyondHead = errors.New("request beyond head block")
)

const (
	maxBlockFetchers = 4
	maxQueryLimit    = 1024
)

// FeeHistory returns data relevant for fee estimation based on the specified range of blocks.
func (gpo *Oracle) FeeHistory(ctx context.Context, blocks int, unresolvedLastBlock rpc.BlockNumber, rewardPercentiles []float64) (*big.Int, [][]*big.Int, []*big.Int, []float64, error) {
	if blocks < 1 {
		return common.Big0, nil, nil, nil, nil
	}
	if blocks > maxQueryLimit {
		blocks = maxQueryLimit
	}
	for i, p := range rewardPercentiles {
		if p < 0 || p > 100 || (i > 0 && p < rewardPercentiles[i-1]) {
			return common.Big0, nil, nil, nil, fmt.Errorf("%w: %f", errInvalidPercentile, p)
		}
	}

	var (
		pendingBlock *types.Block
		lastBlock    uint64
	)
	if unresolvedLastBlock < 0 {
		switch unresolvedLastBlock {
		case rpc.PendingBlockNumber:
			if pending, err := gpo.backend.BlockByNumber(ctx, rpc.PendingBlockNumber); err == nil && pending != nil {
				pendingBlock = pending
				lastBlock = pending.NumberU64()
				break
			}
			// Fallback to latest block if pending is not available
			fallthrough
		case rpc.LatestBlockNumber, rpc.SafeBlockNumber, rpc.FinalizedBlockNumber:
			header, err := gpo.backend.HeaderByNumber(ctx, rpc.LatestBlockNumber)
			if err != nil {
				return common.Big0, nil, nil, nil, err
			}
			if header == nil {
				return common.Big0, nil, nil, nil, errRequestBeyondHead
			}
			lastBlock = header.Number.Uint64()
		case rpc.EarliestBlockNumber:
			lastBlock = 0
		default:
			return common.Big0, nil, nil, nil, fmt.Errorf("invalid block number: %d", unresolvedLastBlock)
		}
	} else {
		header, err := gpo.backend.HeaderByNumber(ctx, unresolvedLastBlock)
		if err != nil {
			return common.Big0, nil, nil, nil, err
		}
		if header == nil {
			return common.Big0, nil, nil, nil, errRequestBeyondHead
		}
		lastBlock = header.Number.Uint64()
	}

	var oldestBlock uint64
	if uint64(blocks) <= lastBlock+1 {
		oldestBlock = lastBlock + 1 - uint64(blocks)
	} else {
		oldestBlock = 0
		blocks = int(lastBlock + 1)
	}

	var reward [][]*big.Int
	if len(rewardPercentiles) > 0 {
		reward = make([][]*big.Int, blocks)
	}
	baseFee := make([]*big.Int, blocks+1)
	for i := range baseFee {
		baseFee[i] = new(big.Int)
	}
	gasUsedRatio := make([]float64, blocks)

	for i := 0; i < blocks; i++ {
		blockNumber := oldestBlock + uint64(i)
		if len(rewardPercentiles) > 0 {
			var (
				block *types.Block
				err   error
			)
			if pendingBlock != nil && blockNumber == pendingBlock.NumberU64() {
				block = pendingBlock
			} else {
				block, err = gpo.backend.BlockByNumber(ctx, rpc.BlockNumber(blockNumber))
				if err != nil {
					return common.Big0, nil, nil, nil, err
				}
			}
			if block == nil {
				return common.Big0, nil, nil, nil, errRequestBeyondHead
			}
			if block.GasLimit() > 0 {
				gasUsedRatio[i] = float64(block.GasUsed()) / float64(block.GasLimit())
			}
			reward[i] = computeBlockRewards(block, rewardPercentiles)
		} else {
			var (
				header *types.Header
				err    error
			)
			if pendingBlock != nil && blockNumber == pendingBlock.NumberU64() {
				header = pendingBlock.Header()
			} else {
				header, err = gpo.backend.HeaderByNumber(ctx, rpc.BlockNumber(blockNumber))
				if err != nil {
					return common.Big0, nil, nil, nil, err
				}
			}
			if header == nil {
				return common.Big0, nil, nil, nil, errRequestBeyondHead
			}
			if header.GasLimit > 0 {
				gasUsedRatio[i] = float64(header.GasUsed) / float64(header.GasLimit)
			}
		}
	}

	return new(big.Int).SetUint64(oldestBlock), reward, baseFee, gasUsedRatio, nil
}

func computeBlockRewards(block *types.Block, rewardPercentiles []float64) []*big.Int {
	rewards := make([]*big.Int, len(rewardPercentiles))
	txs := block.Transactions()
	if len(txs) == 0 {
		for i := range rewards {
			rewards[i] = new(big.Int)
		}
		return rewards
	}

	sortedTxs := make([]*types.Transaction, len(txs))
	copy(sortedTxs, txs)
	sort.Slice(sortedTxs, func(i, j int) bool {
		return sortedTxs[i].GasPrice().Cmp(sortedTxs[j].GasPrice()) < 0
	})

	var totalGas uint64
	for _, tx := range sortedTxs {
		totalGas += tx.Gas()
	}

	var (
		txIndex int
		sumGas  = sortedTxs[0].Gas()
	)
	for i, p := range rewardPercentiles {
		thresholdGas := uint64(float64(totalGas) * p / 100)
		for sumGas < thresholdGas && txIndex < len(sortedTxs)-1 {
			txIndex++
			sumGas += sortedTxs[txIndex].Gas()
		}
		rewards[i] = new(big.Int).Set(sortedTxs[txIndex].GasPrice())
	}
	return rewards
}
