package core

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/gochain-io/gochain/common/perfutils"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/crypto"
	"github.com/gochain-io/gochain/params"
)

func BenchmarkStateProcessor_Process(b *testing.B) {
	ctx := context.Background()
	key, _ := crypto.GenerateKey()
	address := crypto.PubkeyToAddress(key.PublicKey)
	funds := big.NewInt(1000000000)

	genesis := &Genesis{
		Config:     params.TestChainConfig,
		Difficulty: big.NewInt(1),
		Alloc:      GenesisAlloc{address: {Balance: funds}},
	}
	signer := types.NewEIP155Signer(genesis.Config.ChainId)

	bc := newTestBlockChainWithGenesis(ctx, false, true, genesis)
	defer bc.Stop()
	cfg := vm.Config{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		txs := make([]*types.Transaction, 1000)
		for i := 0; i < 1000; i++ {
			tx := types.NewTransaction(uint64(i), common.Address{}, big.NewInt(100), 100000, big.NewInt(1), nil)
			tx, _ = types.SignTx(tx, signer, key)
			txs[i] = tx
		}

		block := types.NewBlock(&types.Header{
			GasLimit: bc.GasLimit(),
		}, txs, nil, nil)
		statedb, err := state.New(bc.CurrentBlock().Root(), bc.stateCache)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		_, _, _, err = bc.Processor().Process(ctx, block, statedb, cfg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestStateProcessor(t *testing.T) {

	numTxs := 10000

	ctx := context.Background()
	ctx = perfutils.WithTimer(ctx)
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

	bc := newTestBlockChainWithGenesis(ctx, false, true, genesis)
	t.Logf("newTestBlockchain duration: %s", time.Since(start))
	defer bc.Stop()
	cfg := vm.Config{}
	// statedb, err := state.New(bc.CurrentBlock().Root(), bc.stateCache)
	// 	if err != nil {
	// 		t.Fatal(err)
	// 	}
	perfTimer := perfutils.GetTimer(ctx)
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
	ps := perfTimer.Start(perfutils.Process)
	_, _, _, err = bc.Processor().Process(ctx, block, statedb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ps.Stop()
	t.Log(perfTimer.Print())
	t.Logf("process() duration: %s", time.Since(start))
}
