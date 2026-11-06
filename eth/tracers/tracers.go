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

// Package tracers is a collection of transaction tracers.
package tracers

import (
	"encoding/json"
	"math/big"
	"strings"
	"time"
	"unicode"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/core/vm"
	"github.com/gochain/gochain/v5/eth/tracers/internal/tracers"
)

// Context contains some contextual infos for a transaction execution that is not
// available from within the EVM object.
type Context struct {
	BlockHash   common.Hash // Hash of the block the tx is contained within (zero if dangling tx or call)
	BlockNumber *big.Int    // Number of the block the tx is contained within (zero if dangling tx or call)
	TxIndex     int         // Index of the transaction within a block (zero if dangling tx or call)
	TxHash      common.Hash // Hash of the transaction being traced (zero if dangling tx or call)
}

// TracerInterface is the interface implemented by all transaction tracers (JS and native).
type TracerInterface interface {
	vm.Tracer
	GetResult() (json.RawMessage, error)
	Stop(err error)
}

type ctorFunc func(*Context, json.RawMessage) (TracerInterface, error)

var (
	// all contains all the built in JavaScript tracers by name.
	all = make(map[string]string)
	// native contains all registered native Go tracers by name.
	native = make(map[string]ctorFunc)
)

// Register registers a native Go tracer by name.
func Register(name string, ctor func(*Context, json.RawMessage) (TracerInterface, error)) {
	native[name] = ctor
}

// camel converts a snake cased input string into a camel cased output.
func camel(str string) string {
	pieces := strings.Split(str, "_")
	for i := 1; i < len(pieces); i++ {
		pieces[i] = string(unicode.ToUpper(rune(pieces[i][0]))) + pieces[i][1:]
	}
	return strings.Join(pieces, "")
}

// init retrieves the JavaScript transaction tracers included in go-ethereum
// and registers the built-in native Go tracers.
func init() {
	for _, file := range tracers.AssetNames() {
		name := camel(strings.TrimSuffix(file, ".js"))
		all[name] = string(tracers.MustAsset(file))
	}
	Register("callTracer", newCallTracer)
	Register("prestateTracer", newPrestateTracer)
	Register("4byteTracer", newFourByteTracer)
	Register("noopTracer", newNoopTracer)
}

// tracer retrieves a specific JavaScript tracer by name.
func tracer(name string) (string, bool) {
	if tracer, ok := all[name]; ok {
		return tracer, true
	}
	return "", false
}

// New instantiates a new tracer instance by name or JavaScript snippet.
func New(code string) (*Tracer, error) {
	return NewWithConfig(code, nil, nil)
}

// NewWithConfig instantiates a new tracer instance by name or JavaScript snippet
// with optional execution context and tracer-specific JSON configuration.
func NewWithConfig(code string, ctx *Context, cfg json.RawMessage) (*Tracer, error) {
	if ctor, ok := native[code]; ok {
		t, err := ctor(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &Tracer{inner: t}, nil
	}
	if jsCode, ok := tracer(code); ok {
		code = jsCode
	}
	return NewJsTracer(code, ctx, cfg)
}

// Tracer wraps an underlying native or JavaScript tracer and implements vm.Tracer.
type Tracer struct {
	inner TracerInterface
}

func (t *Tracer) CaptureStart(from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) error {
	return t.inner.CaptureStart(from, to, create, input, gas, value)
}

func (t *Tracer) CaptureState(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	return t.inner.CaptureState(env, pc, op, gas, cost, memory, stack, contract, depth, err)
}

func (t *Tracer) CaptureFault(env *vm.EVM, pc uint64, op vm.OpCode, gas, cost uint64, memory *vm.Memory, stack *vm.Stack, contract *vm.Contract, depth int, err error) error {
	return t.inner.CaptureFault(env, pc, op, gas, cost, memory, stack, contract, depth, err)
}

func (t *Tracer) CaptureEnd(output []byte, gasUsed uint64, d time.Duration, err error) error {
	return t.inner.CaptureEnd(output, gasUsed, d, err)
}

func (t *Tracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if enterer, ok := t.inner.(interface {
		CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int)
	}); ok {
		enterer.CaptureEnter(typ, from, to, input, gas, value)
	}
}

func (t *Tracer) CaptureExit(output []byte, gasUsed uint64, err error) {
	if exiter, ok := t.inner.(interface {
		CaptureExit(output []byte, gasUsed uint64, err error)
	}); ok {
		exiter.CaptureExit(output, gasUsed, err)
	}
}

func (t *Tracer) GetResult() (json.RawMessage, error) {
	return t.inner.GetResult()
}

func (t *Tracer) Stop(err error) {
	t.inner.Stop(err)
}
