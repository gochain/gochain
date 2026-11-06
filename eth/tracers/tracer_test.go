// Copyright 2017 The go-ethereum Authors
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
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/common/hexutil"
	"github.com/gochain/gochain/v5/core"
	"github.com/gochain/gochain/v5/core/state"
	"github.com/gochain/gochain/v5/core/vm"
	"github.com/gochain/gochain/v5/ethdb"
	"github.com/gochain/gochain/v5/params"
	"github.com/gochain/gochain/v5/tests"
)

type account struct{}

func (account) SubBalance(amount *big.Int)                          {}
func (account) AddBalance(amount *big.Int)                          {}
func (account) SetAddress(common.Address)                           {}
func (account) Value() *big.Int                                     { return nil }
func (account) SetBalance(*big.Int)                                 {}
func (account) SetNonce(uint64)                                     {}
func (account) Balance() *big.Int                                   { return nil }
func (account) Address() common.Address                             { return common.Address{} }
func (account) ReturnGas(*big.Int)                                  {}
func (account) SetCode(common.Hash, []byte)                         {}
func (account) ForEachStorage(cb func(key, value common.Hash) bool) {}

type dummyStatedb struct {
	state.StateDB
}

func (*dummyStatedb) GetRefund() uint64 { return 1337 }

func runTrace(tracer *Tracer) (json.RawMessage, error) {
	env := vm.NewEVM(vm.Context{BlockNumber: big.NewInt(1)}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Debug: true, Tracer: tracer})

	contract := vm.NewContract(account{}, account{}, big.NewInt(0), 10000)
	contract.Code = []byte{byte(vm.PUSH1), 0x1, byte(vm.PUSH1), 0x1, 0x0}

	_, err := env.Interpreter().Run(contract, []byte{}, false)
	if err != nil {
		return nil, err
	}
	return tracer.GetResult()
}

func TestTracing(t *testing.T) {
	tracer, err := New("{count: 0, step: function() { this.count += 1; }, fault: function() {}, result: function() { return this.count; }}")
	if err != nil {
		t.Fatal(err)
	}

	ret, err := runTrace(tracer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ret, []byte("3")) {
		t.Errorf("Expected return value to be 3, got %s", string(ret))
	}
}

func TestStack(t *testing.T) {
	tracer, err := New("{depths: [], step: function(log) { this.depths.push(log.stack.length()); }, fault: function() {}, result: function() { return this.depths; }}")
	if err != nil {
		t.Fatal(err)
	}

	ret, err := runTrace(tracer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ret, []byte("[0,1,2]")) {
		t.Errorf("Expected return value to be [0,1,2], got %s", string(ret))
	}
}

func TestOpcodes(t *testing.T) {
	tracer, err := New("{opcodes: [], step: function(log) { this.opcodes.push(log.op.toString()); }, fault: function() {}, result: function() { return this.opcodes; }}")
	if err != nil {
		t.Fatal(err)
	}

	ret, err := runTrace(tracer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ret, []byte("[\"PUSH1\",\"PUSH1\",\"STOP\"]")) {
		t.Errorf("Expected return value to be [\"PUSH1\",\"PUSH1\",\"STOP\"], got %s", string(ret))
	}
}

func TestHalt(t *testing.T) {
	timeout := errors.New("stahp")
	tracer, err := New("{step: function() { while(true){} }, fault: function() {}, result: function() { return null; }}")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		tracer.Stop(timeout)
	}()

	if _, err = runTrace(tracer); err == nil || err.Error() != "stahp    in server-side tracer function 'step'" {
		t.Errorf("Expected timeout error, got %v", err)
	}
}

func TestHaltBetweenSteps(t *testing.T) {
	tracer, err := New("{step: function() {}, fault: function() {}, result: function() { return null; }}")
	if err != nil {
		t.Fatal(err)
	}

	env := vm.NewEVM(vm.Context{BlockNumber: big.NewInt(1)}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Debug: true, Tracer: tracer})
	contract := vm.NewContract(&account{}, &account{}, big.NewInt(0), 0)

	tracer.CaptureState(env, 0, 0, 0, 0, nil, nil, contract, 0, nil)
	timeout := errors.New("stahp")
	tracer.Stop(timeout)
	tracer.CaptureState(env, 0, 0, 0, 0, nil, nil, contract, 0, nil)

	if _, err := tracer.GetResult(); err == nil || err.Error() != timeout.Error() {
		t.Errorf("Expected timeout error, got %v", err)
	}
}

func TestOutOfBoundsMemoryAndStackDoNotPanic(t *testing.T) {
	jsCode := `{
		ok: false,
		step: function(log) {
			var s1 = log.memory.slice(10, 5);
			var s2 = log.memory.slice(-1, 5);
			var s3 = log.memory.slice(0, 100000);
			var u1 = log.memory.getUint(-1);
			var u2 = log.memory.getUint(100000);
			var p1 = log.stack.peek(-1);
			var p2 = log.stack.peek(100000);
			var mlen = log.memory.length();
			var g1 = slice([1, 2, 3], 2, 1);
			var g2 = slice([1, 2, 3], -1, 2);
			if (s1.length === 0 && s2.length === 0 && s3.length === 0 &&
				u1.toString(10) === "0" && u2.toString(10) === "0" &&
				p1.toString(10) === "0" && p2.toString(10) === "0" &&
				mlen >= 0 && g1.length === 0 && g2.length === 0) {
				this.ok = true;
			}
		},
		fault: function() {},
		result: function() { return this.ok; }
	}`
	tracer, err := New(jsCode)
	if err != nil {
		t.Fatal(err)
	}
	env := vm.NewEVM(vm.Context{BlockNumber: big.NewInt(1)}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Debug: true, Tracer: tracer})
	// PUSH1 0x42 PUSH1 0x00 MSTORE STOP (resizes memory to 32 bytes so memory.Len() == 32 when testing getUint(-1))
	contract := vm.NewContract(account{}, account{}, big.NewInt(0), 10000)
	contract.Code = []byte{byte(vm.PUSH1), 0x42, byte(vm.PUSH1), 0x00, byte(vm.MSTORE), byte(vm.STOP)}

	if _, err := env.Interpreter().Run(contract, []byte{}, false); err != nil {
		t.Fatal(err)
	}
	ret, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ret, []byte("true")) {
		t.Errorf("Expected true, got %s", string(ret))
	}
}

func TestEnterExitCallback(t *testing.T) {
	tracer, err := New(`{
		entered: "",
		exitedGas: 0,
		enter: function(frame) {
			this.entered = frame.getType() + ":" + toHex(frame.getInput());
		},
		exit: function(res) {
			this.exitedGas = res.getGasUsed();
		},
		result: function() {
			return {entered: this.entered, exitedGas: this.exitedGas};
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	tracer.CaptureEnter(vm.CALL, common.HexToAddress("0x11"), common.HexToAddress("0x22"), []byte{0xde, 0xad}, 1000, big.NewInt(5))
	tracer.CaptureExit([]byte{0xbe, 0xef}, 42, nil)
	ret, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"entered":"CALL:0xdead","exitedGas":42}`
	if string(ret) != expected {
		t.Errorf("Expected %s, got %s", expected, string(ret))
	}
}

func TestNativeNoopTracer(t *testing.T) {
	tracer, err := New("noopTracer")
	if err != nil {
		t.Fatal(err)
	}
	ret, err := runTrace(tracer)
	if err != nil {
		t.Fatal(err)
	}
	if string(ret) != "{}" {
		t.Errorf("Expected {}, got %s", string(ret))
	}
}

func TestNative4ByteTracer(t *testing.T) {
	tracer, err := New("4byteTracer")
	if err != nil {
		t.Fatal(err)
	}
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	input := hexutil.MustDecode("0xa9059cbb0000000000000000000000000000000000000000000000000000000000000001")

	if err := tracer.CaptureStart(from, to, false, input, 100000, big.NewInt(0)); err != nil {
		t.Fatal(err)
	}
	if err := tracer.CaptureEnd(nil, 21000, time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	ret, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	var ids map[string]int
	if err := json.Unmarshal(ret, &ids); err != nil {
		t.Fatal(err)
	}
	if ids["0xa9059cbb-32"] != 1 {
		t.Errorf("Expected 0xa9059cbb-32 count 1, got %v", ids)
	}
}

func TestNativeCallTracerOptions(t *testing.T) {
	caller := common.HexToAddress("0x1111111111111111111111111111111111111111")
	parentAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	childAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")

	// Child contract: PUSH1 0xaa PUSH1 0x00 MSTORE PUSH1 0x01 PUSH1 0x1f RETURN
	childCode := []byte{
		byte(vm.PUSH1), 0xaa,
		byte(vm.PUSH1), 0x00,
		byte(vm.MSTORE),
		byte(vm.PUSH1), 0x01,
		byte(vm.PUSH1), 0x1f,
		byte(vm.RETURN),
	}

	// Parent contract:
	// Emits LOG1(offset=0, size=1, topic=0x77) then CALLs childAddr
	parentCode := []byte{
		byte(vm.PUSH1), 0xbb,
		byte(vm.PUSH1), 0x00,
		byte(vm.MSTORE8),
		byte(vm.PUSH1), 0x77, // topic0
		byte(vm.PUSH1), 0x01, // size
		byte(vm.PUSH1), 0x00, // offset
		byte(vm.LOG1),
		// CALL(gas=50000, addr=childAddr, val=0, inOff=0, inSize=0, outOff=0, outSize=1)
		byte(vm.PUSH1), 0x01, // outSize
		byte(vm.PUSH1), 0x00, // outOff
		byte(vm.PUSH1), 0x00, // inSize
		byte(vm.PUSH1), 0x00, // inOff
		byte(vm.PUSH1), 0x00, // value
		byte(vm.PUSH20),
	}
	parentCode = append(parentCode, childAddr.Bytes()...)
	parentCode = append(parentCode,
		byte(vm.PUSH2), 0xc3, 0x50, // gas 50000
		byte(vm.CALL),
		byte(vm.STOP),
	)

	runWithCfg := func(cfg string) callFrame {
		alloc := core.GenesisAlloc{
			caller:     {Balance: big.NewInt(1000000000)},
			parentAddr: {Code: parentCode, Balance: big.NewInt(0)},
			childAddr:  {Code: childCode, Balance: big.NewInt(0)},
		}
		statedb := tests.MakePreState(ethdb.NewMemDatabase(), alloc)
		tracer, err := NewWithConfig("callTracer", nil, json.RawMessage(cfg))
		if err != nil {
			t.Fatal(err)
		}
		vmctx := vm.Context{
			CanTransfer: core.CanTransfer,
			Transfer:    core.Transfer,
			Origin:      caller,
			BlockNumber: big.NewInt(1),
			Time:        big.NewInt(1),
			Difficulty:  big.NewInt(1),
			GasLimit:    1000000,
			GasPrice:    big.NewInt(1),
		}
		evm := vm.NewEVM(vmctx, statedb, params.MainnetChainConfig, vm.Config{Debug: true, Tracer: tracer})
		if _, _, err := evm.Call(vm.AccountRef(caller), parentAddr, []byte{0x01, 0x02}, 100000, big.NewInt(0)); err != nil {
			t.Fatal(err)
		}
		res, err := tracer.GetResult()
		if err != nil {
			t.Fatal(err)
		}
		var frame callFrame
		if err := json.Unmarshal(res, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}

	// Default: includes subcalls, no logs
	defFrame := runWithCfg(`{}`)
	if len(defFrame.Calls) != 1 {
		t.Fatalf("Expected 1 subcall in default mode, got %d", len(defFrame.Calls))
	}
	if len(defFrame.Logs) != 0 {
		t.Fatalf("Expected 0 logs in default mode, got %d", len(defFrame.Logs))
	}

	// onlyTopCall: true, withLog: true
	topWithLog := runWithCfg(`{"onlyTopCall": true, "withLog": true}`)
	if len(topWithLog.Calls) != 0 {
		t.Fatalf("Expected 0 subcalls with onlyTopCall=true, got %d", len(topWithLog.Calls))
	}
	if len(topWithLog.Logs) != 1 {
		t.Fatalf("Expected 1 log with withLog=true, got %d", len(topWithLog.Logs))
	}
	if topWithLog.Logs[0].Address != parentAddr || !bytes.Equal(topWithLog.Logs[0].Data, []byte{0xbb}) {
		t.Errorf("Unexpected log entry: %+v", topWithLog.Logs[0])
	}
}

func TestNativePrestateTracerDiffMode(t *testing.T) {
	caller := common.HexToAddress("0x1111111111111111111111111111111111111111")
	contractAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	slotKey := common.BigToHash(big.NewInt(1))

	// Contract writes 0x99 to storage slot 1: PUSH1 0x99 PUSH1 0x01 SSTORE STOP
	code := []byte{
		byte(vm.PUSH1), 0x99,
		byte(vm.PUSH1), 0x01,
		byte(vm.SSTORE),
		byte(vm.STOP),
	}
	alloc := core.GenesisAlloc{
		caller: {
			Balance: big.NewInt(1000000),
			Nonce:   5,
		},
		contractAddr: {
			Balance: big.NewInt(100),
			Nonce:   1,
			Code:    code,
			Storage: map[common.Hash]common.Hash{
				slotKey: common.BigToHash(big.NewInt(0x11)),
			},
		},
	}
	statedb := tests.MakePreState(ethdb.NewMemDatabase(), alloc)
	tracer, err := NewWithConfig("prestateTracer", nil, json.RawMessage(`{"diffMode": true}`))
	if err != nil {
		t.Fatal(err)
	}
	vmctx := vm.Context{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		Origin:      caller,
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		Difficulty:  big.NewInt(1),
		GasLimit:    1000000,
		GasPrice:    big.NewInt(1),
	}
	evm := vm.NewEVM(vmctx, statedb, params.MainnetChainConfig, vm.Config{Debug: true, Tracer: tracer})
	statedb.SetNonce(caller, statedb.GetNonce(caller)+1)
	if _, _, err := evm.Call(vm.AccountRef(caller), contractAddr, nil, 100000, big.NewInt(50)); err != nil {
		t.Fatal(err)
	}
	res, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	var diff prestateDiffResult
	if err := json.Unmarshal(res, &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Pre[contractAddr] == nil || diff.Post[contractAddr] == nil {
		t.Fatalf("Expected contractAddr in both pre and post diff, got pre=%v post=%v", diff.Pre, diff.Post)
	}
	if diff.Pre[contractAddr].Storage[slotKey] != common.BigToHash(big.NewInt(0x11)) {
		t.Errorf("Expected pre storage 0x11, got %x", diff.Pre[contractAddr].Storage[slotKey])
	}
	if diff.Post[contractAddr].Storage[slotKey] != common.BigToHash(big.NewInt(0x99)) {
		t.Errorf("Expected post storage 0x99, got %x", diff.Post[contractAddr].Storage[slotKey])
	}
}
