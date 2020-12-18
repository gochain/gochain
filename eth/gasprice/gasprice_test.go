package gasprice

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"math/rand"

	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/crypto"
	"github.com/gochain/gochain/v3/params"
	"github.com/gochain/gochain/v3/rpc"
)

func TestOracle_SuggestPrice(t *testing.T) {
	for _, test := range []suggestPriceTest{
		{
			name: "default",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     1,
				Percentile: 60,
			},
			backend: newTestBackend(
				block{
					txs: []tx{{price: 1}},
				},
			),
		},
		{
			name: "single",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     1,
				Percentile: 60,
			},
			backend: newTestBackend(
				block{
					txs: []tx{
						{price: 1000},
					},
				},
			),
		},
		{
			name: "single full",
			exp:  2000,
			params: Config{
				Blocks:     1,
				Percentile: 60,
			},
			backend: newTestBackend(
				block{
					full: true,
					txs: []tx{
						{price: 2000},
					},
				},
			),
		},
		{
			name: "incomplete",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     10,
				Percentile: 60,
			},
			backend: newTestBackend(
				block{
					full: true,
					txs: []tx{
						{price: Default.Uint64() * 10},
					},
				},
			),
		},
		{
			name: "some full",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     5,
				Percentile: 60,
			},
			backend: newTestBackend(
				block{
					full: true,
					txs:  []tx{{price: 10}},
				},
				block{
					txs: []tx{{price: 1}},
				},
				block{
					txs: []tx{{price: 5}},
				},
				block{
					full: true,
					txs:  []tx{{price: 7}},
				},
				block{
					txs: []tx{{price: 20}},
				},
			),
		},
		{
			name: "some full-100",
			exp:  20,
			params: Config{
				Blocks:     5,
				Percentile: 100,
				Default:    bigInt(1),
			},
			backend: newTestBackend(
				block{
					full: true,
					txs:  []tx{{price: 10}},
				},
				block{
					txs: []tx{{price: 1}},
				},
				block{
					txs: []tx{{price: 5}},
				},
				block{
					full: true,
					txs:  []tx{{price: 7}},
				},
				block{
					full: true,
					txs:  []tx{{price: 20}},
				},
			),
		},
		{
			name: "all full",
			exp:  7,
			params: Config{
				Blocks:     5,
				Percentile: 50,
			},
			backend: newTestBackend(
				block{
					full: true,
					txs:  []tx{{price: 10}},
				},
				block{
					full: true,
					txs:  []tx{{price: 1}},
				},
				block{
					full: true,
					txs:  []tx{{price: 5}},
				},
				block{
					full: true,
					txs:  []tx{{price: 7}},
				},
				block{
					full: true,
					txs:  []tx{{price: 20}},
				},
			),
		},
		{
			name: "some empty",
			exp:  5,
			params: Config{
				Blocks:     5,
				Percentile: 60,
				Default:    bigInt(1),
			},
			backend: newTestBackend(
				block{
					full: true,
					txs:  []tx{{price: 10}},
				},
				block{},
				block{
					full: true,
					txs:  []tx{{price: 5}},
				},
				block{
					full: true,
					txs:  []tx{{price: 7}},
				},
				block{},
			),
		},
		{
			name: "all empty",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     5,
				Percentile: 50,
			},
			backend: newTestBackend(
				block{},
				block{},
				block{},
				block{},
				block{},
			),
		},
		{
			name: "all full local",
			exp:  1,
			params: Config{
				Blocks:     5,
				Percentile: 50,
				Default:    bigInt(1),
			},
			backend: newTestBackend(
				block{
					full: true,
					txs:  []tx{{price: 10, local: true}},
				},
				block{
					full: true,
					txs:  []tx{{price: 50, local: true}},
				},
				block{
					full: true,
					txs:  []tx{{price: 5, local: true}},
				},
				block{
					full: true,
					txs:  []tx{{price: 7, local: true}},
				},
				block{
					full: true,
					txs:  []tx{{price: 20, local: true}},
				},
			),
		},
	} {
		t.Run(test.name, test.run)
	}

}

type suggestPriceTest struct {
	name    string
	exp     uint64
	params  Config
	backend OracleBackend
}

func (test *suggestPriceTest) run(t *testing.T) {
	o := NewOracle(test.backend, test.params)
	got, err := o.SuggestPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Uint64() != test.exp {
		t.Errorf("expected %d but got %s", test.exp, got)
	}
}

type testBackend struct {
	config     *params.ChainConfig
	lastHeader *types.Header
	blocks     []*types.Block
}

type block struct {
	full bool
	txs  []tx
}

type tx struct {
	price uint64
	local bool
}

func newTestBackend(blockSpec ...block) OracleBackend {
	number := rand.Intn(1000)
	localKey, _ := crypto.GenerateKey()
	localAddr := crypto.PubkeyToAddress(localKey.PublicKey)
	otherKey, _ := crypto.GenerateKey()
	var blocks []*types.Block
	for i, b := range blockSpec {
		gasUsed := uint64(len(b.txs)) * params.TxGas
		gasLimit := gasUsed
		if !b.full {
			gasLimit += params.TxGas * 5
		}
		header := &types.Header{
			Number:   new(big.Int).SetUint64(uint64(number + i)),
			GasUsed:  gasUsed,
			GasLimit: gasLimit,
			Coinbase: localAddr,
		}
		var txs []*types.Transaction
		for _, tx := range b.txs {
			key := otherKey
			if tx.local {
				key = localKey
			}
			txs = append(txs, transaction(0, tx.price, key))
		}
		blocks = append(blocks, types.NewBlock(header, txs, nil, nil))
	}
	return &testBackend{
		config:     params.MainnetChainConfig,
		lastHeader: blocks[len(blocks)-1].Header(),
		blocks:     blocks,
	}
}

func (b *testBackend) ChainConfig() *params.ChainConfig {
	return b.config
}

func (b *testBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	if blockNr == rpc.LatestBlockNumber {
		return b.lastHeader, nil
	}
	return nil, nil
}

func (b *testBackend) BlockByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Block, error) {
	for _, block := range b.blocks {
		if block.Number().Int64() == int64(blockNr) {
			return block, nil
		}
	}
	return nil, nil
}

func bigInt(i uint64) *big.Int {
	return new(big.Int).SetUint64(i)
}

func transaction(nonce uint64, gasPrice uint64, key *ecdsa.PrivateKey) *types.Transaction {
	tx, _ := types.SignTx(types.NewTransaction(nonce, common.Address{}, big.NewInt(100), 100, bigInt(gasPrice), nil), types.HomesteadSigner{}, key)
	return tx
}
