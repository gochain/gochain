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

package state

import (
	"bytes"
	"fmt"
	"io"
	"math/big"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/crypto"
	"github.com/gochain-io/gochain/rlp"
)

var (
	emptyCodeHash = crypto.Keccak256Hash(nil)
	zeroHash      common.Hash
)

type Code []byte

func (self Code) String() string {
	return string(self) //strings.Join(Disassemble(self), " ")
}

type Storage map[common.Hash]common.Hash

func (self Storage) String() (str string) {
	for key, value := range self {
		str += fmt.Sprintf("%X : %X\n", key, value)
	}

	return
}

func (self Storage) Copy() Storage {
	cpy := make(Storage)
	for key, value := range self {
		cpy[key] = value
	}

	return cpy
}

// stateObject represents an Ethereum account which is being modified.
//
// The usage pattern is as follows:
// First you need to obtain a state object.
// Account values can be accessed and modified through the object.
// Finally, call CommitTrie to write the modified storage trie into a database.
type stateObject struct {
	address  common.Address
	addrHash common.Hash // hash of ethereum address of the account
	data     Account
	db       *StateDB

	// DB error.
	// State objects are used by the consensus core and VM which are
	// unable to deal with database-level errors. Any error that occurs
	// during a database read is memoized here and will eventually be returned
	// by StateDB.Commit.
	dbErr error

	// Write caches.
	trie Trie // storage trie, which becomes non-nil on first access
	code Code // contract bytecode, which gets set when code is loaded

	originStorage Storage // Storage cache of original entries to dedup rewrites
	dirtyStorage  Storage // Storage entries that need to be flushed to disk

	// Cache flags.
	// When an object is marked suicided it will be delete from the trie
	// during the "update" phase of the state transition.
	dirtyCode bool // true if the code was updated
	suicided  bool
	deleted   bool
}

// empty returns whether the account is considered empty.
func (so *stateObject) empty() bool {
	return so.data.Nonce == 0 && so.data.Balance.Sign() == 0 && so.data.CodeHash == emptyCodeHash
}

// Account is the Ethereum consensus representation of accounts.
// These objects are stored in the main account trie.
type Account struct {
	Nonce    uint64
	Balance  *big.Int
	Root     common.Hash // merkle root of the storage trie
	CodeHash common.Hash
}

func (a *Account) RLPSize() int {
	return rlp.Uint64Size(a.Nonce) +
		rlp.BigIntSize(a.Balance) +
		rlp.BytesSize([]byte(a.Root[:])) +
		rlp.BytesSize([]byte(a.CodeHash[:]))
}

// EncodeRLP implements rlp.Encoder.
func (a *Account) EncodeRLP(w io.Writer) error {
	if _, err := rlp.WriteListHeaderTo(w, a.RLPSize()); err != nil {
		return err
	}

	if _, err := rlp.WriteUint64To(w, a.Nonce); err != nil {
		return err
	} else if _, err := rlp.WriteBigIntTo(w, a.Balance); err != nil {
		return err
	} else if _, err := rlp.WriteBytesTo(w, []byte(a.Root[:])); err != nil {
		return err
	} else if _, err := rlp.WriteBytesTo(w, []byte(a.CodeHash[:])); err != nil {
		return err
	}
	return nil
}

// MarshalRLP returns an RLP encoded byte slice.
func (a *Account) MarshalRLP() (_ []byte, err error) {
	buf := make([]byte, rlp.MaxHeadSize, rlp.MaxHeadSize+rlp.Uint64Size(a.Nonce)+rlp.MaxBigIntSize+rlp.BytesSize([]byte(a.Root[:]))+rlp.BytesSize([]byte(a.CodeHash[:])))
	buf = rlp.AppendUint64(buf, a.Nonce)
	if buf, err = rlp.AppendBigInt(buf, a.Balance); err != nil {
		return nil, err
	}
	buf = rlp.AppendBytes(buf, []byte(a.Root[:]))
	buf = rlp.AppendBytes(buf, []byte(a.CodeHash[:]))
	buf = rlp.PrependListHeader(buf)
	return buf, nil
}

// newObject creates a state object.
func newObject(db *StateDB, address common.Address, data Account) *stateObject {
	if data.Balance == nil {
		data.Balance = new(big.Int)
	}
	if data.CodeHash == zeroHash {
		data.CodeHash = emptyCodeHash
	}
	return &stateObject{
		db:            db,
		address:       address,
		addrHash:      crypto.Keccak256Hash(address[:]),
		data:          data,
		originStorage: make(Storage),
		dirtyStorage:  make(Storage),
	}
}

// RLPSize returns the size of the encoded RLP object.
func (so *stateObject) RLPSize() int {
	return so.data.RLPSize()
}

// EncodeRLP implements rlp.Encoder.
func (so *stateObject) EncodeRLP(w io.Writer) error {
	return so.data.EncodeRLP(w)
	// return rlp.Encode(w, so.data)
}

// MarshalRLP returns an RLP encoded byte slice.
func (so *stateObject) MarshalRLP() ([]byte, error) {
	return so.data.MarshalRLP()
}

// setError remembers the first non-nil error it is called with.
func (so *stateObject) setError(err error) {
	if so.dbErr == nil {
		so.dbErr = err
	}
}

func (so *stateObject) markSuicided() {
	so.suicided = true
}

func (so *stateObject) touch() {
	so.db.journal.append(touchChange{
		account: &so.address,
	})
	if so.address == ripemd {
		// Explicitly put it in the dirty-cache, which is otherwise generated from
		// flattened journals.
		so.db.journal.dirty(so.address)
	}
}

func (so *stateObject) getTrie(db Database) Trie {
	if so.trie == nil {
		var err error
		so.trie, err = db.OpenStorageTrie(so.addrHash, so.data.Root)
		if err != nil {
			so.trie, _ = db.OpenStorageTrie(so.addrHash, common.Hash{})
			so.setError(fmt.Errorf("can't create storage trie: %v", err))
		}
	}
	return so.trie
}

// GetState retrieves a value from the account storage trie.
func (so *stateObject) GetState(db Database, key common.Hash) common.Hash {
	// If we have a dirty value for this state entry, return it
	value, dirty := so.dirtyStorage[key]
	if dirty {
		return value
	}
	// Otherwise return the entry's original value
	return so.GetCommittedState(db, key)
}

// GetCommittedState retrieves a value from the committed account storage trie.
func (so *stateObject) GetCommittedState(db Database, key common.Hash) common.Hash {
	// If we have the original value cached, return that
	value, cached := so.originStorage[key]
	if cached {
		return value
	}
	// Otherwise load the value from the database
	enc, err := so.getTrie(db).TryGet(key[:])
	if err != nil {
		so.setError(err)
		return common.Hash{}
	}
	if len(enc) > 0 {
		_, content, _, err := rlp.Split(enc)
		if err != nil {
			so.setError(err)
		}
		value.SetBytes(content)
	}
	so.originStorage[key] = value
	return value
}

// SetState updates a value in account storage.
func (so *stateObject) SetState(db Database, key, value common.Hash) {
	// If the new value is the same as old, don't set
	prev := so.GetState(db, key)
	if prev == value {
		return
	}
	// New value is different, update and journal the change
	so.db.journal.append(storageChange{
		account:  &so.address,
		key:      key,
		prevalue: prev,
	})
	so.setState(key, value)
}

func (so *stateObject) setState(key, value common.Hash) {
	so.dirtyStorage[key] = value
}

// updateTrie writes cached storage modifications into the object's storage trie.
func (so *stateObject) updateTrie(db Database) Trie {
	tr := so.getTrie(db)
	for key, value := range so.dirtyStorage {
		delete(so.dirtyStorage, key)

		// Skip noop changes, persist actual changes
		if value == so.originStorage[key] {
			continue
		}
		so.originStorage[key] = value

		if (value == common.Hash{}) {
			so.setError(tr.TryDelete(key[:]))
			continue
		}
		// Encoding []byte cannot fail, ok to ignore the error.
		v, _ := rlp.EncodeToBytes(bytes.TrimLeft(value[:], "\x00"))
		so.setError(tr.TryUpdate(key[:], v))
	}
	return tr
}

// UpdateRoot sets the trie root to the current root hash of
func (so *stateObject) updateRoot(db Database) {
	so.updateTrie(db)
	so.data.Root = so.trie.Hash()
}

// CommitTrie the storage trie of the object to db.
// This updates the trie root.
func (so *stateObject) CommitTrie(db Database) error {
	so.updateTrie(db)
	if so.dbErr != nil {
		return so.dbErr
	}
	root, err := so.trie.Commit(nil)
	if err == nil {
		so.data.Root = root
	}
	return err
}

// AddBalance removes amount from c's balance.
// It is used to add funds to the destination account of a transfer.
func (so *stateObject) AddBalance(amount *big.Int) {
	// EIP158: We must check emptiness for the objects such that the account
	// clearing (0,0,0 objects) can take effect.
	if amount.Sign() == 0 {
		if so.empty() {
			so.touch()
		}

		return
	}
	so.SetBalance(new(big.Int).Add(so.Balance(), amount))
}

// SubBalance removes amount from c's balance.
// It is used to remove funds from the origin account of a transfer.
func (so *stateObject) SubBalance(amount *big.Int) {
	if amount.Sign() == 0 {
		return
	}
	so.SetBalance(new(big.Int).Sub(so.Balance(), amount))
}

func (so *stateObject) SetBalance(amount *big.Int) {
	so.db.journal.append(balanceChange{
		account: &so.address,
		prev:    so.data.Balance,
	})
	so.setBalance(amount)
}

func (so *stateObject) setBalance(amount *big.Int) {
	so.data.Balance = amount
}

// Return the gas back to the origin. Used by the Virtual machine or Closures
func (so *stateObject) ReturnGas(gas *big.Int) {}

func (so *stateObject) deepCopy(db *StateDB) *stateObject {
	stateObject := newObject(db, so.address, so.data)
	if so.trie != nil {
		stateObject.trie = db.db.CopyTrie(so.trie)
	}
	stateObject.code = so.code
	stateObject.dirtyStorage = so.dirtyStorage.Copy()
	stateObject.originStorage = so.originStorage.Copy()
	stateObject.suicided = so.suicided
	stateObject.dirtyCode = so.dirtyCode
	stateObject.deleted = so.deleted
	return stateObject
}

//
// Attribute accessors
//

// Returns the address of the contract/account
func (so *stateObject) Address() common.Address {
	return so.address
}

// Code returns the contract code associated with this object, if any.
func (so *stateObject) Code(db Database) []byte {
	if so.code != nil {
		return so.code
	}
	if so.data.CodeHash == emptyCodeHash {
		return nil
	}
	code, err := db.ContractCode(so.addrHash, so.data.CodeHash)
	if err != nil {
		so.setError(fmt.Errorf("can't load code hash %x: %v", so.data.CodeHash, err))
	}
	so.code = code
	return code
}

func (so *stateObject) SetCode(codeHash common.Hash, code []byte) {
	prevcode := so.Code(so.db.db)
	so.db.journal.append(codeChange{
		account:  &so.address,
		prevhash: so.data.CodeHash,
		prevcode: prevcode,
	})
	so.setCode(codeHash, code)
}

func (so *stateObject) setCode(codeHash common.Hash, code []byte) {
	so.code = code
	so.data.CodeHash = codeHash
	so.dirtyCode = true
}

func (so *stateObject) SetNonce(nonce uint64) {
	so.db.journal.append(nonceChange{
		account: &so.address,
		prev:    so.data.Nonce,
	})
	so.setNonce(nonce)
}

func (so *stateObject) setNonce(nonce uint64) {
	so.data.Nonce = nonce
}

func (so *stateObject) Balance() *big.Int {
	return so.data.Balance
}

func (so *stateObject) Nonce() uint64 {
	return so.data.Nonce
}

// Never called, but must be present to allow stateObject to be used
// as a vm.Account interface that also satisfies the vm.ContractRef
// interface. Interfaces are awesome.
func (so *stateObject) Value() *big.Int {
	panic("Value on stateObject should never be called")
}
