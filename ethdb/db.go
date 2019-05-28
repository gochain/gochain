package ethdb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/syndtr/goleveldb/leveldb"
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
	// Migrate database first, if necessary.
	if err := db.migrate(); err != nil {
		return err
	}

	// Ensure directory exists.
	if err := os.MkdirAll(db.Path, 0777); err != nil {
		return err
	}

	db.global = NewTable("global", db.TablePath("global"), &StaticPartitioner{Name: "data"})
	db.body = NewTable("body", db.TablePath("body"), NewBlockNumberPartitioner(db.PartitionSize))
	db.header = NewTable("header", db.TablePath("header"), NewBlockNumberPartitioner(db.PartitionSize))
	db.receipt = NewTable("receipt", db.TablePath("receipt"), NewBlockNumberPartitioner(db.PartitionSize))

	for _, tbl := range db.Tables() {
		// Allow 100x header files since they are small.
		tbl.MaxOpenSegmentCount = db.MaxOpenSegmentCount
		if tbl.Name == "header" {
			tbl.MaxOpenSegmentCount *= 100
		}

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

// Table returns a table by name.
func (db *DB) Table(name string) *Table {
	switch name {
	case "global":
		return db.global
	case "body":
		return db.body
	case "header":
		return db.header
	case "receipt":
		return db.receipt
	default:
		return nil
	}
}

func (db *DB) Has(key []byte) (bool, error) {
	panic("implement me")
}

func (db *DB) Get(key []byte) ([]byte, error) {
	panic("implement me")
}

func (db *DB) Put(key []byte, value []byte) error {
	panic("implement me")
}

func (db *DB) Delete(key []byte) error {
	panic("implement me")
}

func (db *DB) NewBatch() common.Batch {
	panic("implement me")
}

func (db *DB) NewIterator() common.Iterator {
	panic("implement me")
}

func (db *DB) NewIteratorWithStart(start []byte) common.Iterator {
	panic("implement me")
}

func (db *DB) NewIteratorWithPrefix(prefix []byte) common.Iterator {
	panic("implement me")
}

func (db *DB) Stat(property string) (string, error) {
	panic("implement me")
}

func (db *DB) Compact(start, limit []byte) error {
	panic("implement me")
}

// migrate converts a source LevelDB database to the new ethdb formatted database.
func (db *DB) migrate() error {
	const suffix = ".migrating"

	if strings.HasSuffix(db.Path, suffix) {
		return nil
	}

	// Ignore if there is no directory.
	if _, err := os.Stat(db.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("ethdb.DB.migrate: %s", err)
	}

	// Ignore if path has already been migrated.
	if ok, err := IsDBDir(db.Path); err != nil {
		return fmt.Errorf("ethdb.DB.migrate: cannot check ethdb directory: %s", err)
	} else if ok {
		return nil // already migrated
	}

	// Skip if not an LevelDB directory. Probably empty.
	if ok, err := IsLevelDBDir(db.Path); err != nil {
		return fmt.Errorf("ethdb.DB.migrate: cannot check leveldb directory: %s", err)
	} else if !ok {
		return nil
	}

	log.Info("begin ethdb migration", "path", db.Path)

	// Remove partial migration, if exists.
	tmpPath := db.Path + suffix
	if err := os.RemoveAll(tmpPath); err != nil {
		return fmt.Errorf("ethdb.DB.migrate: cannot remove partial migration: %s", err)
	}

	// Open source database.
	src, err := leveldb.OpenFile(db.Path, nil)
	if err != nil {
		return err
	}
	defer src.Close()

	// Clone to temporary destination database.
	dst := *db
	dst.Path = tmpPath
	dst.MinCompactionAge = 0
	if err := dst.Open(); err != nil {
		return fmt.Errorf("cannot open dst database: %s", err)
	}
	defer dst.Close()

	// Iterate over every key in the source database.
	itr := src.NewIterator(nil, nil)
	defer itr.Release()

	// Write all key/values to new database.
	for itr.Next() {
		var tbl *Table

		switch {
		case isBodyKey(itr.Key()):
			tbl = dst.BodyTable().(*Table)
		case isHeaderKey(itr.Key()):
			tbl = dst.HeaderTable().(*Table)
		case isReceiptKey(itr.Key()):
			tbl = dst.ReceiptTable().(*Table)
		default:
			tbl = dst.GlobalTable().(*Table)
		}

		if err := tbl.Put(itr.Key(), itr.Value()); err != nil {
			return fmt.Errorf("cannot insert item: tbl=%s key=%x err=%q", tbl.Name, itr.Key(), err)
		}
	}
	if err := itr.Error(); err != nil {
		return err
	}

	// Close both databases.
	if err := src.Close(); err != nil {
		return err
	} else if err := dst.Close(); err != nil {
		return err
	}

	// Rename databases.
	if err := os.Rename(db.Path, db.Path+".old"); err != nil {
		return err
	} else if err := os.Rename(tmpPath, db.Path); err != nil {
		return err
	}

	log.Info("ethdb migration complete", "path", db.Path)

	return nil
}

func isBodyKey(key []byte) bool {
	return bytes.HasPrefix(key, []byte("b")) && len(key) == 41
}

func isHeaderKey(key []byte) bool {
	return bytes.HasPrefix(key, []byte("h")) && (len(key) == 41 || (len(key) == 10 && key[9] == 'n'))
}

func isReceiptKey(key []byte) bool {
	return bytes.HasPrefix(key, []byte("r")) && len(key) == 41
}

// IsDBDir returns true if path contains an ethdb.DB.
// Checks if the "global" table exists.
func IsDBDir(path string) (bool, error) {
	fi, err := os.Stat(filepath.Join(path, "global"))
	if os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

// IsLevelDBDir returns true if path contains a goleveldb database.
// Verifies that path contains a CURRENT file that starts with MANIFEST.
func IsLevelDBDir(path string) (bool, error) {
	f, err := os.Open(filepath.Join(path, "CURRENT"))
	if os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 8)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false, fmt.Errorf("ethdb.DB.IsLevelDBDir: cannot read CURRENT: %s", err)
	}
	return string(buf) == "MANIFEST", nil
}
