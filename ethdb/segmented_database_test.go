package ethdb_test

import (
	"bytes"
	"encoding/binary"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/syndtr/goleveldb/leveldb"
)

func TestSegmentedDatabase_Put(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)
		key := numHashKey('h', 18372, common.Hash{})

		// Insert new key/value.
		if err := db.Put(key, []byte("xyz")); err != nil {
			t.Fatal(err)
		}

		// Verify it exists.
		if ok, err := db.Has(key); err != nil {
			t.Fatal(err)
		} else if !ok {
			t.Fatal("expected key exists")
		}

		// Verify its value can be retrieved.
		if value, err := db.Get(key); err != nil {
			t.Fatal(err)
		} else if !bytes.Equal(value, []byte("xyz")) {
			t.Fatalf("unexpected value: %x", value)
		}

		// Verify correct segment is created.
		if _, err := os.Stat(filepath.Join(db.Path, "00000000000047c4")); os.IsNotExist(err) {
			t.Fatal("expected segment directory to exist")
		} else if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Global", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)
		key := []byte("foo")

		// Insert new key/value.
		if err := db.Put(key, []byte("bar")); err != nil {
			t.Fatal(err)
		}

		// Verify it exists.
		if ok, err := db.Has(key); err != nil {
			t.Fatal(err)
		} else if !ok {
			t.Fatal("expected key exists")
		}

		// Verify its value can be retrieved.
		if value, err := db.Get(key); err != nil {
			t.Fatal(err)
		} else if !bytes.Equal(value, []byte("bar")) {
			t.Fatalf("unexpected value: %x", value)
		}
	})
}

func TestSegmentedDatabase_Has(t *testing.T) {
	t.Run("NotFound/NoSegment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)
		if ok, err := db.Has(numHashKey('h', 18372, common.Hash{})); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("expected not found")
		}
	})

	t.Run("NotFound/WithSegment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if err := db.Put(numHashKey('t', 18372, common.Hash{}), []byte("xyz")); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.Has(numHashKey('h', 18372, common.Hash{})); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("expected not found")
		}
	})

	t.Run("NotFound/Global", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if ok, err := db.Has([]byte("foo")); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("expected not found")
		}
	})
}

func TestSegmentedDatabase_Get(t *testing.T) {
	t.Run("NotFound/NoSegment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)
		if _, err := db.Get(numHashKey('h', 18372, common.Hash{})); err != leveldb.ErrNotFound {
			t.Fatal(err)
		}
	})

	t.Run("NotFound/WithSegment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if err := db.Put(numHashKey('t', 18372, common.Hash{}), []byte("xyz")); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Get(numHashKey('h', 18372, common.Hash{})); err != leveldb.ErrNotFound {
			t.Fatal(err)
		}
	})

	t.Run("NotFound/Global", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if _, err := db.Get([]byte("foo")); err != leveldb.ErrNotFound {
			t.Fatal(err)
		}
	})
}

func TestSegmentedDatabase_Delete(t *testing.T) {
	t.Run("OK/Segment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if err := db.Put(numHashKey('h', 18372, common.Hash{}), []byte("xyz")); err != nil {
			t.Fatal(err)
		}
		if err := db.Delete(numHashKey('h', 18372, common.Hash{})); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.Has(numHashKey('h', 18372, common.Hash{})); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("expected key to be deleted")
		}
	})

	t.Run("OK/Global", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if err := db.Put([]byte("foo"), []byte("xyz")); err != nil {
			t.Fatal(err)
		}
		if err := db.Delete([]byte("foo")); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.Has([]byte("foo")); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("expected key to be deleted")
		}
	})

	t.Run("NotFound/NoSegment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)
		if err := db.Delete(numHashKey('h', 18372, common.Hash{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("NotFound/WithSegment", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if err := db.Put(numHashKey('t', 18372, common.Hash{}), []byte("xyz")); err != nil {
			t.Fatal(err)
		}
		if err := db.Delete(numHashKey('h', 18372, common.Hash{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("NotFound/Global", func(t *testing.T) {
		db := MustNewSegmentedDatabase()
		defer MustCloseSegmentedDatabase(db)

		if err := db.Delete([]byte("foo")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestKeyBlockNumber(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		if num, ok := ethdb.KeyBlockNumber(numHashKey('t', 12346789, common.Hash{})); !ok {
			t.Fatal("expected ok")
		} else if num != 12346789 {
			t.Fatalf("unexpected block number: %d", num)
		}
	})

	t.Run("Other", func(t *testing.T) {
		if num, ok := ethdb.KeyBlockNumber(numHashKey('X', 12346789, common.Hash{})); ok {
			t.Fatal("expected not ok")
		} else if num != 0 {
			t.Fatalf("unexpected block number: %d", num)
		}
	})

	t.Run("TooShort", func(t *testing.T) {
		if num, ok := ethdb.KeyBlockNumber([]byte("t129837")); ok {
			t.Fatal("expected not ok")
		} else if num != 0 {
			t.Fatalf("unexpected block number: %d", num)
		}
	})
}

func TestParseSegmentFilename(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		if num, err := ethdb.ParseSegmentFilename(ethdb.FormatSegmentFilename(912837)); err != nil {
			t.Fatal(err)
		} else if num != 912837 {
			t.Fatalf("unexpected block number: %d", num)
		}
	})

	t.Run("ErrInvalidSegmentFilename", func(t *testing.T) {
		if _, err := ethdb.ParseSegmentFilename("BAD_FILENAME"); err != ethdb.ErrInvalidSegmentFilename {
			t.Fatalf("unexpected error: %s", err)
		}
	})
}

// MustNewSegmentedDatabase returns a new SegmentedDatabase at a temporary path.
func MustNewSegmentedDatabase() *ethdb.SegmentedDatabase {
	path, err := ioutil.TempDir("", "")
	if err != nil {
		panic(err)
	}

	db, err := ethdb.NewSegmentedDatabase(path)
	if err != nil {
		panic(err)
	}
	db.NewDatabase = func(opt ethdb.NewDatabaseOptions) (ethdb.Database, error) {
		return ethdb.NewLDBDatabase(opt.Path, 0, 0)
	}
	return db
}

// MustCloseSegmentedDatabase closes and removes the underlying data for db.
func MustCloseSegmentedDatabase(db *ethdb.SegmentedDatabase) {
	db.Close()
	if err := os.RemoveAll(db.Path); err != nil {
		panic(err)
	}
}

func numHashKey(prefix byte, number uint64, hash common.Hash) []byte {
	var k [41]byte
	k[0] = prefix
	binary.BigEndian.PutUint64(k[1:], number)
	copy(k[9:], hash[:])
	return k[:]
}
