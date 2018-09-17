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

// Package state provides a caching layer atop the Ethereum state trie.
package state

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sort"

	"go.opencensus.io/trace"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/crypto"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/rlp"
	"github.com/gochain-io/gochain/trie"
)

type revision struct {
	id           int
	journalIndex int
}

var (
	// emptyState is the known hash of an empty state trie entry.
	emptyState = crypto.Keccak256Hash(nil)

	// emptyCode is the known hash of the empty EVM bytecode.
	emptyCode = crypto.Keccak256Hash(nil)
)

// StateDBs within the ethereum protocol are used to store anything
// within the merkle trie. StateDBs take care of caching and storing
// nested states. It's the general query interface to retrieve:
// * Contracts
// * Accounts
type StateDB struct {
	db   Database
	trie Trie

	// This map holds 'live' objects, which will get modified while processing a state transition.
	stateObjects      map[common.Address]*stateObject
	stateObjectsDirty map[common.Address]struct{}

	// The refund counter, also used by state transitioning.
	refund uint64

	thash, bhash common.Hash
	txIndex      int
	logs         map[common.Hash][]*types.Log
	logSize      uint

	preimages map[common.Hash][]byte

	// Journal of state modifications. This is the backbone of
	// Snapshot and RevertToSnapshot.
	journal        journal
	validRevisions []revision
	nextRevisionId int
}

// Create a new state from a given trie
func New(root common.Hash, db Database) (*StateDB, error) {
	tr, err := db.OpenTrie(root)
	if err != nil {
		return nil, err
	}
	return &StateDB{
		db:                db,
		trie:              tr,
		stateObjects:      make(map[common.Address]*stateObject),
		stateObjectsDirty: make(map[common.Address]struct{}),
		logs:              make(map[common.Hash][]*types.Log),
		preimages:         make(map[common.Hash][]byte),
	}, nil
}

// Reset clears out all emphemeral state objects from the state db, but keeps
// the underlying state trie to avoid reloading data for the next operations.
func (db *StateDB) Reset(root common.Hash) error {
	tr, err := db.db.OpenTrie(root)
	if err != nil {
		return err
	}
	db.trie = tr
	db.stateObjects = make(map[common.Address]*stateObject)
	db.stateObjectsDirty = make(map[common.Address]struct{})
	db.thash = common.Hash{}
	db.bhash = common.Hash{}
	db.txIndex = 0
	db.logs = make(map[common.Hash][]*types.Log)
	db.logSize = 0
	db.preimages = make(map[common.Hash][]byte)
	db.clearJournalAndRefund()
	return nil
}

func (db *StateDB) AddLog(log *types.Log) {
	db.journal = append(db.journal, addLogChange{txhash: db.thash})

	log.TxHash = db.thash
	log.BlockHash = db.bhash
	log.TxIndex = uint(db.txIndex)
	log.Index = db.logSize
	db.logs[db.thash] = append(db.logs[db.thash], log)
	db.logSize++
}

func (db *StateDB) GetLogs(hash common.Hash) []*types.Log {
	return db.logs[hash]
}

func (db *StateDB) Logs() []*types.Log {
	var logs []*types.Log
	for _, lgs := range db.logs {
		logs = append(logs, lgs...)
	}
	return logs
}

// AddPreimage records a SHA3 preimage seen by the VM.
func (db *StateDB) AddPreimage(hash common.Hash, preimage []byte) {
	if _, ok := db.preimages[hash]; !ok {
		db.journal = append(db.journal, addPreimageChange{hash: hash})
		pi := make([]byte, len(preimage))
		copy(pi, preimage)
		db.preimages[hash] = pi
	}
}

// Preimages returns a list of SHA3 preimages that have been submitted.
func (db *StateDB) Preimages() map[common.Hash][]byte {
	return db.preimages
}

func (db *StateDB) AddRefund(gas uint64) {
	db.journal = append(db.journal, refundChange{prev: db.refund})
	db.refund += gas
}

// Exist reports whether the given account address exists in the state.
// Notably this also returns true for suicided accounts.
func (db *StateDB) Exist(addr common.Address) bool {
	so, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	return so != nil
}

// Empty returns whether the state object is either non-existent
// or empty according to the EIP161 specification (balance = nonce = code = 0)
func (db *StateDB) Empty(addr common.Address) bool {
	so, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	return so == nil || so.empty()
}

// Retrieve the balance from the given address or 0 if object not found.
func (db *StateDB) GetBalance(addr common.Address) *big.Int {
	bal, err := db.GetBalanceErr(addr)
	if err != nil {
		log.Error("Failed to get balance", "err", err)
		return common.Big0
	}
	return bal
}

// Retrieve the balance from the given address or an error.
func (db *StateDB) GetBalanceErr(addr common.Address) (*big.Int, error) {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		return nil, err
	}
	if stateObject != nil {
		return stateObject.Balance(), nil
	}
	return common.Big0, nil
}

func (db *StateDB) GetNonce(addr common.Address) uint64 {
	nonce, err := db.GetNonceErr(addr)
	if err != nil {
		log.Error("Failed to get nonce", "err", err)
		return 0
	}
	return nonce
}

func (db *StateDB) GetNonceErr(addr common.Address) (uint64, error) {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		return 0, err
	}
	if stateObject != nil {
		return stateObject.Nonce(), nil
	}

	return 0, nil
}

func (db *StateDB) GetCode(addr common.Address) []byte {
	code, err := db.GetCodeErr(addr)
	if err != nil {
		log.Error("Failed to get code", "err", err)
		return nil
	}
	return code
}

func (db *StateDB) GetCodeErr(addr common.Address) ([]byte, error) {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		return nil, err
	}
	if stateObject != nil {
		return stateObject.Code(db.db), nil
	}
	return nil, nil
}

func (db *StateDB) GetCodeSize(addr common.Address) int {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get code size", "err", err)
		return 0
	}
	if stateObject == nil {
		return 0
	}
	if stateObject.code != nil {
		return len(stateObject.code)
	}
	size, err := db.db.ContractCodeSize(stateObject.addrHash, stateObject.data.CodeHash)
	if err != nil {
		log.Error("Failed to get code size", "err", err)
	}
	return size
}

func (db *StateDB) GetCodeHash(addr common.Address) common.Hash {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get code hash", "err", err)
	} else if stateObject != nil {
		return stateObject.data.CodeHash
	}
	return common.Hash{}

}

func (db *StateDB) GetState(a common.Address, b common.Hash) common.Hash {
	state, err := db.GetStateErr(a, b)
	if err != nil {
		log.Error("Failed to get state", "err", err)
		return common.Hash{}
	}
	return state
}

func (db *StateDB) GetStateErr(a common.Address, b common.Hash) (common.Hash, error) {
	stateObject, err := db.getStateObject(a)
	if err != nil {
		return common.Hash{}, err
	}
	if stateObject != nil {
		return stateObject.GetState(db.db, b), nil
	}
	return common.Hash{}, nil
}

// Database retrieves the low level database supporting the lower level trie ops.
func (db *StateDB) Database() Database {
	return db.db
}

// StorageTrie returns the storage trie of an account.
// The return value is a copy and is nil for non-existent accounts.
func (db *StateDB) StorageTrie(a common.Address) Trie {
	stateObject, err := db.getStateObject(a)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	if stateObject == nil {
		return nil
	}
	cpy := stateObject.deepCopy(db, nil)
	return cpy.updateTrie(db.db)
}

func (db *StateDB) HasSuicided(addr common.Address) bool {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	if stateObject != nil {
		return stateObject.suicided
	}
	return false
}

/*
 * SETTERS
 */

// AddBalance adds amount to the account associated with addr
func (db *StateDB) AddBalance(addr common.Address, amount *big.Int) {
	stateObject := db.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.AddBalance(amount)
	}
}

// SubBalance subtracts amount from the account associated with addr
func (db *StateDB) SubBalance(addr common.Address, amount *big.Int) {
	stateObject := db.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SubBalance(amount)
	}
}

func (db *StateDB) SetBalance(addr common.Address, amount *big.Int) {
	stateObject := db.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetBalance(amount)
	}
}

func (db *StateDB) SetNonce(addr common.Address, nonce uint64) {
	stateObject := db.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetNonce(nonce)
	}
}

func (db *StateDB) SetCode(addr common.Address, code []byte) {
	stateObject := db.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetCode(crypto.Keccak256Hash(code), code)
	}
}

func (db *StateDB) SetState(addr common.Address, key common.Hash, value common.Hash) {
	stateObject := db.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetState(db.db, key, value)
	}
}

// Suicide marks the given account as suicided.
// This clears the account balance.
//
// The account's state object is still available until the state is committed,
// getStateObject will return a non-nil account after Suicide.
func (db *StateDB) Suicide(addr common.Address) bool {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	if stateObject == nil {
		return false
	}
	db.journal = append(db.journal, suicideChange{
		account:     &addr,
		prev:        stateObject.suicided,
		prevbalance: new(big.Int).Set(stateObject.Balance()),
	})
	stateObject.markSuicided()
	stateObject.data.Balance = new(big.Int)

	return true
}

//
// Setting, updating & deleting state object methods
//

// updateStateObject writes the given object to the trie.
func (db *StateDB) updateStateObject(stateObject *stateObject) error {
	addr := stateObject.Address()
	buf := bytes.NewBuffer(make([]byte, 0, stateObject.RLPSize()))
	if err := stateObject.EncodeRLP(buf); err != nil {
		panic(fmt.Errorf("can't encode object at %x: %v", addr[:], err))
	}
	return db.trie.TryUpdate(addr[:], buf.Bytes())
}

// deleteStateObject removes the given object from the state trie.
func (db *StateDB) deleteStateObject(stateObject *stateObject) error {
	stateObject.deleted = true
	addr := stateObject.Address()
	return db.trie.TryDelete(addr[:])
}

// Retrieve a state object given my the address. Returns nil if not found.
func (db *StateDB) getStateObject(addr common.Address) (stateObject *stateObject, err error) {
	// Prefer 'live' objects.
	if obj := db.stateObjects[addr]; obj != nil {
		if obj.deleted {
			return nil, nil
		}
		return obj, nil
	}

	// Load the object from the database.
	enc, err := db.trie.TryGet(addr[:])
	if len(enc) == 0 {
		return nil, err
	}
	var data Account
	if err := rlp.DecodeBytes(enc, &data); err != nil {
		log.Error("Failed to decode state object", "addr", addr, "err", err)
		return nil, nil
	}
	// Insert into the live set.
	obj := newObject(db, addr, data, db.MarkStateObjectDirty)
	db.setStateObject(obj)
	return obj, nil
}

func (db *StateDB) setStateObject(object *stateObject) {
	db.stateObjects[object.Address()] = object
}

// Retrieve a state object or create a new state object if nil
func (db *StateDB) GetOrNewStateObject(addr common.Address) *stateObject {
	stateObject, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	if stateObject == nil || stateObject.deleted {
		stateObject, _ = db.createObject(addr)
	}
	return stateObject
}

// MarkStateObjectDirty adds the specified object to the dirty map to avoid costly
// state object cache iteration to find a handful of modified ones.
func (db *StateDB) MarkStateObjectDirty(addr common.Address) {
	db.stateObjectsDirty[addr] = struct{}{}
}

// createObject creates a new state object. If there is an existing account with
// the given address, it is overwritten and returned as the second return value.
func (db *StateDB) createObject(addr common.Address) (*stateObject, *stateObject) {
	prev, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	newobj := newObject(db, addr, Account{}, db.MarkStateObjectDirty)
	newobj.setNonce(0) // sets the object to dirty
	if prev == nil {
		db.journal = append(db.journal, createObjectChange{account: &addr})
	} else {
		db.journal = append(db.journal, resetObjectChange{prev: prev})
	}
	db.setStateObject(newobj)
	return newobj, prev
}

// CreateAccount explicitly creates a state object. If a state object with the address
// already exists the balance is carried over to the new account.
//
// CreateAccount is called during the EVM CREATE operation. The situation might arise that
// a contract does the following:
//
//   1. sends funds to sha(account ++ (nonce + 1))
//   2. tx_create(sha(account ++ nonce)) (note that this gets the address of 1)
//
// Carrying over the balance ensures that Ether doesn't disappear.
func (db *StateDB) CreateAccount(addr common.Address) {
	new, prev := db.createObject(addr)
	if prev != nil {
		new.setBalance(prev.data.Balance)
	}
}

func (db *StateDB) ForEachStorage(addr common.Address, cb func(key, value common.Hash) bool) {
	so, err := db.getStateObject(addr)
	if err != nil {
		log.Error("Failed to get state object", "err", err)
	}
	if so == nil {
		return
	}

	// When iterating over the storage check the cache first
	for h, value := range so.cachedStorage {
		cb(h, value)
	}

	it := trie.NewIterator(so.getTrie(db.db).NodeIterator(nil))
	for it.Next() {
		// ignore cached values
		key := common.BytesToHash(db.trie.GetKey(it.Key))
		if _, ok := so.cachedStorage[key]; !ok {
			cb(key, common.BytesToHash(it.Value))
		}
	}
}

// Copy creates a deep, independent copy of the state.
// Snapshots of the copied state cannot be applied to the copy.
func (db *StateDB) Copy(ctx context.Context) *StateDB {
	ctx, span := trace.StartSpan(ctx, "StateDB.Copy")
	defer span.End()

	// Copy all the basic fields, initialize the memory ones
	state := &StateDB{
		db:                db.db,
		trie:              db.db.CopyTrie(db.trie),
		stateObjects:      make(map[common.Address]*stateObject, len(db.stateObjectsDirty)),
		stateObjectsDirty: make(map[common.Address]struct{}, len(db.stateObjectsDirty)),
		refund:            db.refund,
		logs:              make(map[common.Hash][]*types.Log, len(db.logs)),
		logSize:           db.logSize,
		preimages:         make(map[common.Hash][]byte),
	}
	// Copy the dirty states, logs, and preimages
	for addr := range db.stateObjectsDirty {
		state.stateObjects[addr] = db.stateObjects[addr].deepCopy(state, state.MarkStateObjectDirty)
		state.stateObjectsDirty[addr] = struct{}{}
	}
	for hash, logs := range db.logs {
		state.logs[hash] = make([]*types.Log, len(logs))
		copy(state.logs[hash], logs)
	}
	for hash, preimage := range db.preimages {
		state.preimages[hash] = preimage
	}
	return state
}

// Snapshot returns an identifier for the current revision of the state.
func (db *StateDB) Snapshot() int {
	id := db.nextRevisionId
	db.nextRevisionId++
	db.validRevisions = append(db.validRevisions, revision{id, len(db.journal)})
	return id
}

// RevertToSnapshot reverts all state changes made since the given revision.
func (db *StateDB) RevertToSnapshot(revid int) {
	// Find the snapshot in the stack of valid snapshots.
	idx := sort.Search(len(db.validRevisions), func(i int) bool {
		return db.validRevisions[i].id >= revid
	})
	if idx == len(db.validRevisions) || db.validRevisions[idx].id != revid {
		panic(fmt.Errorf("revision id %v cannot be reverted", revid))
	}
	snapshot := db.validRevisions[idx].journalIndex

	// Replay the journal to undo changes.
	for i := len(db.journal) - 1; i >= snapshot; i-- {
		db.journal[i].undo(db)
	}
	db.journal = db.journal[:snapshot]

	// Remove invalidated snapshots from the stack.
	db.validRevisions = db.validRevisions[:idx]
}

// GetRefund returns the current value of the refund counter.
func (db *StateDB) GetRefund() uint64 {
	return db.refund
}

// Finalise finalises the state by removing the self destructed objects
// and clears the journal as well as the refunds.
func (db *StateDB) Finalise(deleteEmptyObjects bool) {
	for addr := range db.stateObjectsDirty {
		stateObject := db.stateObjects[addr]
		if stateObject.suicided || (deleteEmptyObjects && stateObject.empty()) {
			db.deleteStateObject(stateObject)
		} else {
			stateObject.updateRoot(db.db)
			db.updateStateObject(stateObject)
		}
	}
	// Invalidate journal because reverting across transactions is not allowed.
	db.clearJournalAndRefund()
}

// IntermediateRoot computes the current root hash of the state trie.
// It is called in between transactions to get the root hash that
// goes into transaction receipts.
func (db *StateDB) IntermediateRoot(deleteEmptyObjects bool) common.Hash {
	db.Finalise(deleteEmptyObjects)
	return db.trie.Hash()
}

// Prepare sets the current transaction hash and index and block hash which is
// used when the EVM emits new state logs.
func (db *StateDB) Prepare(thash, bhash common.Hash, ti int) {
	db.thash = thash
	db.bhash = bhash
	db.txIndex = ti
}

func (s *StateDB) clearJournalAndRefund() {
	s.journal = nil
	s.validRevisions = s.validRevisions[:0]
	s.refund = 0
}

// Commit writes the state to the underlying in-memory trie database.
func (db *StateDB) Commit(deleteEmptyObjects bool) (root common.Hash, err error) {
	defer db.clearJournalAndRefund()

	// Commit objects to the trie.
	for addr, stateObject := range db.stateObjects {
		_, isDirty := db.stateObjectsDirty[addr]
		switch {
		case stateObject.suicided || (isDirty && deleteEmptyObjects && stateObject.empty()):
			// If the object has been removed, don't bother syncing it
			// and just mark it for deletion in the trie.
			db.deleteStateObject(stateObject)
		case isDirty:
			// Write any contract code associated with the state object
			if stateObject.code != nil && stateObject.dirtyCode {
				db.db.TrieDB().Insert(stateObject.data.CodeHash, stateObject.code)
				stateObject.dirtyCode = false
			}
			// Write any storage changes in the state object to its storage trie.
			if err := stateObject.CommitTrie(db.db); err != nil {
				return common.Hash{}, err
			}
			// Update the object in the main account trie.
			db.updateStateObject(stateObject)
		}
		delete(db.stateObjectsDirty, addr)
	}
	// Write trie changes.
	root, err = db.trie.Commit(func(leaf []byte, parent common.Hash) error {
		var account Account
		if err := rlp.DecodeBytes(leaf, &account); err != nil {
			return nil
		}
		if account.Root != emptyState {
			db.db.TrieDB().Reference(account.Root, parent)
		}
		code := account.CodeHash
		if code != emptyCode {
			db.db.TrieDB().Reference(code, parent)
		}
		return nil
	})
	log.Debug("Trie cache stats after commit", "misses", trie.CacheMisses(), "unloads", trie.CacheUnloads())
	return root, err
}
