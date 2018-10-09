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

	"github.com/gochain-io/gochain/accounts"
	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/common/math"
	"github.com/gochain-io/gochain/core"
	"github.com/gochain-io/gochain/core/bloombits"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/eth/downloader"
	"github.com/gochain-io/gochain/eth/gasprice"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/event"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/params"
	"github.com/gochain-io/gochain/rpc"
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

func (b *EthApiBackend) StateQuery(ctx context.Context, blockNr rpc.BlockNumber, fn func(*state.StateDB) error) error {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.StateQuery")
	defer span.End()
	// Pending state is only known by the miner
	if blockNr == rpc.PendingBlockNumber {
		return b.eth.miner.PendingQuery(fn)
	}
	header, err := b.HeaderByNumber(ctx, blockNr)
	if header == nil || err != nil {
		return err
	}
	stateDb, err := b.eth.BlockChain().StateAt(header.Root)
	if err != nil {
		return err
	}
	return fn(stateDb)
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

func (b *EthApiBackend) GetBlock(ctx context.Context, blockHash common.Hash) (*types.Block, error) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.GetBlock")
	defer span.End()
	return b.eth.blockchain.GetBlockByHash(blockHash), nil
}

func (b *EthApiBackend) GetReceipts(ctx context.Context, blockHash common.Hash) (types.Receipts, error) {
	ctx, span := trace.StartSpan(ctx, "EthApiBackend.GetReceipts")
	defer span.End()
	return core.GetBlockReceipts(b.eth.chainDb, blockHash, core.GetBlockNumber(b.eth.chainDb, blockHash)), nil
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

func (b *EthApiBackend) SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) {
	b.eth.BlockChain().SubscribeRemovedLogsEvent(ch)
}

func (b *EthApiBackend) UnsubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) {
	b.eth.BlockChain().UnsubscribeRemovedLogsEvent(ch)
}

func (b *EthApiBackend) SubscribeChainEvent(ch chan<- core.ChainEvent) {
	b.eth.BlockChain().SubscribeChainEvent(ch)
}

func (b *EthApiBackend) UnsubscribeChainEvent(ch chan<- core.ChainEvent) {
	b.eth.BlockChain().UnsubscribeChainEvent(ch)
}

func (b *EthApiBackend) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) {
	b.eth.BlockChain().SubscribeChainHeadEvent(ch)
}

func (b *EthApiBackend) UnsubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) {
	b.eth.BlockChain().UnsubscribeChainHeadEvent(ch)
}

func (b *EthApiBackend) SubscribeChainSideEvent(ch chan<- core.ChainSideEvent) {
	b.eth.BlockChain().SubscribeChainSideEvent(ch)
}

func (b *EthApiBackend) UnsubscribeChainSideEvent(ch chan<- core.ChainSideEvent) {
	b.eth.BlockChain().UnsubscribeChainSideEvent(ch)
}

func (b *EthApiBackend) SubscribeLogsEvent(ch chan<- []*types.Log) {
	b.eth.BlockChain().SubscribeLogsEvent(ch)
}

func (b *EthApiBackend) UnsubscribeLogsEvent(ch chan<- []*types.Log) {
	b.eth.BlockChain().UnsubscribeLogsEvent(ch)
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

func (b *EthApiBackend) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) {
	b.eth.TxPool().SubscribeNewTxsEvent(ch)
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

func (b *EthApiBackend) ChainDb() ethdb.Database {
	return b.eth.ChainDb()
}

func (b *EthApiBackend) EventMux() *event.TypeMux {
	return b.eth.EventMux()
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
