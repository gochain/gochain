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

package eth

import (
	"context"
	"math/big"

	"go.opencensus.io/trace"

	"github.com/gochain-io/gochain/v3/accounts"
	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/common/math"
	"github.com/gochain-io/gochain/v3/core"
	"github.com/gochain-io/gochain/v3/core/bloombits"
	"github.com/gochain-io/gochain/v3/core/state"
	"github.com/gochain-io/gochain/v3/core/types"
	"github.com/gochain-io/gochain/v3/core/vm"
	"github.com/gochain-io/gochain/v3/eth/downloader"
	"github.com/gochain-io/gochain/v3/eth/gasprice"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/params"
	"github.com/gochain-io/gochain/v3/rpc"
)

// EthApiBackend implements ethapi.Backend for full nodes
type EthApiBackend struct {
	eth           *GoChain
	initialSupply *big.Int
	gpo           *gasprice.Oracle
}

func (b *EthApiBackend) ChainConfig() *params.ChainConfig {
	return b.eth.chainConfig
}

func (b *EthApiBackend) InitialSupply() *big.Int {
	return b.initialSupply
}

func (b *EthApiBackend) GenesisAlloc() core.GenesisAlloc {
	if g := b.eth.config.Genesis; g != nil {
		return g.Alloc
	}
	return nil
}

func (b *EthApiBackend) CurrentBlock() *types.Block {
	return b.eth.blockchain.CurrentBlock()
}

func (b *EthApiBackend) SetHead(number uint64) {
	b.eth.protocolManager.downloader.Cancel()
	if err := b.eth.blockchain.SetHead(number); err != nil {
		log.Error("Cannot set eth api backend head", "number", number, "err", err)
	}
}

func (b *EthApiBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.HeaderByNumber")
	defer span.End()
	// Pending block is only known by the miner
	if blockNr == rpc.PendingBlockNumber {
		block := b.eth.miner.PendingBlock(ctx)
		if block == nil {
			return nil, nil
		}
		return block.Header(), nil
	}
	// Otherwise resolve and return the block
	if blockNr == rpc.LatestBlockNumber {
		return b.eth.blockchain.CurrentBlock().Header(), nil
	}
	return b.eth.blockchain.GetHeaderByNumber(uint64(blockNr)), nil
}

func (b *EthApiBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return b.eth.blockchain.GetHeaderByHash(hash), nil
}

func (b *EthApiBackend) BlockByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Block, error) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.BlockByNumber")
	defer span.End()
	span.AddAttributes(trace.Int64Attribute("num", int64(blockNr)))
	// Pending block is only known by the miner
	if blockNr == rpc.PendingBlockNumber {
		block := b.eth.miner.PendingBlock(ctx)
		return block, nil
	}
	// Otherwise resolve and return the block
	if blockNr == rpc.LatestBlockNumber {
		return b.eth.blockchain.CurrentBlock(), nil
	}
	return b.eth.blockchain.GetBlockByNumber(uint64(blockNr)), nil
}

func (b *EthApiBackend) StateAndHeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*state.StateDB, *types.Header, error) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.StateAndHeaderByNumber")
	defer span.End()
	// Pending state is only known by the miner
	if blockNr == rpc.PendingBlockNumber {
		block, state := b.eth.miner.Pending(ctx)
		return state, block.Header(), nil
	}
	// Otherwise resolve the block number and return its state
	header, err := b.HeaderByNumber(ctx, blockNr)
	if header == nil || err != nil {
		return nil, nil, err
	}
	stateDb, err := b.eth.BlockChain().StateAt(header.Root)
	return stateDb, header, err
}

func (b *EthApiBackend) GetBlock(ctx context.Context, hash common.Hash) (*types.Block, error) {
	return b.eth.blockchain.GetBlockByHash(hash), nil
}

func (b *EthApiBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	return b.eth.blockchain.GetReceiptsByHash(hash), nil
}

func (b *EthApiBackend) GetLogs(ctx context.Context, hash common.Hash) ([][]*types.Log, error) {
	receipts := b.eth.blockchain.GetReceiptsByHash(hash)
	if receipts == nil {
		return nil, nil
	}
	logs := make([][]*types.Log, len(receipts))
	for i, receipt := range receipts {
		logs[i] = receipt.Logs
	}
	return logs, nil
}

func (b *EthApiBackend) GetTd(blockHash common.Hash) *big.Int {
	return b.eth.blockchain.GetTdByHash(blockHash)
}

func (b *EthApiBackend) GetEVM(ctx context.Context, msg core.Message, state *state.StateDB, header *types.Header, vmCfg vm.Config) (*vm.EVM, error) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.GetEVM")
	defer span.End()
	state.SetBalance(msg.From(), math.MaxBig256)

	context := core.NewEVMContext(msg, header, b.eth.BlockChain(), nil)
	return vm.NewEVM(context, state, b.eth.chainConfig, vmCfg), nil
}

func (b *EthApiBackend) SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent, name string) {
	b.eth.BlockChain().SubscribeRemovedLogsEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) {
	b.eth.BlockChain().UnsubscribeRemovedLogsEvent(ch)
}

func (b *EthApiBackend) SubscribeChainEvent(ch chan<- core.ChainEvent, name string) {
	b.eth.BlockChain().SubscribeChainEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribeChainEvent(ch chan<- core.ChainEvent) {
	b.eth.BlockChain().UnsubscribeChainEvent(ch)
}

func (b *EthApiBackend) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent, name string) {
	b.eth.BlockChain().SubscribeChainHeadEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) {
	b.eth.BlockChain().UnsubscribeChainHeadEvent(ch)
}

func (b *EthApiBackend) SubscribeChainSideEvent(ch chan<- core.ChainSideEvent, name string) {
	b.eth.BlockChain().SubscribeChainSideEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribeChainSideEvent(ch chan<- core.ChainSideEvent) {
	b.eth.BlockChain().UnsubscribeChainSideEvent(ch)
}

func (b *EthApiBackend) SubscribeLogsEvent(ch chan<- []*types.Log, name string) {
	b.eth.BlockChain().SubscribeLogsEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribeLogsEvent(ch chan<- []*types.Log) {
	b.eth.BlockChain().UnsubscribeLogsEvent(ch)
}

func (b *EthApiBackend) SubscribePendingLogsEvent(ch chan<- core.PendingLogsEvent, name string) {
	b.eth.BlockChain().SubscribePendingLogsEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribePendingLogsEvent(ch chan<- core.PendingLogsEvent) {
	b.eth.BlockChain().UnsubscribePendingLogsEvent(ch)
}

func (b *EthApiBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	return b.eth.txPool.AddLocal(ctx, signedTx)
}

func (b *EthApiBackend) GetPoolTransactions() types.Transactions {
	ctx := context.TODO()
	return b.eth.txPool.PendingList(ctx)
}

func (b *EthApiBackend) GetPoolTransaction(hash common.Hash) *types.Transaction {
	return b.eth.txPool.Get(hash)
}

func (b *EthApiBackend) GetPoolNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return b.eth.txPool.State().GetNonce(addr), nil
}

func (b *EthApiBackend) Stats() (pending int, queued int) {
	ctx, span := trace.StartSpan(context.Background(), "EthApiBackend.Stats")
	defer span.End()
	return b.eth.txPool.StatsCtx(ctx)
}

func (b *EthApiBackend) TxPoolContent(ctx context.Context) (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.TxPoolContent")
	defer span.End()
	return b.eth.TxPool().Content(ctx)
}

func (b *EthApiBackend) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent, name string) {
	b.eth.TxPool().SubscribeNewTxsEvent(ch, name)
}

func (b *EthApiBackend) UnsubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) {
	b.eth.TxPool().UnsubscribeNewTxsEvent(ch)
}

func (b *EthApiBackend) Downloader() *downloader.Downloader {
	return b.eth.Downloader()
}

func (b *EthApiBackend) ProtocolVersion() int {
	return b.eth.EthVersion()
}

func (b *EthApiBackend) SuggestPrice(ctx context.Context) (*big.Int, error) {
	return b.gpo.SuggestPrice(ctx)
}

func (b *EthApiBackend) ChainDb() common.Database {
	return b.eth.ChainDb()
}

func (b *EthApiBackend) AccountManager() *accounts.Manager {
	return b.eth.AccountManager()
}

func (b *EthApiBackend) BloomStatus() (uint64, uint64) {
	sections, _, _ := b.eth.bloomIndexer.Sections()
	return params.BloomBitsBlocks, sections
}

func (b *EthApiBackend) ServiceFilter(ctx context.Context, session *bloombits.MatcherSession) {
	for i := 0; i < bloomFilterThreads; i++ {
		go session.Multiplex(bloomRetrievalBatch, bloomRetrievalWait, b.eth.bloomRequests)
	}
}
