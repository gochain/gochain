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

package ethapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/gochain/gochain/v5/accounts"
	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/common/hexutil"
	"github.com/gochain/gochain/v5/common/math"
	"github.com/gochain/gochain/v5/core"
	"github.com/gochain/gochain/v5/core/rawdb"
	"github.com/gochain/gochain/v5/core/state"
	"github.com/gochain/gochain/v5/core/types"
	"github.com/gochain/gochain/v5/core/vm"
	"github.com/gochain/gochain/v5/crypto"
	"github.com/gochain/gochain/v5/eth/downloader"
	"github.com/gochain/gochain/v5/ethdb"
	"github.com/gochain/gochain/v5/params"
	"github.com/gochain/gochain/v5/rpc"
)

type testBackend struct {
	db          common.Database
	chainConfig *params.ChainConfig
	state       *state.StateDB
	header      *types.Header
	block       *types.Block
	receipts    types.Receipts
	gasCap      uint64
	evmTimeout  time.Duration
	txFeeCap    float64
	sentTxs     []*types.Transaction
	poolPending map[common.Address]types.Transactions
	poolQueue   map[common.Address]types.Transactions
}

func newTestBackend(t *testing.T) *testBackend {
	t.Helper()
	db := ethdb.NewMemDatabase()
	statedb, err := state.New(common.Hash{}, state.NewDatabase(db))
	if err != nil {
		t.Fatalf("failed to create state: %v", err)
	}
	header := &types.Header{
		Number:     big.NewInt(42),
		Time:       big.NewInt(1600000000),
		Difficulty: big.NewInt(7),
		GasLimit:   10000000,
		GasUsed:    21000,
		Coinbase:   common.HexToAddress("0x1111111111111111111111111111111111111111"),
	}
	block := types.NewBlockWithHeader(header)
	cfg := *params.AllCliqueProtocolChanges
	cfg.ChainId = big.NewInt(60)
	return &testBackend{
		db:          db,
		chainConfig: &cfg,
		state:       statedb,
		header:      header,
		block:       block,
		gasCap:      50000000,
		evmTimeout:  5 * time.Second,
		txFeeCap:    100,
		poolPending: make(map[common.Address]types.Transactions),
		poolQueue:   make(map[common.Address]types.Transactions),
	}
}

func (b *testBackend) Downloader() *downloader.Downloader                       { return nil }
func (b *testBackend) ProtocolVersion() int                                     { return 63 }
func (b *testBackend) SuggestPrice(ctx context.Context) (*big.Int, error)       { return big.NewInt(2e9), nil }
func (b *testBackend) ChainDb() common.Database                                 { return b.db }
func (b *testBackend) AccountManager() *accounts.Manager                        { return nil }
func (b *testBackend) RPCGasCap() uint64                                        { return b.gasCap }
func (b *testBackend) RPCEVMTimeout() time.Duration                             { return b.evmTimeout }
func (b *testBackend) RPCTxFeeCap() float64                                     { return b.txFeeCap }
func (b *testBackend) SetHead(number uint64)                                    {}
func (b *testBackend) ChainConfig() *params.ChainConfig                         { return b.chainConfig }
func (b *testBackend) CurrentBlock() *types.Block                               { return b.block }
func (b *testBackend) InitialSupply() *big.Int                                  { return big.NewInt(1000000) }
func (b *testBackend) GenesisAlloc() core.GenesisAlloc                          { return nil }
func (b *testBackend) GetTd(blockHash common.Hash) *big.Int                     { return big.NewInt(100) }
func (b *testBackend) SubscribeChainEvent(ch chan<- core.ChainEvent, name string) {}
func (b *testBackend) UnsubscribeChainEvent(ch chan<- core.ChainEvent)          {}
func (b *testBackend) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent, name string) {
}
func (b *testBackend) UnsubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) {}
func (b *testBackend) SubscribeChainSideEvent(ch chan<- core.ChainSideEvent, name string) {
}
func (b *testBackend) UnsubscribeChainSideEvent(ch chan<- core.ChainSideEvent) {}
func (b *testBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	b.sentTxs = append(b.sentTxs, signedTx)
	return nil
}
func (b *testBackend) GetPoolTransactions() types.Transactions         { return nil }
func (b *testBackend) GetPoolTransaction(txHash common.Hash) *types.Transaction { return nil }
func (b *testBackend) GetPoolNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return b.state.GetNonce(addr), nil
}
func (b *testBackend) Stats() (pending int, queued int) { return 0, 0 }
func (b *testBackend) TxPoolContent(ctx context.Context) (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	return b.poolPending, b.poolQueue
}
func (b *testBackend) SubscribeNewTxsEvent(chan<- core.NewTxsEvent, string) {}
func (b *testBackend) UnsubscribeNewTxsEvent(chan<- core.NewTxsEvent)       {}

func (b *testBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	if blockNr == rpc.LatestBlockNumber || blockNr == rpc.SafeBlockNumber || blockNr == rpc.FinalizedBlockNumber || blockNr == rpc.PendingBlockNumber || blockNr == rpc.BlockNumber(b.header.Number.Int64()) {
		return b.header, nil
	}
	return nil, nil
}

func (b *testBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	if hash == b.block.Hash() || hash == b.header.Hash() {
		return b.header, nil
	}
	return nil, nil
}

func (b *testBackend) HeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.HeaderByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		return b.HeaderByHash(ctx, hash)
	}
	return nil, errors.New("invalid blockNrOrHash")
}

func (b *testBackend) BlockByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Block, error) {
	if blockNr == rpc.LatestBlockNumber || blockNr == rpc.SafeBlockNumber || blockNr == rpc.FinalizedBlockNumber || blockNr == rpc.PendingBlockNumber || blockNr == rpc.BlockNumber(b.block.Number().Int64()) {
		return b.block, nil
	}
	return nil, nil
}

func (b *testBackend) BlockByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Block, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.BlockByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		return b.GetBlock(ctx, hash)
	}
	return nil, errors.New("invalid blockNrOrHash")
}

func (b *testBackend) StateAndHeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*state.StateDB, *types.Header, error) {
	h, err := b.HeaderByNumber(ctx, blockNr)
	if h == nil || err != nil {
		return nil, nil, err
	}
	return b.state, h, nil
}

func (b *testBackend) StateAndHeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*state.StateDB, *types.Header, error) {
	h, err := b.HeaderByNumberOrHash(ctx, blockNrOrHash)
	if h == nil || err != nil {
		return nil, nil, err
	}
	return b.state, h, nil
}

func (b *testBackend) GetBlock(ctx context.Context, blockHash common.Hash) (*types.Block, error) {
	if blockHash == b.block.Hash() {
		return b.block, nil
	}
	return nil, nil
}

func (b *testBackend) GetReceipts(ctx context.Context, blockHash common.Hash) (types.Receipts, error) {
	if blockHash == b.block.Hash() {
		return b.receipts, nil
	}
	return nil, nil
}

func (b *testBackend) GetEVM(ctx context.Context, msg core.Message, statedb *state.StateDB, header *types.Header, vmCfg vm.Config) (*vm.EVM, error) {
	statedb.SetBalance(msg.From(), math.MaxBig256)
	evmCtx := vm.Context{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Origin:      msg.From(),
		Coinbase:    header.Coinbase,
		BlockNumber: new(big.Int).Set(header.Number),
		Time:        new(big.Int).Set(header.Time),
		Difficulty:  new(big.Int).Set(header.Difficulty),
		GasLimit:    header.GasLimit,
		GasPrice:    new(big.Int).Set(msg.GasPrice()),
	}
	return vm.NewEVM(evmCtx, statedb, b.chainConfig, vmCfg), nil
}

func (b *testBackend) FeeHistory(ctx context.Context, blockCount int, lastBlock rpc.BlockNumber, rewardPercentiles []float64) (*big.Int, [][]*big.Int, []*big.Int, []float64, error) {
	oldest := big.NewInt(40)
	rewards := [][]*big.Int{{big.NewInt(1e9), big.NewInt(2e9)}, {big.NewInt(3e9), big.NewInt(4e9)}}
	baseFees := []*big.Int{big.NewInt(0), big.NewInt(0), big.NewInt(0)}
	ratios := []float64{0.25, 0.5}
	return oldest, rewards, baseFees, ratios, nil
}

// TestCallArgsInputAndData verifies that CallArgs accepts both "input" and "data",
// prioritizing "input" when present.
func TestCallArgsInputAndData(t *testing.T) {
	tb := newTestBackend(t)
	api := NewPublicBlockChainAPI(tb)

	contract := common.HexToAddress("0x2222222222222222222222222222222222222222")
	// EVM bytecode that copies 32 bytes of calldata at offset 0 to memory 0 and returns it:
	// PUSH1 0x20 (32), PUSH1 0x00, PUSH1 0x00, CALLDATACOPY (37), PUSH1 0x20, PUSH1 0x00, RETURN (f3)
	tb.state.SetCode(contract, common.FromHex("0x6020600060003760206000f3"))

	want := common.RightPadBytes([]byte("hello-from-input"), 32)
	inputBytes := hexutil.Bytes(want)

	res, err := api.Call(
		context.Background(),
		CallArgs{To: &contract, Input: &inputBytes},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Call with Input failed: %v", err)
	}
	if !bytes.Equal(res, want) {
		t.Fatalf("Call result mismatch: got %x, want %x", res, want)
	}

	// Also verify Data still works when Input is nil
	wantData := common.RightPadBytes([]byte("hello-from-data"), 32)
	dataBytes := hexutil.Bytes(wantData)
	resData, err := api.Call(
		context.Background(),
		CallArgs{To: &contract, Data: &dataBytes},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Call with Data failed: %v", err)
	}
	if !bytes.Equal(resData, wantData) {
		t.Fatalf("Call with Data result mismatch: got %x, want %x", resData, wantData)
	}
}

// TestStateOverride verifies StateOverride (Code, Balance, State, StateDiff) and ensures cached state is untouched.
func TestStateOverride(t *testing.T) {
	tb := newTestBackend(t)
	api := NewPublicBlockChainAPI(tb)

	target := common.HexToAddress("0x3333333333333333333333333333333333333333")
	slot0 := common.BigToHash(big.NewInt(0))
	slot1 := common.BigToHash(big.NewInt(1))
	tb.state.SetState(target, slot0, common.BigToHash(big.NewInt(111)))
	tb.state.SetState(target, slot1, common.BigToHash(big.NewInt(222)))

	// Bytecode that returns SLOAD(0) || SLOAD(1) (64 bytes):
	// PUSH1 0x00, SLOAD, PUSH1 0x00, MSTORE, PUSH1 0x01, SLOAD, PUSH1 0x20, MSTORE, PUSH1 0x40, PUSH1 0x00, RETURN
	code := hexutil.Bytes(common.FromHex("0x60005460005260015460205260406000f3"))

	// 1. StateDiff overrides slot1 while preserving slot0
	diffMap := map[common.Hash]common.Hash{
		slot1: common.BigToHash(big.NewInt(999)),
	}
	overrides := StateOverride{
		target: OverrideAccount{
			Code:      &code,
			StateDiff: &diffMap,
		},
	}
	res, err := api.Call(
		context.Background(),
		CallArgs{To: &target},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		&overrides,
		nil,
	)
	if err != nil {
		t.Fatalf("Call with StateDiff failed: %v", err)
	}
	if got0 := new(big.Int).SetBytes(res[:32]).Int64(); got0 != 111 {
		t.Fatalf("StateDiff slot0 got %d, want 111", got0)
	}
	if got1 := new(big.Int).SetBytes(res[32:64]).Int64(); got1 != 999 {
		t.Fatalf("StateDiff slot1 got %d, want 999", got1)
	}

	// Verify underlying state was not mutated
	if orig1 := tb.state.GetState(target, slot1); orig1 != common.BigToHash(big.NewInt(222)) {
		t.Fatalf("underlying state mutated: got %x", orig1)
	}

	// 2. State (full replacement) clears slot0 and sets slot1
	fullState := map[common.Hash]common.Hash{
		slot1: common.BigToHash(big.NewInt(777)),
	}
	overridesFull := StateOverride{
		target: OverrideAccount{
			Code:  &code,
			State: &fullState,
		},
	}
	resFull, err := api.Call(
		context.Background(),
		CallArgs{To: &target},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		&overridesFull,
		nil,
	)
	if err != nil {
		t.Fatalf("Call with State failed: %v", err)
	}
	if got0 := new(big.Int).SetBytes(resFull[:32]).Int64(); got0 != 0 {
		t.Fatalf("Full State slot0 got %d, want 0", got0)
	}
	if got1 := new(big.Int).SetBytes(resFull[32:64]).Int64(); got1 != 777 {
		t.Fatalf("Full State slot1 got %d, want 777", got1)
	}

	// 3. Both State and StateDiff should error
	overridesBoth := StateOverride{
		target: OverrideAccount{
			State:     &fullState,
			StateDiff: &diffMap,
		},
	}
	if _, err := api.Call(
		context.Background(),
		CallArgs{To: &target},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		&overridesBoth,
		nil,
	); err == nil {
		t.Fatalf("expected error when both State and StateDiff are set")
	}
}

// TestBlockOverrides verifies BlockOverrides modifies header fields seen by EVM opcodes.
func TestBlockOverrides(t *testing.T) {
	tb := newTestBackend(t)
	api := NewPublicBlockChainAPI(tb)

	target := common.HexToAddress("0x4444444444444444444444444444444444444444")
	// Bytecode returning NUMBER (43), TIMESTAMP (42), COINBASE (41) in 96 bytes:
	// NUMBER (43), PUSH1 0x00, MSTORE (52), TIMESTAMP (42), PUSH1 0x20, MSTORE (52), COINBASE (41), PUSH1 0x40, MSTORE (52), PUSH1 0x60, PUSH1 0x00, RETURN (f3)
	tb.state.SetCode(target, common.FromHex("0x43600052426020524160405260606000f3"))

	overrideNum := (*hexutil.Big)(big.NewInt(12345))
	overrideTime := (*hexutil.Big)(big.NewInt(1700000000))
	overrideCoinbase := common.HexToAddress("0x9999999999999999999999999999999999999999")

	blockOverrides := &BlockOverrides{
		Number:       overrideNum,
		Time:         overrideTime,
		FeeRecipient: &overrideCoinbase,
	}
	res, err := api.Call(
		context.Background(),
		CallArgs{To: &target},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
		blockOverrides,
	)
	if err != nil {
		t.Fatalf("Call with BlockOverrides failed: %v", err)
	}
	if gotNum := new(big.Int).SetBytes(res[:32]).Int64(); gotNum != 12345 {
		t.Fatalf("NUMBER override got %d, want 12345", gotNum)
	}
	if gotTime := new(big.Int).SetBytes(res[32:64]).Int64(); gotTime != 1700000000 {
		t.Fatalf("TIMESTAMP override got %d, want 1700000000", gotTime)
	}
	if gotCoinbase := common.BytesToAddress(res[64:96]); gotCoinbase != overrideCoinbase {
		t.Fatalf("COINBASE override got %s, want %s", gotCoinbase.Hex(), overrideCoinbase.Hex())
	}
}

// TestRevertError verifies EIP-1474 structured revertError on Error(string) and Panic(uint256).
func TestRevertError(t *testing.T) {
	tb := newTestBackend(t)
	api := NewPublicBlockChainAPI(tb)

	// Standard Solidity Error("insufficient allowance") revert payload:
	// selector: 0x08c379a0
	// offset:   0x20
	// length:   0x16 (22)
	// data:     "insufficient allowance"
	revertPayload := common.FromHex("0x08c379a0" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000016" +
		"696e73756666696369656e7420616c6c6f77616e636500000000000000000000")

	// Contract that copies calldata to memory and executes REVERT(0, calldatasize):
	// CALLDATASIZE (36), PUSH1 0x00, PUSH1 0x00, CALLDATACOPY (37), CALLDATASIZE (36), PUSH1 0x00, REVERT (fd)
	reverter := common.HexToAddress("0x5555555555555555555555555555555555555555")
	tb.state.SetCode(reverter, common.FromHex("0x366000600037366000fd"))

	input := hexutil.Bytes(revertPayload)
	_, err := api.Call(
		context.Background(),
		CallArgs{To: &reverter, Input: &input},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
		nil,
	)
	if err == nil {
		t.Fatalf("expected revert error, got nil")
	}

	var rpcErr rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.ErrorCode() != 3 {
		t.Fatalf("expected rpc.Error with ErrorCode() == 3, got %T (%v)", err, err)
	}
	var dataErr rpc.DataError
	if !errors.As(err, &dataErr) || dataErr.ErrorData() != hexutil.Encode(revertPayload) {
		t.Fatalf("expected ErrorData() == %s, got %v", hexutil.Encode(revertPayload), dataErr)
	}
	if err.Error() != "execution reverted: insufficient allowance" {
		t.Fatalf("unexpected revert error message: %q", err.Error())
	}

	// Also verify EstimateGas returns the structured revertError
	_, errEst := api.EstimateGas(context.Background(), CallArgs{To: &reverter, Input: &input}, nil, nil)
	if errEst == nil || errEst.Error() != "execution reverted: insufficient allowance" {
		t.Fatalf("expected EstimateGas revert error, got %v", errEst)
	}

	// Test Panic(uint256) with code 0x11 (arithmetic underflow or overflow)
	panicPayload := common.FromHex("0x4e487b710000000000000000000000000000000000000000000000000000000000000011")
	panicInput := hexutil.Bytes(panicPayload)
	_, errPanic := api.Call(
		context.Background(),
		CallArgs{To: &reverter, Input: &panicInput},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
		nil,
	)
	if errPanic == nil || errPanic.Error() != "execution reverted: arithmetic underflow or overflow" {
		t.Fatalf("unexpected panic error: %v", errPanic)
	}
}

// TestRPCGasCapAndTxFeeCap verifies RPCGasCap caps gas in Call and EstimateGas, and RPCTxFeeCap rejects high-fee txs.
func TestRPCGasCapAndTxFeeCap(t *testing.T) {
	tb := newTestBackend(t)
	tb.gasCap = 50000
	api := NewPublicBlockChainAPI(tb)

	// Contract that returns GAS (5a) at the start of execution:
	// GAS (5a), PUSH1 0x00, MSTORE (52), PUSH1 0x20, PUSH1 0x00, RETURN (f3)
	gasContract := common.HexToAddress("0x6666666666666666666666666666666666666666")
	tb.state.SetCode(gasContract, common.FromHex("0x5a60005260206000f3"))

	reqGas := hexutil.Uint64(1000000) // 1M > 50k cap
	res, err := api.Call(
		context.Background(),
		CallArgs{To: &gasContract, Gas: &reqGas},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	remainingGas := new(big.Int).SetBytes(res).Uint64()
	if remainingGas > tb.gasCap {
		t.Fatalf("remainingGas %d exceeded RPCGasCap %d", remainingGas, tb.gasCap)
	}

	// Verify checkTxFee / SubmitTransaction with RPCTxFeeCap
	tb.txFeeCap = 1.0 // 1 GO max fee
	// 21000 gas * 100000 Gwei = 2.1 GO > 1.0 GO cap
	highFeeTx := types.NewTransaction(0, gasContract, big.NewInt(0), 21000, new(big.Int).Mul(big.NewInt(100000), big.NewInt(params.Shannon)), nil)
	if _, err := SubmitTransaction(context.Background(), tb, highFeeTx); err == nil {
		t.Fatalf("expected SubmitTransaction to reject transaction exceeding RPCTxFeeCap")
	}

	// Reasonable fee tx (21000 * 2 Gwei = 0.000042 GO) succeeds
	lowFeeTx := types.NewTransaction(0, gasContract, big.NewInt(0), 21000, big.NewInt(2*params.Shannon), nil)
	if _, err := SubmitTransaction(context.Background(), tb, lowFeeTx); err != nil {
		t.Fatalf("expected SubmitTransaction to accept reasonable fee tx, got: %v", err)
	}
}

// TestGetHeaderAndBlockReceiptsAndFeeHistory tests GetHeaderByNumber, GetHeaderByHash, GetBlockReceipts, and FeeHistory.
func TestGetHeaderAndBlockReceiptsAndFeeHistory(t *testing.T) {
	tb := newTestBackend(t)

	key, _ := crypto.GenerateKey()
	signer := types.NewEIP155Signer(tb.chainConfig.ChainId)
	tx, err := types.SignTx(
		types.NewTransaction(0, common.HexToAddress("0x7777777777777777777777777777777777777777"), big.NewInt(100), 21000, big.NewInt(2*params.Shannon), nil),
		signer,
		key,
	)
	if err != nil {
		t.Fatalf("SignTx failed: %v", err)
	}
	receipt := &types.Receipt{
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		TxHash:            tx.Hash(),
		GasUsed:           21000,
		Logs:              []*types.Log{},
	}
	tb.block = types.NewBlock(tb.header, []*types.Transaction{tx}, nil, []*types.Receipt{receipt})
	tb.header = tb.block.Header()
	tb.receipts = types.Receipts{receipt}
	rawdb.WriteBlock(tb.db, tb.block)
	rawdb.WriteCanonicalHash(tb.db, tb.block.Hash(), tb.block.NumberU64())

	bcAPI := NewPublicBlockChainAPI(tb)

	// 1. GetHeaderByNumber
	hMap, err := bcAPI.GetHeaderByNumber(context.Background(), rpc.LatestBlockNumber)
	if err != nil || hMap == nil {
		t.Fatalf("GetHeaderByNumber failed: %v", err)
	}
	if gotNum := (*big.Int)(hMap["number"].(*hexutil.Big)).Int64(); gotNum != 42 {
		t.Fatalf("GetHeaderByNumber number = %d, want 42", gotNum)
	}

	// 2. GetHeaderByHash
	hByHash := bcAPI.GetHeaderByHash(context.Background(), tb.block.Hash())
	if hByHash == nil || hByHash["hash"] != tb.block.Hash() {
		t.Fatalf("GetHeaderByHash mismatch: %v", hByHash)
	}

	// 3. GetBlockReceipts (by block number and by block hash)
	receipts, err := bcAPI.GetBlockReceipts(context.Background(), rpc.BlockNumberOrHashWithHash(tb.block.Hash(), false))
	if err != nil {
		t.Fatalf("GetBlockReceipts failed: %v", err)
	}
	if len(receipts) != 1 || receipts[0]["transactionHash"] != tx.Hash() {
		t.Fatalf("GetBlockReceipts unexpected result: %+v", receipts)
	}

	// 4. FeeHistory & DecimalOrHex unmarshaling
	ethAPI := NewPublicEthereumAPI(tb)
	var count DecimalOrHex
	if err := json.Unmarshal([]byte(`"0x2"`), &count); err != nil || count != 2 {
		t.Fatalf("DecimalOrHex hex unmarshal failed: count=%d, err=%v", count, err)
	}
	if err := json.Unmarshal([]byte(`2`), &count); err != nil || count != 2 {
		t.Fatalf("DecimalOrHex decimal unmarshal failed: count=%d, err=%v", count, err)
	}
	fh, err := ethAPI.FeeHistory(context.Background(), count, rpc.LatestBlockNumber, []float64{25, 75})
	if err != nil {
		t.Fatalf("FeeHistory failed: %v", err)
	}
	if fh.OldestBlock.ToInt().Int64() != 40 || len(fh.Reward) != 2 || len(fh.BaseFee) != 3 || len(fh.GasUsedRatio) != 2 {
		t.Fatalf("FeeHistory unexpected result: %+v", fh)
	}
}
