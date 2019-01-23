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

package ethapi

import (
	"sync"

	"github.com/gochain-io/gochain/v3/common"
)

// AddrLocker stores locks per account. This is used to prevent another tx getting the
// same nonce, by holding the lock when signing a transaction.
type AddrLocker struct {
	mu    sync.RWMutex
	locks map[common.Address]*sync.Mutex
}

func newAddrLocker() *AddrLocker {
	return &AddrLocker{
		locks: make(map[common.Address]*sync.Mutex),
	}
}

// lock returns the lock of the given address.
func (l *AddrLocker) lock(address common.Address) sync.Locker {
	l.mu.RLock()
	mu, ok := l.locks[address]
	l.mu.RUnlock()
	if ok {
		return mu
	}
	l.mu.Lock()
	mu, ok = l.locks[address]
	if !ok {
		mu = new(sync.Mutex)
		l.locks[address] = mu
	}
	l.mu.Unlock()
	return mu
}
