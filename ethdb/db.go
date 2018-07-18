package ethdb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/log"
)

// Database errors.
var (
	ErrInvalidSegmentType = errors.New("ethdb: Invalid segment type")
)

// Prefix used for each table type.
const (
	HeaderPrefix        = "h"
	BlockHashPrefix     = "H"
	BodyPrefix          = "b"
	BlockReceiptsPrefix = "r"
	LookupPrefix        = "l"
	BloomBitsPrefix     = "B"
)

const (
	// DefaultPartitionSize is the default number of blocks per partition.
	DefaultPartitionSize = 1024

	// The minimum number of mutable segments on a given table.
	MinMutableSegmentCount = 10
)

// DB is the top-level database and contains a mixture of LevelDB & File storage layers.
type DB struct {
	mu      sync.RWMutex
	global  *Table
	body    *Table
	header  *Table
	receipt *Table

	// Filename of the root of the database.
	Path string

	// Number of blocks grouped together.
	PartitionSize uint64
}

// NewDB returns a new instance of DB.
func NewDB(path string) *DB {
	return &DB{
		Path:          path,
		PartitionSize: DefaultPartitionSize,
	}
}

// Open initializes and opens the database.
func (db *DB) Open() error {
	if err := os.MkdirAll(db.Path, 0777); err != nil {
		return err
	}

	db.global = NewTable("global", db.TablePath("global"), &StaticPartitioner{Name: "data"})
	if err := db.global.Open(); err != nil {
		db.Close()
		return err
	}

	db.body = NewTable("body", db.TablePath("body"), NewBlockNumberPartitioner(db.PartitionSize))
	if err := db.body.Open(); err != nil {
		db.Close()
		return err
	}

	db.header = NewTable("header", db.TablePath("header"), NewBlockNumberPartitioner(db.PartitionSize))
	if err := db.header.Open(); err != nil {
		db.Close()
		return err
	}

	db.receipt = NewTable("receipt", db.TablePath("receipt"), NewBlockNumberPartitioner(db.PartitionSize))
	if err := db.receipt.Open(); err != nil {
		db.Close()
		return err
	}

	return nil
}

// Close closes all underlying tables.
func (db *DB) Close() error {
	if db.global != nil {
		db.global.Close()
	}
	if db.body != nil {
		db.body.Close()
	}
	if db.header != nil {
		db.header.Close()
	}
	if db.receipt != nil {
		db.receipt.Close()
	}
	return nil
}

// TablePath returns the filename for the given table.
func (db *DB) TablePath(name string) string {
	return filepath.Join(db.Path, name)
}

// GlobalTable returns the global, statically partitioned table.
func (db *DB) GlobalTable() common.Table { return db.global }

// BodyTable returns the table which holds body data.
func (db *DB) BodyTable() common.Table { return db.body }

// HeaderTable returns the table which holds header data.
func (db *DB) HeaderTable() common.Table { return db.header }

// ReceiptTable returns the table which holds receipt data.
func (db *DB) ReceiptTable() common.Table { return db.receipt }

// Tables returns a sorted list of all tables.
func (db *DB) Tables() []*Table {
	return []*Table{db.global, db.body, db.header, db.receipt}
}

// Table represents key/value storage for a particular data type.
// Contains zero or more segments that are separated by partitioner.
type Table struct {
	mu       sync.RWMutex
	active   string             // active segment name
	segments map[string]Segment // all segments

	Name        string
	Path        string
	Partitioner Partitioner
}

// NewTable returns a new instance of Table.
func NewTable(name, path string, partitioner Partitioner) *Table {
	return &Table{
		segments: make(map[string]Segment),

		Name:        name,
		Path:        path,
		Partitioner: partitioner,
	}
}

// Open initializes the table and all existing segments.
func (t *Table) Open() error {
	if err := os.MkdirAll(t.Path, 0777); err != nil {
		return err
	}

	fis, err := ioutil.ReadDir(t.Path)
	if err != nil {
		return err
	}
	for _, fi := range fis {
		path := filepath.Join(t.Path, fi.Name())
		name := filepath.Base(path)

		// Determine the segment file type.
		typ, err := SegmentFileType(path)
		if err != nil {
			return err
		}

		// Open appropriate segment type.
		switch typ {
		case SegmentETH1:
			fileSegment := NewFileSegment(name, path)
			if err := fileSegment.Open(); err != nil {
				t.Close()
				return err
			}
			t.segments[name] = fileSegment

		case SegmentLDB1:
			ldbSegment := NewLDBSegment(name, path)
			if err := ldbSegment.Open(); err != nil {
				t.Close()
				return err
			}
			t.segments[name] = ldbSegment

		default:
			log.Info("unknown segment type, skipping", "filename", fi.Name())
			continue
		}

		// Set as active if it has the highest lexicographical name.
		if name > t.active {
			t.active = name
		}
	}
	return nil
}

// Close closes all segments within the table.
func (t *Table) Close() error {
	for _, segment := range t.segments {
		if err := segment.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ActiveSegmentName the name of the current active segment.
func (t *Table) ActiveSegmentName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// ActiveSegment returns the active segment.
func (t *Table) ActiveSegment() Segment {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.segments[t.active]
}

// SegmentPath returns the path of the named segment.
func (t *Table) SegmentPath(name string) string {
	return filepath.Join(t.Path, name)
}

// Segment returns a segment by name. Returns nil if segment does not exist.
func (t *Table) Segment(name string) Segment {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.segments[name]
}

// SegmentNames a sorted list of all segments names.
func (t *Table) SegmentNames() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	a := make([]string, 0, len(t.segments))
	for _, s := range t.segments {
		a = append(a, s.Name())
	}
	sort.Strings(a)
	return a
}

// CreateSegmentIfNotExists returns a segment by name.
// Creates a new segment if it does not exist.
func (t *Table) CreateSegmentIfNotExists(name string) (Segment, error) {
	if s := t.Segment(name); s != nil {
		return s, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Recheck under write lock.
	if s := t.segments[name]; s != nil {
		return s, nil
	}

	// Ensure segment name can become active.
	if name < t.active {
		log.Error("cannot non-active create segment", "name", name, "active", t.active)
		return nil, ErrFileSegmentImmutable
	}

	// Create new mutable segment.
	ldbSegment := NewLDBSegment(name, t.SegmentPath(name))
	if err := ldbSegment.Open(); err != nil {
		return nil, err
	}
	t.segments[name] = ldbSegment

	// Set as active segment.
	t.active = name

	// Compact under lock.
	// TODO(benbjohnson): Run compaction in background if too slow.
	if err := t.compact(); err != nil {
		return nil, err
	}

	return ldbSegment, nil
}

// SegmentSlice returns a sorted list of all segments.
func (t *Table) SegmentSlice() []Segment {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.segmentSlice()
}

func (t *Table) segmentSlice() []Segment {
	a := make([]Segment, 0, len(t.segments))
	for _, tbl := range t.segments {
		a = append(a, tbl)
	}
	sort.Slice(a, func(i, j int) bool { return a[i].Name() < a[j].Name() })
	return a
}

// Has returns true if key exists in the table.
func (t *Table) Has(key []byte) (bool, error) {
	s := t.Segment(t.Partitioner.Partition(key))
	if s == nil {
		return false, nil
	}
	return s.Has(key)
}

// Get returns the value associated with key.
func (t *Table) Get(key []byte) ([]byte, error) {
	s := t.Segment(t.Partitioner.Partition(key))
	if s == nil {
		return nil, nil
	}
	return s.Get(key)
}

// Put associates a value with key.
func (t *Table) Put(key, value []byte) error {
	// Ignore if value is the same.
	if v, err := t.Get(key); err != nil && err != ErrKeyNotFound {
		return err
	} else if bytes.Equal(v, value) {
		return nil
	}

	s, err := t.CreateSegmentIfNotExists(t.Partitioner.Partition(key))
	if err != nil {
		return err
	}
	return s.Put(key, value)
}

// Delete removes key from the database.
func (t *Table) Delete(key []byte) error {
	s := t.Segment(t.Partitioner.Partition(key))
	if s == nil {
		return nil
	}
	return s.Delete(key)
}

func (t *Table) NewBatch() common.Batch {
	return &tableBatch{table: t, batches: make(map[string]*ldbSegmentBatch)}
}

// Compact converts LDB segments into immutable file segments.
func (t *Table) Compact() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.compact()
}

func (t *Table) compact() error {
	// Retrieve segments. Exit if too few mutable segments.
	segments := t.segmentSlice()
	if len(segments) < MinMutableSegmentCount {
		return nil
	}

	for _, s := range segments[:len(segments)-MinMutableSegmentCount] {
		s, ok := s.(*LDBSegment)
		if !ok {
			continue
		}

		if err := t.compactSegment(s); err != nil {
			return err
		}
	}
	return nil
}

func (t *Table) compactSegment(s *LDBSegment) error {
	// Compact to temporary file.
	tmpPath := s.Path() + ".tmp"
	if err := s.CompactTo(tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Close & remove the segment.
	if err := s.Close(); err != nil {
		return err
	} else if err := os.RemoveAll(s.Path()); err != nil {
		return err
	}

	// Reopen as file segment.
	fs := NewFileSegment(s.Name(), s.Path())
	if err := os.Rename(tmpPath, s.Path()); err != nil {
		return err
	} else if err := fs.Open(); err != nil {
		return err
	}
	t.segments[s.Name()] = fs

	return nil
}

type tableBatch struct {
	table   *Table
	batches map[string]*ldbSegmentBatch
	size    int
}

func (b *tableBatch) Put(key, value []byte) error {
	// Ignore if value is the same.
	if v, err := b.table.Get(key); err != nil && err != ErrKeyNotFound {
		return err
	} else if bytes.Equal(v, value) {
		return nil
	}

	name := b.table.Partitioner.Partition(key)
	segment, err := b.table.CreateSegmentIfNotExists(name)
	if err != nil {
		log.Error("tableBatch.Put: error", "table", b.table.Name, "segment", name, "key", fmt.Sprintf("%x", key))
		return err
	}

	ldbSegment, ok := segment.(*LDBSegment)
	if !ok {
		log.Error("cannot insert into compacted segment", "name", name, "key", fmt.Sprintf("%x", key))
		panic("dbg/put.immutable")
		return ErrFileSegmentImmutable
	}

	sb := b.batches[name]
	if sb == nil {
		sb = ldbSegment.newBatch()
		b.batches[name] = sb
	}
	if err := sb.Put(key, value); err != nil {
		return err
	}
	b.size += len(value)
	return nil
}

func (b *tableBatch) Write() error {
	for _, sb := range b.batches {
		if err := sb.Write(); err != nil {
			return err
		}
	}
	return nil
}

func (b *tableBatch) ValueSize() int {
	return b.size
}

func (b *tableBatch) Reset() {
	for _, sb := range b.batches {
		sb.Reset()
	}
	b.size = 0
}

// Partitioner represents an object that returns a partition name for a given key.
type Partitioner interface {
	Partition(key []byte) string
}

// PartitionFunc implements Partitioner.
type PartitionFunc func(key []byte) string

func (fn PartitionFunc) Partition(key []byte) string { return fn(key) }

// StaticPartitioner represents a partitioner that always returns the same partition name.
type StaticPartitioner struct {
	Name string
}

// Partition always returns the same partition name.
func (p *StaticPartitioner) Partition(key []byte) string {
	return p.Name
}

// BlockNumberPartitioner represents a partitioner that returns a partition based on block number.
type BlockNumberPartitioner struct {
	Size uint64
}

// NewBlockNumberPartitioner returns a new instance of BlockNumberPartitioner.
func NewBlockNumberPartitioner(size uint64) *BlockNumberPartitioner {
	return &BlockNumberPartitioner{Size: size}
}

// Partition always returns a partition name based on a grouping of block numbers.
func (p *BlockNumberPartitioner) Partition(key []byte) string {
	blockNumber, _ := KeyBlockNumber(key)
	if p.Size > 0 {
		blockNumber -= (blockNumber % p.Size)
	}
	return fmt.Sprintf("%016x", blockNumber)
}

// Segment represents a subset of Table data.
type Segment interface {
	io.Closer

	Name() string
	Path() string

	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
}

// Segment file types.
const (
	SegmentETH1 = "eth1"
	SegmentLDB1 = "ldb1"
)

// SegmentFileType returns the file type at path.
func SegmentFileType(path string) (string, error) {
	// Check if this is a directory. If so then treat as LevelDB database.
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	} else if fi.IsDir() {
		return SegmentLDB1, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read file magic.
	magic := make([]byte, len(FileSegmentMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		return "", err
	}

	// Match file magic with known types.
	switch string(magic) {
	case FileSegmentMagic:
		return SegmentETH1, nil
	default:
		return "", ErrInvalidSegmentType
	}
}
