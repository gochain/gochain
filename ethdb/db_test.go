package ethdb_test

import (
	"testing"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/ethdb"
)

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
