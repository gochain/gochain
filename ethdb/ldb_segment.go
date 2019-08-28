package ethdb

import (
	"github.com/gochain/gochain/v3/common"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Ensure implementation implements interface.
var _ MutableSegment = (*LDBSegment)(nil)

// LDBSegement represents a mutable segment in a Table.
// These segments can eventually be rebuilt into immutable FileSegments.
type LDBSegment struct {
	db *leveldb.DB

	name string
	path string
}

// NewLDBSegment returns a LevelDB-based database segment.
func NewLDBSegment(name, path string) *LDBSegment {
	return &LDBSegment{name: name, path: path}
}

// Open initializes the underlying segment database.
func (s *LDBSegment) Open() (err error) {
	s.db, err = leveldb.OpenFile(s.path, &opt.Options{
		OpenFilesCacheCapacity: 16,
		BlockCacheCapacity:     16 / 2 * opt.MiB,
		WriteBuffer:            16 / 4 * opt.MiB,
		Filter:                 filter.NewBloomFilter(10),
	})
	return err
}

// Close closes the underlying database.
func (s *LDBSegment) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Name returns the name of the segment.
func (s *LDBSegment) Name() string { return s.name }

// Path returns the path to the segment.
func (s *LDBSegment) Path() string { return s.path }

// LDB returns the underlying LevelDB database.
func (s *LDBSegment) LDB() *leveldb.DB { return s.db }

// Has returns true if the segment contains key.
func (s *LDBSegment) Has(key []byte) (bool, error) {
	return s.db.Has(key, nil)
}

// Get returns the given key if it's present.
func (s *LDBSegment) Get(key []byte) ([]byte, error) {
	value, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, common.ErrNotFound
	}
	return value, err
}

// Put inserts a value into a given key.
func (s *LDBSegment) Put(key []byte, value []byte) error {
	return s.db.Put(key, value, nil)
}

// Delete deletes the key from the queue and database
func (s *LDBSegment) Delete(key []byte) error {
	return s.db.Delete(key, nil)
}

func (s *LDBSegment) newBatch() *ldbSegmentBatch {
	return &ldbSegmentBatch{segment: s, batch: new(leveldb.Batch)}
}

// Iterator returns a sequential iterator for the segment.
func (s *LDBSegment) Iterator() SegmentIterator {
	return &ldbSegmentIterator{s.db.NewIterator(nil, nil)}
}

// CompactTo writes the segment to disk as a file segment.
func (s *LDBSegment) CompactTo(path string) error {
	enc := NewFileSegmentEncoder(path)
	if err := enc.Open(); err != nil {
		return err
	}

	itr := s.Iterator()
	defer itr.Close()

	// Copy all LDB key/value pairs to the file segment.
	for itr.Next() {
		if err := enc.EncodeKeyValue(itr.Key(), itr.Value()); err != nil {
			return err
		}
	}
	if err := itr.Close(); err != nil {
		return err
	}

	// Write out file segment.
	if err := enc.Flush(); err != nil {
		return err
	} else if err := enc.Close(); err != nil {
		return err
	}
	return nil
}

// UncompactSegmentTo writes an LDB segment from a file segment.
func UncompactSegmentTo(s Segment, path string) error {
	ldbSegment := NewLDBSegment(s.Name(), path)
	if err := ldbSegment.Open(); err != nil {
		return err
	}
	defer ldbSegment.Close()

	itr := s.Iterator()
	defer itr.Close()

	for itr.Next() {
		if err := ldbSegment.Put(itr.Key(), itr.Value()); err != nil {
			return err
		}
	}

	return ldbSegment.Close()
}

// ldbSegmentIterator represents an adapter between goleveldb and the ethdb iterator.
type ldbSegmentIterator struct {
	iterator.Iterator
}

func (itr *ldbSegmentIterator) Close() (err error) {
	err = itr.Error()
	itr.Release()
	return err
}

type ldbSegmentBatch struct {
	segment *LDBSegment
	batch   *leveldb.Batch
	size    int
}

func (b *ldbSegmentBatch) Put(key, value []byte) error {
	b.batch.Put(key, value)
	b.size += len(value)
	return nil
}

func (b *ldbSegmentBatch) Delete(key []byte) error {
	b.batch.Delete(key)
	return nil
}

func (b *ldbSegmentBatch) Write() error {
	return b.segment.db.Write(b.batch, nil)
}

func (b *ldbSegmentBatch) ValueSize() int {
	return b.size
}

func (b *ldbSegmentBatch) Reset() {
	b.batch.Reset()
	b.size = 0
}
