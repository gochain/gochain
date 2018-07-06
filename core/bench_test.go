// Copyright 2015 The go-ethereum Authors
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

package core

import (
	"context"
	"crypto/ecdsa"
	"io/ioutil"
	"math/big"
	"os"
	"testing"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/common/hexutil"
	"github.com/gochain-io/gochain/common/math"
	"github.com/gochain-io/gochain/consensus/clique"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/crypto"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/params"
)

func BenchmarkInsertChain_empty_memdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, false, nil)
}
func BenchmarkInsertChain_empty_diskdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, true, nil)
}
func BenchmarkInsertChain_valueTx_memdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, false, genValueTx(ctx, 0))
}
func BenchmarkInsertChain_valueTx_diskdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, true, genValueTx(ctx, 0))
}
func BenchmarkInsertChain_valueTx_100kB_memdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, false, genValueTx(ctx, 100*1024))
}
func BenchmarkInsertChain_valueTx_100kB_diskdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, true, genValueTx(ctx, 100*1024))
}
func BenchmarkInsertChain_ring200_memdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, false, genTxRing(ctx, 200))
}
func BenchmarkInsertChain_ring200_diskdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, true, genTxRing(ctx, 200))
}
func BenchmarkInsertChain_ring1000_memdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, false, genTxRing(ctx, 1000))
}
func BenchmarkInsertChain_ring1000_diskdb(b *testing.B) {
	ctx := context.Background()
	benchInsertChain(ctx, b, true, genTxRing(ctx, 1000))
}

var (
	// This is the content of the genesis block used by the benchmarks.
	benchRootKey, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	benchRootAddr   = crypto.PubkeyToAddress(benchRootKey.PublicKey)
	benchRootFunds  = math.BigPow(2, 100)
)

// genValueTx returns a block generator that includes a single
// value-transfer transaction with n bytes of extra data in each
// block.
func genValueTx(ctx context.Context, nbytes int) func(context.Context, int, *BlockGen) {
	return func(ctx2 context.Context, i int, gen *BlockGen) {
		toaddr := common.Address{}
		data := make([]byte, nbytes)
		gas, _ := IntrinsicGas(data, false, false)
		tx, _ := types.SignTx(types.NewTransaction(gen.TxNonce(benchRootAddr), toaddr, big.NewInt(1), gas, nil, data), types.HomesteadSigner{}, benchRootKey)
		gen.AddTx(ctx2, tx)
	}
}

var (
	ringKeys  = make([]*ecdsa.PrivateKey, 1000)
	ringAddrs = make([]common.Address, len(ringKeys))
)

func init() {
	ringKeys[0] = benchRootKey
	ringAddrs[0] = benchRootAddr
	for i := 1; i < len(ringKeys); i++ {
		ringKeys[i], _ = crypto.GenerateKey()
		ringAddrs[i] = crypto.PubkeyToAddress(ringKeys[i].PublicKey)
	}
}

// genTxRing returns a block generator that sends ether in a ring
// among n accounts. This is creates n entries in the state database
// and fills the blocks with many small transactions.
func genTxRing(ctx context.Context, naccounts int) func(context.Context, int, *BlockGen) {
	from := 0
	return func(ctx2 context.Context, i int, gen *BlockGen) {
		gas := CalcGasLimit(gen.PrevBlock(i - 1))
		for {
			gas -= params.TxGas
			if gas < params.TxGas {
				break
			}
			to := (from + 1) % naccounts
			tx := types.NewTransaction(
				gen.TxNonce(ringAddrs[from]),
				ringAddrs[to],
				benchRootFunds,
				params.TxGas,
				nil,
				nil,
			)
			tx, _ = types.SignTx(tx, types.HomesteadSigner{}, ringKeys[from])
			gen.AddTx(ctx2, tx)
			from = to
		}
	}
}

func benchInsertChain(ctx context.Context, b *testing.B, disk bool, gen func(context.Context, int, *BlockGen)) {
	// Create the database in memory or in a temporary directory.
	var db common.Database
	if !disk {
		db = ethdb.NewMemDatabase()
	} else {
		dir, err := ioutil.TempDir("", "eth-core-bench")
		if err != nil {
			b.Fatalf("cannot create temporary directory: %v", err)
		}
		defer os.RemoveAll(dir)

		diskDB := ethdb.NewDB(dir)
		if err := diskDB.Open(); err != nil {
			b.Fatalf("cannot create temporary database: %v", err)
		}
		defer diskDB.Close()
		db = diskDB
	}

	// Generate a chain of b.N blocks using the supplied block
	// generator function.
	gspec := Genesis{
		Config: params.TestChainConfig,
		Alloc:  GenesisAlloc{benchRootAddr: {Balance: benchRootFunds}},
		Signer: hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
	}
	genesis := gspec.MustCommit(db)
	engine := clique.NewFaker()
	chain, _ := GenerateChain(ctx, gspec.Config, genesis, engine, db, b.N, gen)

	// Time the insertion of the new chain.
	// State and blocks are stored in the same DB.
	chainman, _ := NewBlockChain(ctx, db, nil, gspec.Config, engine, vm.Config{})
	defer chainman.Stop()
	b.ReportAllocs()
	b.ResetTimer()
	if i, err := chainman.InsertChain(ctx, chain); err != nil {
		b.Fatalf("insert error (block %d): %v\n", i, err)
	}
}

func BenchmarkChainRead_header_10k(b *testing.B) {
	benchReadChain(b, false, 10000)
}
func BenchmarkChainRead_full_10k(b *testing.B) {
	benchReadChain(b, true, 10000)
}
func BenchmarkChainRead_header_100k(b *testing.B) {
	benchReadChain(b, false, 100000)
}
func BenchmarkChainRead_full_100k(b *testing.B) {
	benchReadChain(b, true, 100000)
}
func BenchmarkChainRead_header_500k(b *testing.B) {
	benchReadChain(b, false, 500000)
}
func BenchmarkChainRead_full_500k(b *testing.B) {
	benchReadChain(b, true, 500000)
}
func BenchmarkChainWrite_header_10k(b *testing.B) {
	benchWriteChain(b, false, 10000)
}
func BenchmarkChainWrite_full_10k(b *testing.B) {
	benchWriteChain(b, true, 10000)
}
func BenchmarkChainWrite_header_100k(b *testing.B) {
	benchWriteChain(b, false, 100000)
}
func BenchmarkChainWrite_full_100k(b *testing.B) {
	benchWriteChain(b, true, 100000)
}
func BenchmarkChainWrite_header_500k(b *testing.B) {
	benchWriteChain(b, false, 500000)
}
func BenchmarkChainWrite_full_500k(b *testing.B) {
	benchWriteChain(b, true, 500000)
}

// makeChainForBench writes a given number of headers or empty blocks/receipts
// into a database.
func makeChainForBench(db common.Database, full bool, count uint64) {
	var hash common.Hash
	for n := uint64(0); n < count; n++ {
		header := &types.Header{
			Coinbase:    common.Address{},
			Number:      big.NewInt(int64(n)),
			ParentHash:  hash,
			Difficulty:  big.NewInt(1),
			UncleHash:   types.EmptyUncleHash,
			TxHash:      types.EmptyRootHash,
			ReceiptHash: types.EmptyRootHash,
		}
		hash = header.Hash()
		WriteHeader(db.GlobalTable(), db.HeaderTable(), header)
		WriteCanonicalHash(db, hash, n)
		WriteTd(db.GlobalTable(), hash, n, big.NewInt(int64(n+1)))
		if full || n == 0 {
			block := types.NewBlockWithHeader(header)
			WriteBody(db.BodyTable(), hash, n, block.Body())
			WriteBlockReceipts(db.ReceiptTable(), hash, n, nil)
		}
	}
}

func benchWriteChain(b *testing.B, full bool, count uint64) {
	for i := 0; i < b.N; i++ {
		dir, err := ioutil.TempDir("", "eth-chain-bench")
		if err != nil {
			b.Fatalf("cannot create temporary directory: %v", err)
		}
		db := ethdb.NewDB(dir)
		if err := db.Open(); err != nil {
			b.Fatalf("error opening database at %v: %v", dir, err)
		}
		makeChainForBench(db, full, count)
		db.Close()
		os.RemoveAll(dir)
	}
}

func benchReadChain(b *testing.B, full bool, count uint64) {
	dir, err := ioutil.TempDir("", "eth-chain-bench")
	if err != nil {
		b.Fatalf("cannot create temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)

	db := ethdb.NewDB(dir)
	if err := db.Open(); err != nil {
		b.Fatalf("error opening database at %v: %v", dir, err)
	}
	makeChainForBench(db, full, count)
	db.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		db := ethdb.NewDB(dir)
		if err := db.Open(); err != nil {
			b.Fatalf("error opening database at %v: %v", dir, err)
		}
		chain, err := NewBlockChain(context.Background(), db, nil, params.TestChainConfig, clique.NewFaker(), vm.Config{})
		if err != nil {
			b.Fatalf("error creating chain: %v", err)
		}

		for n := uint64(0); n < count; n++ {
			header := chain.GetHeaderByNumber(n)
			if full {
				hash := header.Hash()
				GetBody(db.BodyTable(), hash, n)
				GetBlockReceipts(db.ReceiptTable(), hash, n)
			}
		}

		chain.Stop()
		db.Close()
	}
}
