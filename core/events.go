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
	"sync"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/log"
)

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
	subs []chan<- NewTxsEvent
}

func (f *NewTxsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *NewTxsFeed) Subscribe(ch chan<- NewTxsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *NewTxsFeed) Unsubscribe(ch chan<- NewTxsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *NewTxsFeed) Send(ev NewTxsEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- ev:
		default:
			log.Trace("NewTxsFeed send dropped: channel full", "cap", cap(sub), "txs", len(ev.Txs))
		}
	}
}

type ChainFeed struct {
	mu   sync.RWMutex
	subs []chan<- ChainEvent
}

func (f *ChainFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *ChainFeed) Subscribe(ch chan<- ChainEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *ChainFeed) Unsubscribe(ch chan<- ChainEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *ChainFeed) Send(ev ChainEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- ev:
		default:
			log.Info("ChainFeed send dropped: channel full", "cap", cap(sub), "block", ev.Block.NumberU64(), "hash", ev.Hash)
		}
	}
}

type ChainHeadFeed struct {
	mu   sync.RWMutex
	subs []chan<- ChainHeadEvent
}

func (f *ChainHeadFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *ChainHeadFeed) Subscribe(ch chan<- ChainHeadEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *ChainHeadFeed) Unsubscribe(ch chan<- ChainHeadEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *ChainHeadFeed) Send(ev ChainHeadEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- ev:
		default:
			log.Info("ChainHeadFeed send dropped: channel full", "cap", cap(sub), "block", ev.Block.NumberU64(), "hash", ev.Block.Hash())
		}
	}
}

type ChainSideFeed struct {
	mu   sync.RWMutex
	subs []chan<- ChainSideEvent
}

func (f *ChainSideFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *ChainSideFeed) Subscribe(ch chan<- ChainSideEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *ChainSideFeed) Unsubscribe(ch chan<- ChainSideEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *ChainSideFeed) Send(ev ChainSideEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- ev:
		default:
			log.Info("ChainSideFeed send dropped: channel full", "cap", cap(sub), "block", ev.Block.NumberU64(), "hash", ev.Block.Hash())
		}
	}
}

type PendingLogsFeed struct {
	mu   sync.RWMutex
	subs []chan<- PendingLogsEvent
}

func (f *PendingLogsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *PendingLogsFeed) Subscribe(ch chan<- PendingLogsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *PendingLogsFeed) Unsubscribe(ch chan<- PendingLogsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *PendingLogsFeed) Send(ev PendingLogsEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- ev:
		default:
			log.Info("PendingLogsFeed send dropped: channel full", "cap", cap(sub), "len", len(ev.Logs))
		}
	}
}

type RemovedLogsFeed struct {
	mu   sync.RWMutex
	subs []chan<- RemovedLogsEvent
}

func (f *RemovedLogsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *RemovedLogsFeed) Subscribe(ch chan<- RemovedLogsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *RemovedLogsFeed) Unsubscribe(ch chan<- RemovedLogsEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *RemovedLogsFeed) Send(ev RemovedLogsEvent) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- ev:
		default:
			log.Info("RemovedLogsFeed send dropped: channel full", "cap", cap(sub), "len", len(ev.Logs))
		}
	}
}

type LogsFeed struct {
	mu   sync.RWMutex
	subs []chan<- []*types.Log
}

func (f *LogsFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		close(sub)
	}
	f.subs = nil
}

func (f *LogsFeed) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.subs)
}

func (f *LogsFeed) Subscribe(ch chan<- []*types.Log) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

func (f *LogsFeed) Unsubscribe(ch chan<- []*types.Log) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.subs {
		if s == ch {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (f *LogsFeed) Send(logs []*types.Log) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sub := range f.subs {
		select {
		case sub <- logs:
		default:
			log.Info("LogsFeed send dropped: channel full", "cap", cap(sub), "len", len(logs))
		}
	}
}
