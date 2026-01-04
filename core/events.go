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
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/core/types"
	"github.com/gochain/gochain/v5/log"
)

const timeout = 500 * time.Millisecond

// NewTxsEvent is posted when a batch of transactions enter the transaction pool.
type NewTxsEvent struct{ Txs []*types.Transaction }

// PendingLogsEvent is posted pre mining and notifies of pending logs.
type PendingLogsEvent struct {
	Logs []*types.Log
}

// PendingStateEvent is posted pre mining and notifies of pending state changes.
type PendingStateEvent struct{}

// NewMinedBlockEvent is posted when a block has been imported.
type NewMinedBlockEvent struct{ Block *types.Block }

// RemovedLogsEvent is posted when a reorg happens
type RemovedLogsEvent struct{ Logs []*types.Log }

type ChainEvent struct {
	Block *types.Block
	Hash  common.Hash
	Logs  []*types.Log
}

type ChainSideEvent struct {
	Block *types.Block
}

type ChainHeadEvent struct{ Block *types.Block }

type NewTxsFeed struct {
	mu   sync.RWMutex
	subs map[chan<- NewTxsEvent]string
}

func (f *NewTxsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *NewTxsFeed) Subscribe(ch chan<- NewTxsEvent, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- NewTxsEvent]string)
	}
	f.subs[ch] = name
}

func (f *NewTxsFeed) Unsubscribe(ch chan<- NewTxsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *NewTxsFeed) Send(ev NewTxsEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- ev:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- ev:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("NewTxsFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "txs", len(ev.Txs))
		}
	}
}

type ChainFeed struct {
	mu   sync.RWMutex
	subs map[chan<- ChainEvent]string
}

func (f *ChainFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *ChainFeed) Subscribe(ch chan<- ChainEvent, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- ChainEvent]string)
	}
	f.subs[ch] = name
}

func (f *ChainFeed) Unsubscribe(ch chan<- ChainEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *ChainFeed) Send(ev ChainEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- ev:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- ev:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("ChainFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "block", ev.Block.NumberU64(), "hash", ev.Hash)
		}
	}
}

type ChainHeadFeed struct {
	mu   sync.RWMutex
	subs map[chan<- ChainHeadEvent]string
}

func (f *ChainHeadFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *ChainHeadFeed) Subscribe(ch chan<- ChainHeadEvent, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- ChainHeadEvent]string)
	}
	f.subs[ch] = name
}

func (f *ChainHeadFeed) Unsubscribe(ch chan<- ChainHeadEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *ChainHeadFeed) Send(ev ChainHeadEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- ev:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- ev:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("ChainHeadFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "block", ev.Block.NumberU64(), "hash", ev.Block.Hash())
		}
	}
}

type ChainSideFeed struct {
	mu   sync.RWMutex
	subs map[chan<- ChainSideEvent]string
}

func (f *ChainSideFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *ChainSideFeed) Subscribe(ch chan<- ChainSideEvent, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- ChainSideEvent]string)
	}
	f.subs[ch] = name
}

func (f *ChainSideFeed) Unsubscribe(ch chan<- ChainSideEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *ChainSideFeed) Send(ev ChainSideEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- ev:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- ev:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("ChainSideFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "block", ev.Block.NumberU64(), "hash", ev.Block.Hash())
		}
	}
}

type PendingLogsFeed struct {
	mu   sync.RWMutex
	subs map[chan<- PendingLogsEvent]string
}

func (f *PendingLogsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *PendingLogsFeed) Subscribe(ch chan<- PendingLogsEvent, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- PendingLogsEvent]string)
	}
	f.subs[ch] = name
}

func (f *PendingLogsFeed) Unsubscribe(ch chan<- PendingLogsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *PendingLogsFeed) Send(ev PendingLogsEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- ev:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- ev:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("PendingLogsFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "len", len(ev.Logs))
		}
	}
}

type RemovedLogsFeed struct {
	mu   sync.RWMutex
	subs map[chan<- RemovedLogsEvent]string
}

func (f *RemovedLogsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *RemovedLogsFeed) Subscribe(ch chan<- RemovedLogsEvent, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- RemovedLogsEvent]string)
	}
	f.subs[ch] = name
}

func (f *RemovedLogsFeed) Unsubscribe(ch chan<- RemovedLogsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *RemovedLogsFeed) Send(ev RemovedLogsEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- ev:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- ev:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("RemovedLogsFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "len", len(ev.Logs))
		}
	}
}

type LogsFeed struct {
	mu   sync.RWMutex
	subs map[chan<- []*types.Log]string
}

func (f *LogsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *LogsFeed) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.subs)
}

func (f *LogsFeed) Subscribe(ch chan<- []*types.Log, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- []*types.Log]string)
	}
	f.subs[ch] = name
}

func (f *LogsFeed) Unsubscribe(ch chan<- []*types.Log) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *LogsFeed) Send(logs []*types.Log) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- logs:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- logs:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("LogsFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "len", len(logs))
		}
	}
}

type BlockProcFeed struct {
	mu   sync.RWMutex
	subs map[chan<- bool]string
}

func (f *BlockProcFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *BlockProcFeed) Subscribe(ch chan<- bool, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- bool]string)
	}
	f.subs[ch] = name
}

func (f *BlockProcFeed) Unsubscribe(ch chan<- bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *BlockProcFeed) Send(b bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- b:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- b:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("BlockProcFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "val", b)
		}
	}
}

type Int64Feed struct {
	mu   sync.RWMutex
	subs map[chan<- int64]string
}

func (f *Int64Feed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *Int64Feed) Subscribe(ch chan<- int64, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- int64]string)
	}
	f.subs[ch] = name
}

func (f *Int64Feed) Unsubscribe(ch chan<- int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *Int64Feed) Send(e int64) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- e:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- e:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("Int64Feed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "val", e)
		}
	}
}

type IntFeed struct {
	mu   sync.RWMutex
	subs map[chan<- int]string
}

func (f *IntFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *IntFeed) Subscribe(ch chan<- int, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- int]string)
	}
	f.subs[ch] = name
}

func (f *IntFeed) Unsubscribe(ch chan<- int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *IntFeed) Send(e int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- e:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- e:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("IntFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "val", e)
		}
	}
}

type InterfaceFeed struct {
	mu   sync.RWMutex
	subs map[chan<- interface{}]string
}

func (f *InterfaceFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *InterfaceFeed) Subscribe(ch chan<- interface{}, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[chan<- interface{}]string)
	}
	f.subs[ch] = name
}

func (f *InterfaceFeed) Unsubscribe(ch chan<- interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *InterfaceFeed) Send(e interface{}) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for sub, name := range f.subs {
		select {
		case sub <- e:
		default:
			start := time.Now()
			var action string
			select {
			case sub <- e:
				action = "delayed"
			case <-time.After(timeout):
				action = "dropped"
			}
			dur := time.Since(start)
			log.Warn(fmt.Sprintf("InterfaceFeed send %s: channel full", action), "name", name, "cap", cap(sub), "time", dur, "type", reflect.TypeOf(e))
		}
	}
}
