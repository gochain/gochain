package ethdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/gochain-io/gochain/log"
	"github.com/syndtr/goleveldb/leveldb"
)

var (
	ErrInvalidSegmentFilename = errors.New("invalid segment filename")
)

// SegmentedDatabase represents a database segmented by block number.
// Each segment contains data for a different block number.
type SegmentedDatabase struct {
	mu       sync.RWMutex
	global   Database                    // All data not segmented by block number
	segments map[uint64]*DatabaseSegment // Lookup of segments by block number.
	log      log.Logger                  // Contextual logger tracking the database path

	// Filename of the root database directory.
	Path string

	// Generates a new instance of Database for a segment.
	NewDatabase NewDatabaseFunc
}

// NewSegmentedDatabase returns a new instance of SegmentedDatabase.
func NewSegmentedDatabase(path string) (*SegmentedDatabase, error) {
	global, err := NewLDBDatabase(filepath.Join(path, "global"), 0, 0)
	if err != nil {
		return nil, err
	}

	return &SegmentedDatabase{
		global:   global,
		segments: make(map[uint64]*DatabaseSegment),
		log:      log.New("database", path),

		Path: path,
	}, nil
}

// Close closes all global and segment databases.
func (db *SegmentedDatabase) Close() {
	db.global.Close()
	for _, seg := range db.segments {
		seg.db.Close()
	}
}

// Segment finds a segment by block number.
func (db *SegmentedDatabase) Segment(num uint64) *DatabaseSegment {
	db.mu.RLock()
	seg := db.segments[num]
	db.mu.RUnlock()
	return seg
}

// CreateSegmentIfNotExists finds or creates a segment by block number.
func (db *SegmentedDatabase) CreateSegmentIfNotExists(num uint64) (*DatabaseSegment, error) {
	// Attempt to find segment under read lock.
	if seg := db.Segment(num); seg != nil {
		return seg, nil
	}

	// Recheck under write lock and create if it doesn't exist.
	db.mu.Lock()
	defer db.mu.Unlock()
	if seg := db.segments[num]; seg != nil {
		return seg, nil
	}

	// Generate a database for the segment.
	sdb, err := db.NewDatabase(NewDatabaseOptions{Path: filepath.Join(db.Path, FormatSegmentFilename(num))})
	if err != nil {
		return nil, err
	}

	seg := NewDatabaseSegment(num, sdb)
	db.segments[num] = seg
	return seg, nil
}

// Put writes the key/value pair to the database.
func (db *SegmentedDatabase) Put(key, value []byte) error {
	num, ok := KeyBlockNumber(key)
	if !ok {
		return db.global.Put(key, value)
	}

	seg, err := db.CreateSegmentIfNotExists(num)
	if err != nil {
		return err
	}
	return seg.db.Put(key, value)
}

// Has returns true if key exists.
func (db *SegmentedDatabase) Has(key []byte) (bool, error) {
	num, ok := KeyBlockNumber(key)
	if !ok {
		return db.global.Has(key)
	}

	seg := db.Segment(num)
	if seg == nil {
		return false, nil
	}
	return seg.db.Has(key)
}

// Get returns the value of a given key.
func (db *SegmentedDatabase) Get(key []byte) ([]byte, error) {
	num, ok := KeyBlockNumber(key)
	if !ok {
		return db.global.Get(key)
	}

	seg := db.Segment(num)
	if seg == nil {
		return nil, leveldb.ErrNotFound
	}
	return seg.db.Get(key)
}

// Delete deletes the key from database.
func (db *SegmentedDatabase) Delete(key []byte) error {
	num, ok := KeyBlockNumber(key)
	if !ok {
		return db.global.Delete(key)
	}

	seg := db.Segment(num)
	if seg == nil {
		return nil
	}
	return seg.db.Delete(key)
}

// DatabaseSegment represents data for single block number.
type DatabaseSegment struct {
	blockNumber uint64
	db          Database
}

// NewDatabaseSegment returns a new instance of DatabaseSegment.
func NewDatabaseSegment(blockNumber uint64, db Database) *DatabaseSegment {
	return &DatabaseSegment{blockNumber: blockNumber, db: db}
}

// KeyBlockNumber returns the block number for a given key and returns ok true.
// If the key does not encode the block number then ok is false.
func KeyBlockNumber(key []byte) (num uint64, ok bool) {
	if len(key) < 9 {
		return 0, false
	}
	switch key[0] {
	case 'h', 't', 'n', 'b', 'r': // copied from core/database_util.go
		return binary.BigEndian.Uint64(key[1:9]), true
	default:
		return 0, false
	}
}

// FormatSegmentFilename returns a segment filename.
func FormatSegmentFilename(blockNumber uint64) string {
	return fmt.Sprintf("%016x", blockNumber)
}

// ParseSegmentFilename returns a block number for a given segment filename.
func ParseSegmentFilename(filename string) (uint64, error) {
	blockNumber, err := strconv.ParseUint(filename, 16, 64)
	if err != nil {
		return 0, ErrInvalidSegmentFilename
	}
	return blockNumber, nil
}
