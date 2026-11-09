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

package tracers

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/common/hexutil"
	"github.com/gochain/gochain/v5/core/vm"
	"github.com/gochain/gochain/v5/crypto"
)

func emptyBytesIfNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// ============================================================================
// noopTracer
// ============================================================================

type noopTracer struct{}

func newNoopTracer(ctx *Context, cfg json.RawMessage) (TracerInterface, error) {
	return &noopTracer{}, nil
}

func (t *noopTracer) CaptureStart(from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) error {
	return nil
}

func (t *noopTracer) CaptureState(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	return nil
}

func (t *noopTracer) CaptureFault(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	return nil
}

func (t *noopTracer) CaptureEnd(output []byte, gasUsed uint64, d time.Duration, err error) error {
	return nil
}

func (t *noopTracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
}

func (t *noopTracer) CaptureExit(output []byte, gasUsed uint64, err error) {
}

func (t *noopTracer) GetResult() (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (t *noopTracer) Stop(err error) {
}

// ============================================================================
// 4byteTracer
// ============================================================================

type fourByteTracer struct {
	ids       map[string]int
	interrupt uint32
	reason    error
}

func newFourByteTracer(ctx *Context, cfg json.RawMessage) (TracerInterface, error) {
	return &fourByteTracer{
		ids: make(map[string]int),
	}, nil
}

func (t *fourByteTracer) store(id []byte, size int) {
	key := hexutil.Encode(id) + "-" + strconv.Itoa(size)
	t.ids[key] += 1
}

func (t *fourByteTracer) isPrecompiled(addr common.Address) bool {
	_, ok := vm.PrecompiledContractsByzantium[addr]
	return ok
}

func (t *fourByteTracer) CaptureStart(from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) error {
	if !create && len(input) >= 4 {
		t.store(input[0:4], len(input)-4)
	}
	return nil
}

func (t *fourByteTracer) CaptureState(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return t.reason
	}
	if err != nil {
		return nil
	}
	var ct int
	switch op {
	case vm.CALL, vm.CALLCODE:
		ct = 3
	case vm.DELEGATECALL, vm.STATICCALL:
		ct = 2
	default:
		return nil
	}
	if stack == nil || len(stack.Data()) < ct+2 {
		return nil
	}
	to := common.BigToAddress(stack.Back(1))
	if t.isPrecompiled(to) {
		return nil
	}
	inSz := stack.Back(ct + 1).Int64()
	if inSz >= 4 {
		inOff := stack.Back(ct).Int64()
		if memory != nil {
			if data := memory.Get(inOff, 4); len(data) == 4 {
				t.store(data, int(inSz)-4)
			}
		}
	}
	return nil
}

func (t *fourByteTracer) CaptureFault(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	return nil
}

func (t *fourByteTracer) CaptureEnd(output []byte, gasUsed uint64, d time.Duration, err error) error {
	return nil
}

func (t *fourByteTracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return
	}
	if len(input) < 4 {
		return
	}
	if typ != vm.CALL && typ != vm.CALLCODE && typ != vm.DELEGATECALL && typ != vm.STATICCALL {
		return
	}
	if t.isPrecompiled(to) {
		return
	}
	t.store(input[0:4], len(input)-4)
}

func (t *fourByteTracer) CaptureExit(output []byte, gasUsed uint64, err error) {
}

func (t *fourByteTracer) GetResult() (json.RawMessage, error) {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return nil, t.reason
	}
	res, err := json.Marshal(t.ids)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(res), nil
}

func (t *fourByteTracer) Stop(err error) {
	t.reason = err
	atomic.StoreUint32(&t.interrupt, 1)
}

// ============================================================================
// callTracer
// ============================================================================

type callLog struct {
	Address common.Address `json:"address"`
	Topics  []common.Hash  `json:"topics"`
	Data    hexutil.Bytes  `json:"data"`
}

type callFrame struct {
	Type    string          `json:"type"`
	From    *common.Address `json:"from,omitempty"`
	To      *common.Address `json:"to,omitempty"`
	Value   *hexutil.Big    `json:"value,omitempty"`
	Gas     *hexutil.Uint64 `json:"gas,omitempty"`
	GasUsed *hexutil.Uint64 `json:"gasUsed,omitempty"`
	Input   *hexutil.Bytes  `json:"input,omitempty"`
	Output  *hexutil.Bytes  `json:"output,omitempty"`
	Error   string          `json:"error,omitempty"`
	Time    string          `json:"time,omitempty"`
	Calls   []callFrame     `json:"calls,omitempty"`
	Logs    []callLog       `json:"logs,omitempty"`

	gasIn   uint64
	gasCost uint64
	outOff  int64
	outLen  int64
}

type callTracerConfig struct {
	OnlyTopCall bool `json:"onlyTopCall"`
	WithLog     bool `json:"withLog"`
}

type callTracer struct {
	callstack []callFrame
	config    callTracerConfig
	descended bool
	interrupt uint32
	reason    error
}

func newCallTracer(ctx *Context, cfg json.RawMessage) (TracerInterface, error) {
	var config callTracerConfig
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &config); err != nil {
			return nil, err
		}
	}
	return &callTracer{
		callstack: make([]callFrame, 1),
		config:    config,
	}, nil
}

func (t *callTracer) CaptureStart(from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) error {
	typ := "CALL"
	if create {
		typ = "CREATE"
	}
	fromCopy := from
	toCopy := to
	inCopy := hexutil.Bytes(emptyBytesIfNil(common.CopyBytes(input)))
	g := hexutil.Uint64(gas)
	val := new(big.Int)
	if value != nil {
		val.Set(value)
	}
	t.callstack[0] = callFrame{
		Type:  typ,
		From:  &fromCopy,
		To:    &toCopy,
		Input: &inCopy,
		Gas:   &g,
		Value: (*hexutil.Big)(val),
	}
	return nil
}

func (t *callTracer) CaptureState(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return t.reason
	}
	if err != nil {
		return t.CaptureFault(env, pc, op, gas, cost, memory, stack, contract, depth, err)
	}

	if t.config.OnlyTopCall && depth > 1 {
		return nil
	}

	syscall := (byte(op) & 0xf0) == 0xf0

	if !t.config.OnlyTopCall {
		if syscall && (op == vm.CREATE || op == vm.CREATE2) {
			if stack == nil || len(stack.Data()) < 3 {
				return nil
			}
			inOff := stack.Back(1).Int64()
			inSize := stack.Back(2).Int64()
			var inSlice []byte
			if memory != nil {
				inSlice = memory.Get(inOff, inSize)
			}
			inBytes := hexutil.Bytes(emptyBytesIfNil(inSlice))
			fromAddr := contract.Address()
			val := new(big.Int).Set(stack.Back(0))
			call := callFrame{
				Type:    op.String(),
				From:    &fromAddr,
				Input:   &inBytes,
				gasIn:   gas,
				gasCost: cost,
				Value:   (*hexutil.Big)(val),
			}
			t.callstack = append(t.callstack, call)
			t.descended = true
			return nil
		}

		if syscall && op == vm.SELFDESTRUCT {
			left := len(t.callstack)
			t.callstack[left-1].Calls = append(t.callstack[left-1].Calls, callFrame{
				Type: op.String(),
			})
			return nil
		}

		if syscall && (op == vm.CALL || op == vm.CALLCODE || op == vm.DELEGATECALL || op == vm.STATICCALL) {
			off := 1
			if op == vm.DELEGATECALL || op == vm.STATICCALL {
				off = 0
			}
			if stack == nil || len(stack.Data()) < 6+off {
				return nil
			}
			to := common.BigToAddress(stack.Back(1))
			if _, ok := vm.PrecompiledContractsByzantium[to]; ok {
				return nil
			}
			inOff := stack.Back(2 + off).Int64()
			inSize := stack.Back(3 + off).Int64()
			var inSlice []byte
			if memory != nil {
				inSlice = memory.Get(inOff, inSize)
			}
			inBytes := hexutil.Bytes(emptyBytesIfNil(inSlice))
			fromAddr := contract.Address()
			call := callFrame{
				Type:    op.String(),
				From:    &fromAddr,
				To:      &to,
				Input:   &inBytes,
				gasIn:   gas,
				gasCost: cost,
				outOff:  stack.Back(4 + off).Int64(),
				outLen:  stack.Back(5 + off).Int64(),
			}
			if op != vm.DELEGATECALL && op != vm.STATICCALL {
				val := new(big.Int).Set(stack.Back(2))
				call.Value = (*hexutil.Big)(val)
			}
			t.callstack = append(t.callstack, call)
			t.descended = true
			return nil
		}

		if t.descended {
			if depth >= len(t.callstack) {
				g := hexutil.Uint64(gas)
				t.callstack[len(t.callstack)-1].Gas = &g
			}
			t.descended = false
		}
	}

	if syscall && op == vm.REVERT {
		t.callstack[len(t.callstack)-1].Error = "execution reverted"
		t.callstack[len(t.callstack)-1].Logs = nil
		return nil
	}

	if !t.config.OnlyTopCall && len(t.callstack) > 1 && depth == len(t.callstack)-1 {
		call := t.callstack[len(t.callstack)-1]
		t.callstack = t.callstack[:len(t.callstack)-1]

		if call.Type == "CREATE" || call.Type == "CREATE2" {
			gu := hexutil.Uint64(call.gasIn - call.gasCost - gas)
			call.GasUsed = &gu

			if stack != nil && len(stack.Data()) > 0 {
				ret := stack.Back(0)
				if ret.Sign() != 0 {
					toAddr := common.BigToAddress(ret)
					call.To = &toAddr
					var code []byte
					if env != nil && env.StateDB != nil {
						code = common.CopyBytes(env.StateDB.GetCode(toAddr))
					}
					outBytes := hexutil.Bytes(emptyBytesIfNil(code))
					call.Output = &outBytes
				} else if call.Error == "" {
					call.Error = "internal failure"
					call.Logs = nil
				}
			}
		} else {
			if call.Gas != nil {
				gu := hexutil.Uint64(call.gasIn - call.gasCost + uint64(*call.Gas) - gas)
				call.GasUsed = &gu

				if stack != nil && len(stack.Data()) > 0 {
					ret := stack.Back(0)
					if ret.Sign() != 0 {
						var outSlice []byte
						if memory != nil {
							outSlice = memory.Get(call.outOff, call.outLen)
						}
						outBytes := hexutil.Bytes(emptyBytesIfNil(outSlice))
						call.Output = &outBytes
					} else if call.Error == "" {
						call.Error = "internal failure"
						call.Logs = nil
					}
				}
			}
		}
		left := len(t.callstack)
		t.callstack[left-1].Calls = append(t.callstack[left-1].Calls, call)
	}

	if t.config.WithLog && op >= vm.LOG0 && op <= vm.LOG4 {
		n := int(op - vm.LOG0)
		if stack != nil && len(stack.Data()) >= 2+n {
			mStart := stack.Back(0).Int64()
			mSize := stack.Back(1).Int64()
			var data []byte
			if memory != nil {
				data = memory.Get(mStart, mSize)
			}
			topics := make([]common.Hash, n)
			for i := 0; i < n; i++ {
				topics[i] = common.BigToHash(stack.Back(2 + i))
			}
			t.callstack[len(t.callstack)-1].Logs = append(t.callstack[len(t.callstack)-1].Logs, callLog{
				Address: contract.Address(),
				Topics:  topics,
				Data:    hexutil.Bytes(emptyBytesIfNil(data)),
			})
		}
	}

	return nil
}

func (t *callTracer) CaptureFault(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return t.reason
	}
	if t.config.OnlyTopCall && depth > 1 {
		return nil
	}
	if t.callstack[len(t.callstack)-1].Error != "" {
		return nil
	}
	call := t.callstack[len(t.callstack)-1]
	t.callstack = t.callstack[:len(t.callstack)-1]
	if err != nil {
		call.Error = err.Error()
	}
	call.Logs = nil

	if call.Gas != nil {
		gu := *call.Gas
		call.GasUsed = &gu
	}
	if len(t.callstack) > 0 {
		left := len(t.callstack)
		t.callstack[left-1].Calls = append(t.callstack[left-1].Calls, call)
		return nil
	}
	t.callstack = append(t.callstack, call)
	return nil
}

func (t *callTracer) CaptureEnd(output []byte, gasUsed uint64, d time.Duration, err error) error {
	gu := hexutil.Uint64(gasUsed)
	t.callstack[0].GasUsed = &gu
	if d != 0 {
		t.callstack[0].Time = d.String()
	}
	if err != nil && t.callstack[0].Error == "" {
		t.callstack[0].Error = err.Error()
		t.callstack[0].Logs = nil
	}
	if t.callstack[0].Error != "" {
		t.callstack[0].Output = nil
	} else {
		outBytes := hexutil.Bytes(emptyBytesIfNil(common.CopyBytes(output)))
		t.callstack[0].Output = &outBytes
	}
	return nil
}

func (t *callTracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if t.config.OnlyTopCall {
		return
	}
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return
	}
	fromCopy := from
	toCopy := to
	inBytes := hexutil.Bytes(emptyBytesIfNil(common.CopyBytes(input)))
	g := hexutil.Uint64(gas)
	call := callFrame{
		Type:  typ.String(),
		From:  &fromCopy,
		To:    &toCopy,
		Input: &inBytes,
		Gas:   &g,
	}
	if value != nil {
		call.Value = (*hexutil.Big)(new(big.Int).Set(value))
	}
	t.callstack = append(t.callstack, call)
}

func (t *callTracer) CaptureExit(output []byte, gasUsed uint64, err error) {
	if t.config.OnlyTopCall {
		return
	}
	size := len(t.callstack)
	if size <= 1 {
		return
	}
	call := t.callstack[size-1]
	t.callstack = t.callstack[:size-1]

	gu := hexutil.Uint64(gasUsed)
	call.GasUsed = &gu
	if err == nil {
		outBytes := hexutil.Bytes(emptyBytesIfNil(common.CopyBytes(output)))
		call.Output = &outBytes
	} else {
		call.Error = err.Error()
		call.Logs = nil
		if call.Type == "CREATE" || call.Type == "CREATE2" {
			call.To = nil
		}
	}
	t.callstack[size-2].Calls = append(t.callstack[size-2].Calls, call)
}

func (t *callTracer) GetResult() (json.RawMessage, error) {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return nil, t.reason
	}
	res, err := json.Marshal(t.callstack[0])
	if err != nil {
		return nil, err
	}
	return json.RawMessage(res), nil
}

func (t *callTracer) Stop(err error) {
	t.reason = err
	atomic.StoreUint32(&t.interrupt, 1)
}

// ============================================================================
// prestateTracer
// ============================================================================

type prestateAccount struct {
	Balance *hexutil.Big                `json:"balance,omitempty"`
	Nonce   uint64                      `json:"nonce,omitempty"`
	Code    *hexutil.Bytes              `json:"code,omitempty"`
	Storage map[common.Hash]common.Hash `json:"storage,omitempty"`
}

// legacyPrestateAccount always serializes balance, nonce, code, and storage
// to match the exact output format of prestate_tracer.js when diffMode is false.
type legacyPrestateAccount struct {
	Balance *hexutil.Big                `json:"balance"`
	Nonce   uint64                      `json:"nonce"`
	Code    hexutil.Bytes               `json:"code"`
	Storage map[common.Hash]common.Hash `json:"storage"`
}

type prestateTracerConfig struct {
	DiffMode bool `json:"diffMode"`
}

type prestateDiffResult struct {
	Post map[common.Address]*prestateAccount `json:"post"`
	Pre  map[common.Address]*prestateAccount `json:"pre"`
}

type prestateTracer struct {
	env       *vm.EVM
	pre       map[common.Address]*legacyPrestateAccount
	existed   map[common.Address]bool
	created   map[common.Address]bool
	deleted   map[common.Address]bool
	from      common.Address
	to        common.Address
	create    bool
	value     *big.Int
	config    prestateTracerConfig
	inited    bool
	adjusted  bool
	interrupt uint32
	reason    error
}

func newPrestateTracer(ctx *Context, cfg json.RawMessage) (TracerInterface, error) {
	var config prestateTracerConfig
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &config); err != nil {
			return nil, err
		}
	}
	return &prestateTracer{
		pre:     make(map[common.Address]*legacyPrestateAccount),
		existed: make(map[common.Address]bool),
		created: make(map[common.Address]bool),
		deleted: make(map[common.Address]bool),
		value:   new(big.Int),
		config:  config,
	}, nil
}

func (t *prestateTracer) lookupAccount(addr common.Address) {
	if _, ok := t.pre[addr]; ok {
		return
	}
	if t.env == nil || t.env.StateDB == nil {
		t.pre[addr] = &legacyPrestateAccount{
			Balance: (*hexutil.Big)(new(big.Int)),
			Nonce:   0,
			Code:    hexutil.Bytes{},
			Storage: make(map[common.Hash]common.Hash),
		}
		return
	}
	t.existed[addr] = t.env.StateDB.Exist(addr)
	bal := new(big.Int)
	if b := t.env.StateDB.GetBalance(addr); b != nil {
		bal.Set(b)
	}
	code := common.CopyBytes(t.env.StateDB.GetCode(addr))
	t.pre[addr] = &legacyPrestateAccount{
		Balance: (*hexutil.Big)(bal),
		Nonce:   t.env.StateDB.GetNonce(addr),
		Code:    hexutil.Bytes(emptyBytesIfNil(code)),
		Storage: make(map[common.Hash]common.Hash),
	}
}

func (t *prestateTracer) lookupStorage(addr common.Address, key common.Hash) {
	t.lookupAccount(addr)
	if _, ok := t.pre[addr].Storage[key]; !ok {
		var val common.Hash
		if t.env != nil && t.env.StateDB != nil {
			val = t.env.StateDB.GetState(addr, key)
		}
		t.pre[addr].Storage[key] = val
	}
}

func (t *prestateTracer) adjustInitialPrestate() {
	if t.adjusted || t.env == nil || t.env.StateDB == nil {
		return
	}
	t.adjusted = true
	t.lookupAccount(t.from)
	t.lookupAccount(t.to)

	fromBal := new(big.Int).Add((*big.Int)(t.pre[t.from].Balance), t.value)
	t.pre[t.from].Balance = (*hexutil.Big)(fromBal)
	if t.pre[t.from].Nonce > 0 {
		t.pre[t.from].Nonce--
	}

	if t.create {
		t.created[t.to] = true
		delete(t.pre, t.to)
	} else {
		toBal := new(big.Int).Sub((*big.Int)(t.pre[t.to].Balance), t.value)
		t.pre[t.to].Balance = (*hexutil.Big)(toBal)
	}
}

func (t *prestateTracer) CaptureStart(from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) error {
	t.from = from
	t.to = to
	t.create = create
	if value != nil {
		t.value.Set(value)
	}
	return nil
}

func (t *prestateTracer) CaptureState(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return t.reason
	}
	t.env = env
	if !t.inited {
		t.inited = true
		t.lookupAccount(contract.Address())
		t.adjustInitialPrestate()
	}
	if stack == nil {
		return nil
	}
	stackLen := len(stack.Data())
	switch op {
	case vm.SLOAD, vm.SSTORE:
		if stackLen >= 1 {
			slot := common.BigToHash(stack.Back(0))
			t.lookupStorage(contract.Address(), slot)
		}
	case vm.EXTCODECOPY, vm.EXTCODESIZE, vm.EXTCODEHASH, vm.BALANCE, vm.SELFDESTRUCT:
		if stackLen >= 1 {
			addr := common.BigToAddress(stack.Back(0))
			t.lookupAccount(addr)
			if op == vm.SELFDESTRUCT {
				t.deleted[contract.Address()] = true
			}
		}
	case vm.DELEGATECALL, vm.CALL, vm.STATICCALL, vm.CALLCODE:
		if stackLen >= 5 {
			addr := common.BigToAddress(stack.Back(1))
			t.lookupAccount(addr)
		}
	case vm.CREATE:
		if env != nil && env.StateDB != nil {
			nonce := env.StateDB.GetNonce(contract.Address())
			addr := crypto.CreateAddress(contract.Address(), nonce)
			t.lookupAccount(addr)
			t.created[addr] = true
		}
	case vm.CREATE2:
		if stackLen >= 4 && memory != nil {
			offset := stack.Back(1).Int64()
			size := stack.Back(2).Int64()
			salt := common.BigToHash(stack.Back(3))
			code := memory.Get(offset, size)
			addr := crypto.CreateAddress2(contract.Address(), salt, crypto.Keccak256(code))
			t.lookupAccount(addr)
			t.created[addr] = true
		}
	}
	return nil
}

func (t *prestateTracer) CaptureFault(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	return nil
}

func (t *prestateTracer) CaptureEnd(output []byte, gasUsed uint64, d time.Duration, err error) error {
	if !t.adjusted {
		t.adjustInitialPrestate()
	}
	if t.create {
		delete(t.pre, t.to)
	}
	return nil
}

func (t *prestateTracer) GetResult() (json.RawMessage, error) {
	if atomic.LoadUint32(&t.interrupt) > 0 {
		return nil, t.reason
	}
	if !t.adjusted {
		t.adjustInitialPrestate()
	}
	if !t.config.DiffMode {
		res, err := json.Marshal(t.pre)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(res), nil
	}

	diff := prestateDiffResult{
		Pre:  make(map[common.Address]*prestateAccount),
		Post: make(map[common.Address]*prestateAccount),
	}
	if t.env == nil || t.env.StateDB == nil {
		res, err := json.Marshal(diff)
		return json.RawMessage(res), err
	}

	// Also check created accounts that might have been removed from t.pre (such as t.to on top-level CREATE)
	allAddrs := make(map[common.Address]struct{})
	for addr := range t.pre {
		allAddrs[addr] = struct{}{}
	}
	for addr := range t.created {
		allAddrs[addr] = struct{}{}
	}

	for addr := range allAddrs {
		preAcc := t.pre[addr]
		wasCreated := t.created[addr]
		if !wasCreated && preAcc != nil && !t.existed[addr] {
			wasCreated = true
		}

		// If account was deleted via SELFDESTRUCT or doesn't exist in post state
		if t.deleted[addr] || !t.env.StateDB.Exist(addr) {
			if !wasCreated && preAcc != nil {
				preCode := preAcc.Code
				diff.Pre[addr] = &prestateAccount{
					Balance: preAcc.Balance,
					Nonce:   preAcc.Nonce,
					Code:    &preCode,
					Storage: preAcc.Storage,
				}
			}
			continue
		}

		postBal := new(big.Int)
		if b := t.env.StateDB.GetBalance(addr); b != nil {
			postBal.Set(b)
		}
		postNonce := t.env.StateDB.GetNonce(addr)
		postCode := hexutil.Bytes(emptyBytesIfNil(common.CopyBytes(t.env.StateDB.GetCode(addr))))

		if wasCreated {
			postAcc := &prestateAccount{
				Balance: (*hexutil.Big)(postBal),
				Nonce:   postNonce,
				Storage: make(map[common.Hash]common.Hash),
			}
			if len(postCode) > 0 {
				postAcc.Code = &postCode
			}
			if preAcc != nil {
				for k := range preAcc.Storage {
					if v := t.env.StateDB.GetState(addr, k); v != (common.Hash{}) {
						postAcc.Storage[k] = v
					}
				}
			}
			diff.Post[addr] = postAcc
			continue
		}

		// Account existed before and exists after; compute diff
		preDiff := &prestateAccount{
			Storage: make(map[common.Hash]common.Hash),
		}
		postDiff := &prestateAccount{
			Storage: make(map[common.Hash]common.Hash),
		}
		modified := false

		if (*big.Int)(preAcc.Balance).Cmp(postBal) != 0 {
			modified = true
			preDiff.Balance = preAcc.Balance
			postDiff.Balance = (*hexutil.Big)(postBal)
		}
		if preAcc.Nonce != postNonce {
			modified = true
			preDiff.Nonce = preAcc.Nonce
			postDiff.Nonce = postNonce
		}
		if !bytes.Equal(preAcc.Code, postCode) {
			modified = true
			preCodeCopy := preAcc.Code
			preDiff.Code = &preCodeCopy
			postDiff.Code = &postCode
		}

		for k, preVal := range preAcc.Storage {
			postVal := t.env.StateDB.GetState(addr, k)
			if preVal == postVal {
				continue
			}
			modified = true
			if preVal != (common.Hash{}) {
				preDiff.Storage[k] = preVal
			}
			if postVal != (common.Hash{}) {
				postDiff.Storage[k] = postVal
			}
		}

		if modified {
			if len(preDiff.Storage) == 0 {
				preDiff.Storage = nil
			}
			if len(postDiff.Storage) == 0 {
				postDiff.Storage = nil
			}
			diff.Pre[addr] = preDiff
			diff.Post[addr] = postDiff
		}
	}

	res, err := json.Marshal(diff)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(res), nil
}

func (t *prestateTracer) Stop(err error) {
	t.reason = err
	atomic.StoreUint32(&t.interrupt, 1)
}
