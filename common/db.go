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

package common

import (
	"errors"
	"io"
)

var ErrNotFound = errors.New("not found")

// KeyValueReader wraps the Has and Get method of a backing data store.
type KeyValueReader interface {
	// Has retrieves if a key is present in the key-value data store.
	Has(key []byte) (bool, error)

	// Get retrieves the given key if it's present in the key-value data store.
	Get(key []byte) ([]byte, error)
}

// KeyValueWriter wraps the Put method of a backing data store.
type KeyValueWriter interface {
	// Put inserts the given value into the key-value data store.
	Put(key []byte, value []byte) error

	// Delete removes the key from the key-value data store.
	Delete(key []byte) error
}

// Stater wraps the Stat method of a backing data store.
type Stater interface {
	// Stat returns a particular internal stat of the database.
	Stat(property string) (string, error)
}

// Compacter wraps the Compact method of a backing data store.
type Compacter interface {
	// Compact flattens the underlying data store for the given key range. In essence,
	// deleted and overwritten versions are discarded, and the data is rearranged to
	// reduce the cost of operations needed to access them.
	//
	// A nil start is treated as a key before all keys in the data store; a nil limit
	// is treated as a key after all keys in the data store. If both is nil then it
	// will compact entire data store.
	Compact(start, limit []byte) error
}

// KeyValueStore contains all the methods required to allow handling different
// key-value data stores backing the high level database.
type KeyValueStore interface {
	KeyValueReader
	KeyValueWriter
	Batcher
	Iteratee
	Stater
	Compacter
	io.Closer
}

// AncientReader contains the methods required to read from immutable ancient data.
type AncientReader interface {
	// HasAncient returns an indicator whether the specified data exists in the
	// ancient store.
	HasAncient(kind string, number uint64) (bool, error)

	// Ancient retrieves an ancient binary blob from the append-only immutable files.
	Ancient(kind string, number uint64) ([]byte, error)

	// Ancients returns the ancient item numbers in the ancient store.
	Ancients() (uint64, error)

	// AncientSize returns the ancient size of the specified category.
	AncientSize(kind string) (uint64, error)
}

// AncientWriter contains the methods required to write to immutable ancient data.
type AncientWriter interface {
	// AppendAncient injects all binary blobs belong to block at the end of the
	// append-only immutable table files.
	AppendAncient(number uint64, hash, header, body, receipt, td []byte) error

	// TruncateAncients discards all but the first n ancient data from the ancient store.
	TruncateAncients(n uint64) error

	// Sync flushes all in-memory ancient store data to disk.
	Sync() error
}

// Reader contains the methods required to read data from both key-value as well as
// immutable ancient data.
type Reader interface {
	KeyValueReader
	AncientReader
}

// Writer contains the methods required to write data to both key-value as well as
// immutable ancient data.
type Writer interface {
	KeyValueWriter
	AncientWriter
}

// AncientStore contains all the methods required to allow handling different
// ancient data stores backing immutable chain data store.
type AncientStore interface {
	AncientReader
	AncientWriter
	io.Closer
}

// Database wraps all database operations. All methods are safe for concurrent use.
type Database interface {
	io.Closer
	GlobalTable() Table
	BodyTable() Table
	HeaderTable() Table
	ReceiptTable() Table
}

//TODO ditch this? ancient or not?
// Table wraps all mutation & accessor operations. All methods are safe for concurrent use.
type Table interface {
	Reader
	Writer
	Batcher
	Iteratee
	Stater
	Compacter
	io.Closer
}

// IdealBatchSize defines the size of the data batches should ideally add in one
// write.
const IdealBatchSize = 100 * 1024

// Batch is a write-only database that commits changes to its host database
// when Write is called. Batch cannot be used concurrently.
type Batch interface {
	KeyValueWriter

	// ValueSize retrieves the amount of data queued up for writing.
	ValueSize() int

	// Write flushes any accumulated data to disk.
	Write() error

	// Reset resets the batch for reuse.
	Reset()

	// Replay replays the batch contents.
	Replay(w KeyValueWriter) error
}

// Batcher wraps the NewBatch method of a backing data store.
type Batcher interface {
	// NewBatch creates a write-only database that buffers changes to its host db
	// until a final write is called.
	NewBatch() Batch
}

// TablePrefixer represents an wrapper for Database that prefixes all operations with a key prefix.
type TablePrefixer struct {
	table  Table
	prefix string
}

// NewTablePrefixer returns a new instance of TablePrefixer.
func NewTablePrefixer(t Table, prefix string) *TablePrefixer {
	return &TablePrefixer{table: t, prefix: prefix}
}

func (p *TablePrefixer) Put(key []byte, value []byte) error {
	return p.table.Put(append([]byte(p.prefix), key...), value)
}

func (p *TablePrefixer) Has(key []byte) (bool, error) {
	return p.table.Has(append([]byte(p.prefix), key...))
}

func (p *TablePrefixer) Get(key []byte) ([]byte, error) {
	return p.table.Get(append([]byte(p.prefix), key...))
}

func (p *TablePrefixer) Delete(key []byte) error {
	return p.table.Delete(append([]byte(p.prefix), key...))
}

func (p *TablePrefixer) Close() error { return nil }

func (p *TablePrefixer) NewBatch() Batch {
	return &TablePrefixerBatch{p.table.NewBatch(), p.prefix}
}

func (p *TablePrefixer) NewIterator() Iterator {
	panic("implement me")
}

func (p *TablePrefixer) NewIteratorWithStart(start []byte) Iterator {
	panic("implement me")
}

func (p *TablePrefixer) NewIteratorWithPrefix(prefix []byte) Iterator {
	panic("implement me")
}

func (p *TablePrefixer) Stat(property string) (string, error) {
	panic("implement me")
}

func (p *TablePrefixer) Compact(start, limit []byte) error {
	panic("implement me")
}

func (p *TablePrefixer) HasAncient(kind string, number uint64) (bool, error) {
	panic("implement me")
}

func (p *TablePrefixer) Ancient(kind string, number uint64) ([]byte, error) {
	panic("implement me")
}

func (p *TablePrefixer) Ancients() (uint64, error) {
	panic("implement me")
}

func (p *TablePrefixer) AncientSize(kind string) (uint64, error) {
	panic("implement me")
}

func (p *TablePrefixer) AppendAncient(number uint64, hash, header, body, receipt, td []byte) error {
	panic("implement me")
}

func (p *TablePrefixer) TruncateAncients(n uint64) error {
	panic("implement me")
}

func (p *TablePrefixer) Sync() error {
	panic("implement me")
}

type TablePrefixerBatch struct {
	batch  Batch
	prefix string
}

func (b *TablePrefixerBatch) Put(key, value []byte) error {
	return b.batch.Put(append([]byte(b.prefix), key...), value)
}

func (b *TablePrefixerBatch) Delete(key []byte) error {
	return b.batch.Delete(append([]byte(b.prefix), key...))
}

func (b *TablePrefixerBatch) Write() error {
	return b.batch.Write()
}

func (b *TablePrefixerBatch) ValueSize() int {
	return b.batch.ValueSize()
}

func (b *TablePrefixerBatch) Reset() {
	b.batch.Reset()
}

func (b *TablePrefixerBatch) Replay(w KeyValueWriter) error {
	return b.batch.Replay(w)
}

// Iterator iterates over a database's key/value pairs in ascending key order.
//
// When it encounters an error any seek will return false and will yield no key/
// value pairs. The error can be queried by calling the Error method. Calling
// Release is still necessary.
//
// An iterator must be released after use, but it is not necessary to read an
// iterator until exhaustion. An iterator is not safe for concurrent use, but it
// is safe to use multiple iterators concurrently.
type Iterator interface {
	// Next moves the iterator to the next key/value pair. It returns whether the
	// iterator is exhausted.
	Next() bool

	// Error returns any accumulated error. Exhausting all the key/value pairs
	// is not considered to be an error.
	Error() error

	// Key returns the key of the current key/value pair, or nil if done. The caller
	// should not modify the contents of the returned slice, and its contents may
	// change on the next call to Next.
	Key() []byte

	// Value returns the value of the current key/value pair, or nil if done. The
	// caller should not modify the contents of the returned slice, and its contents
	// may change on the next call to Next.
	Value() []byte

	// Release releases associated resources. Release should always succeed and can
	// be called multiple times without causing error.
	Release()
}

// Iteratee wraps the NewIterator methods of a backing data store.
type Iteratee interface {
	// NewIterator creates a binary-alphabetical iterator over the entire keyspace
	// contained within the key-value database.
	NewIterator() Iterator

	// NewIteratorWithStart creates a binary-alphabetical iterator over a subset of
	// database content starting at a particular initial key (or after, if it does
	// not exist).
	NewIteratorWithStart(start []byte) Iterator

	// NewIteratorWithPrefix creates a binary-alphabetical iterator over a subset
	// of database content with a particular key prefix.
	NewIteratorWithPrefix(prefix []byte) Iterator
}
