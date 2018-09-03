package ethdb

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

// Segment file types.
const (
	SegmentETH1 = "eth1"
	SegmentLDB1 = "ldb1"
)

// Segment represents a subset of Table data.
type Segment interface {
	io.Closer

	Name() string
	Path() string

	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
	Iterator() SegmentIterator
}

// SortSegments sorts a by name.
func SortSegments(a []Segment) {
	sort.Slice(a, func(i, j int) bool { return a[i].Name() < a[j].Name() })
}

// MutableSegment represents a segment that can be altered.
// These segments are eventually compacted into immutable segments.
type MutableSegment interface {
	Segment
	Put(key, value []byte) error
	Delete(key []byte) error
}

// SegmentIterator represents a sequentially iterator over all the key/value
// pairs inside a segment.
type SegmentIterator interface {
	io.Closer
	Next() bool
	Key() []byte
	Value() []byte
}

// SegmentOpener represents an object that can instantiate and load an immutable segment.
type SegmentOpener interface {
	OpenSegment(table, name, path string) (Segment, error)
	ListSegmentNames(path, table string) ([]string, error)
}

// SegmentCompactor represents an object that can compact from an LDB segment
// to an immutable segment and back.
type SegmentCompactor interface {
	CompactSegment(ctx context.Context, table string, s *LDBSegment) (Segment, error)
	UncompactSegment(ctx context.Context, table string, s Segment) (*LDBSegment, error)
}

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
	if _, err := io.ReadFull(f, magic); err == io.EOF {
		return "", ErrInvalidSegmentType
	} else if err != nil {
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
