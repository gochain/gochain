package ethdb

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

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

	// DefaultMinMutableSegmentCount is the minimum number of mutable segments on a given table.
	DefaultMinMutableSegmentCount = 40

	// DefaultMinCompactionAge is the minimum age after creation before an LDB
	// segment can be compacted into a file segment.
	DefaultMinCompactionAge = 1 * time.Minute
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

	// Maximum number of segments that can be opened at once.
	MaxOpenSegmentCount int

	// Age before LDB segment can be compacted to a file segment.
	MinCompactionAge time.Duration

	SegmentOpener    SegmentOpener
	SegmentCompactor SegmentCompactor
}

// NewDB returns a new instance of DB.
func NewDB(path string) *DB {
	return &DB{
		Path:                path,
		PartitionSize:       DefaultPartitionSize,
		MaxOpenSegmentCount: DefaultMaxOpenSegmentCount,
		MinCompactionAge:    DefaultMinCompactionAge,
		SegmentOpener:       NewFileSegmentOpener(),
		SegmentCompactor:    NewFileSegmentCompactor(),
	}
}

// Open initializes and opens the database.
func (db *DB) Open() error {
	if err := os.MkdirAll(db.Path, 0777); err != nil {
		return err
	}

	db.global = NewTable("global", db.TablePath("global"), &StaticPartitioner{Name: "data"})
	db.body = NewTable("body", db.TablePath("body"), NewBlockNumberPartitioner(db.PartitionSize))
	db.header = NewTable("header", db.TablePath("header"), NewBlockNumberPartitioner(db.PartitionSize))
	db.receipt = NewTable("receipt", db.TablePath("receipt"), NewBlockNumberPartitioner(db.PartitionSize))

	for _, tbl := range db.Tables() {
		tbl.MaxOpenSegmentCount = db.MaxOpenSegmentCount
		tbl.MinCompactionAge = db.MinCompactionAge
		tbl.SegmentOpener = db.SegmentOpener
		tbl.SegmentCompactor = db.SegmentCompactor
		if err := tbl.Open(); err != nil {
			log.Error("Cannot open table", "name", tbl.Name, "err", err)
			db.Close()
			return err
		}
	}

	return nil
}

// Close closes all underlying tables.
func (db *DB) Close() error {
	for _, tbl := range db.Tables() {
		if tbl != nil {
			tbl.Close()
		}
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
