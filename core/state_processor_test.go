package core

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/gochain/gochain/v4/common"
	"github.com/gochain/gochain/v4/core/state"
	"github.com/gochain/gochain/v4/core/types"
	"github.com/gochain/gochain/v4/core/vm"
	"github.com/gochain/gochain/v4/crypto"
	"github.com/gochain/gochain/v4/params"
)

func BenchmarkStateProcessor_Process(b *testing.B) {
	for _, cnt := range []int{1, 10, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("% 5d", cnt), benchmarkStateProcessor_Process(cnt))
	}
}

func benchmarkStateProcessor_Process(cnt int) func(b *testing.B) {
	key, _ := crypto.GenerateKey()
	address := crypto.PubkeyToAddress(key.PublicKey)
	funds := big.NewInt(1000000000)

	genesis := &Genesis{
		Config:     params.TestChainConfig,
		Difficulty: big.NewInt(1),
		Alloc:      GenesisAlloc{address: {Balance: funds}},
	}
	txs := make([]*types.Transaction, cnt)
	signer := types.NewEIP155Signer(genesis.Config.ChainId)
	for i := range txs {
		tx := types.NewTransaction(uint64(i), common.Address{}, big.NewInt(100), 100000, big.NewInt(1), nil)
		tx, _ = types.SignTx(tx, signer, key)
		txs[i] = tx
	}
	bc, err := newTestBlockChainWithGenesis(false, true, genesis)
	block := types.NewBlock(&types.Header{
		GasLimit: bc.GasLimit(),
	}, txs, nil, nil)

	return func(b *testing.B) {
		// Irregular handling to keep setup separate, but only fail the sub-benchmark.
		if err != nil {
			b.Fatal(err)
		}
		defer bc.Stop()
		var cfg vm.Config
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			statedb, err := state.New(bc.CurrentBlock().Root(), bc.stateCache)
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()

			_, _, _, err = bc.Processor().Process(block, statedb, cfg)
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func TestStateProcessor(t *testing.T) {
	numTxs := 10000

	start := time.Now()
	key, _ := crypto.GenerateKey()
	address := crypto.PubkeyToAddress(key.PublicKey)
	funds := big.NewInt(1000000000)

	genesis := &Genesis{
		Config:     params.TestChainConfig,
		Difficulty: big.NewInt(1),
		Alloc:      GenesisAlloc{address: {Balance: funds}},
	}
	signer := types.NewEIP155Signer(genesis.Config.ChainId)

	bc, err := newTestBlockChainWithGenesis(false, true, genesis)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("newTestBlockchain duration: %s", time.Since(start))
	defer bc.Stop()
	cfg := vm.Config{}
	// statedb, err := state.New(bc.CurrentBlock().Root(), bc.stateCache)
	// 	if err != nil {
	// 		t.Fatal(err)
	// 	}

	txs := make([]*types.Transaction, numTxs)
	for i := 0; i < numTxs; i++ {
		tx := types.NewTransaction(uint64(i), common.Address{}, big.NewInt(100), 100000, big.NewInt(1), nil)
		tx, _ = types.SignTx(tx, signer, key)
		// txs = append(txs, types.NewTransaction(uint64(i), common.Address{}, big.NewInt(1), uint64(21000), big.NewInt(21000), nil))
		txs[i] = tx
	}
	block := types.NewBlock(&types.Header{
		GasLimit: bc.GasLimit(),
	}, txs, nil, nil)
	statedb, err := state.New(bc.CurrentBlock().Root(), bc.stateCache)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	_, _, _, err = bc.Processor().Process(block, statedb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("process() duration: %s", time.Since(start))
}
