package ethdb_test

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/ethdb"
)

func TestTable_Put(t *testing.T) {
	dir := MustTempDir()
	tbl := ethdb.NewTable("test", dir, &ethdb.StaticPartitioner{Name:"data"})
	defer os.RemoveAll(tbl.Path)

	if err := tbl.Open(); err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if err := tbl.Put(numHashKey('b', 1000, common.Hash{}), []byte("BLOCKDATA")); err != nil {
		t.Fatal(err)
	}

	if exists, err := tbl.Has(numHashKey('b', 1000, common.Hash{})); err != nil {
		t.Fatal(err)
	} else if !exists {
		t.Fatal("expected value to exist")
	}

	if value, err := tbl.Get(numHashKey('b', 1000, common.Hash{})); err != nil {
		t.Fatal(err)
	} else if string(value) != "BLOCKDATA" {
		t.Fatalf("unexpected value: %q", value)
	}

	// Close original database.
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTable_Delete(t *testing.T) {
	dir := MustTempDir()
	tbl := ethdb.NewTable("test", dir, &ethdb.StaticPartitioner{Name:"data"})
	defer os.RemoveAll(tbl.Path)

	if err := tbl.Open(); err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if err := tbl.Put(numHashKey('b', 1000, common.Hash{}), []byte("BLOCKDATA")); err != nil {
		t.Fatal(err)
	} else if err := tbl.Delete(numHashKey('b', 1000, common.Hash{})); err != nil {
		t.Fatal(err)
	}

	if exists, err := tbl.Has(numHashKey('b', 1000, common.Hash{})); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("expected value to not exist")
	}

	if _, err := tbl.Get(numHashKey('b', 1000, common.Hash{})); err != ethdb.ErrKeyNotFound {
		t.Fatal(err)
	}
}

func TestTable_Compact(t *testing.T) {
	dir := MustTempDir()
	defer os.RemoveAll(dir)

	tbl := ethdb.NewTable("test", dir, ethdb.NewBlockNumberPartitioner(1000))
	if err := tbl.Open(); err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if err := tbl.Put(numHashKey('b', 200, common.Hash{}), []byte("foo")); err != nil {
		t.Fatal(err)
	} else if err := tbl.Put(numHashKey('b', 700, common.Hash{}), []byte("bar")); err != nil {
		t.Fatal(err)
	} else if err := tbl.Put(numHashKey('b', 1500, common.Hash{}), []byte("baz")); err != nil {
		t.Fatal(err)
	} else if err := tbl.Put(numHashKey('b', 2100, common.Hash{}), []byte("bat")); err != nil {
		t.Fatal(err)
	}

	// Force compaction.
	if err := tbl.Compact(); err != nil {
		t.Fatal(err)
	}

	// Verify segment file names.
	segments := tbl.SegmentSlice()
	if len(segments) != 3 {
		t.Fatalf("unexpected segment count: %d", len(segments))
	} else if _, ok := segments[0].(*ethdb.FileSegment); !ok {
		t.Fatalf("expected file segment(0), got %T", segments[0])
	} else if _, ok := segments[1].(*ethdb.LDBSegment); !ok {
		t.Fatalf("expected ldb segment(1), got %T", segments[1])
	} else if _, ok := segments[2].(*ethdb.LDBSegment); !ok {
		t.Fatalf("expected ldb segment(1), got %T", segments[1])
	}

	// Verify active segment.
	if name := tbl.ActiveSegmentName(); name != `00000000000007d0` {
		t.Fatalf("unexpected active segment name: %s", name)
	}

	// Verify data can be read from compacted segment.
	if v, err := tbl.Get(numHashKey('b', 200, common.Hash{})); err != nil {
		t.Fatal(err)
	} else if string(v) != `foo` {
		t.Fatalf("unexpected value: %q", v)
	}

	if v, err := tbl.Get(numHashKey('b', 700, common.Hash{})); err != nil {
		t.Fatal(err)
	} else if string(v) != `bar` {
		t.Fatalf("unexpected value: %q", v)
	}
}

func TestStaticPartitioner_Partition(t *testing.T) {
	p := ethdb.StaticPartitioner{Name: "TEST"}
	if v := p.Partition([]byte("foo")); v != "TEST" {
		t.Fatalf("unexpected partition: %v", v)
	} else if v := p.Partition([]byte("bar")); v != "TEST" {
		t.Fatalf("unexpected partition: %v", v)
	}
}

func TestBlockNumberPartitioner_Partition(t *testing.T) {
	p := ethdb.NewBlockNumberPartitioner(100)
	if v := p.Partition(numHashKey('t', 1234, common.Hash{})); v != `00000000000004b0` {
		t.Fatalf("unexpected partition: %v", v)
	}
}

func numHashKey(prefix byte, number uint64, hash common.Hash) []byte {
	var k [41]byte
	k[0] = prefix
	binary.BigEndian.PutUint64(k[1:], number)
	copy(k[9:], hash[:])
	return k[:]
}
