package gasprice

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/gochain/gochain/v5"
	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/core/types"
	"github.com/gochain/gochain/v5/crypto"
	"github.com/gochain/gochain/v5/params"
	"github.com/gochain/gochain/v5/rpc"
	"math/rand"
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
			backend: newTestBackend(nil, block{
				txs: []tx{{price: 1}},
			}),
		},
		{
			name: "single",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     1,
				Percentile: 60,
			},
			backend: newTestBackend(nil, block{
				txs: []tx{
					{price: 1000},
				},
			}),
		},
		{
			name: "single full",
			exp:  2000,
			params: Config{
				Blocks:     1,
				Percentile: 60,
				Default:    bigInt(1),
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs: []tx{
					{price: 2000},
				},
			}),
		},
		{
			name: "incomplete",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     10,
				Percentile: 60,
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs: []tx{
					{price: Default.Uint64() * 10},
				},
			}),
		},
		{
			name: "some full",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     5,
				Percentile: 60,
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs:  []tx{{price: 10}},
			}, block{
				txs: []tx{{price: 1}},
			}, block{
				txs: []tx{{price: 5}},
			}, block{
				full: true,
				txs:  []tx{{price: 7}},
			}, block{
				txs: []tx{{price: 20}},
			}),
		},
		{
			name: "some full-100",
			exp:  20,
			params: Config{
				Blocks:     5,
				Percentile: 100,
				Default:    bigInt(1),
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs:  []tx{{price: 10}},
			}, block{
				txs: []tx{{price: 1}},
			}, block{
				txs: []tx{{price: 5}},
			}, block{
				full: true,
				txs:  []tx{{price: 7}},
			}, block{
				full: true,
				txs:  []tx{{price: 20}},
			}),
		},
		{
			name: "all full",
			exp:  7,
			params: Config{
				Blocks:     5,
				Percentile: 50,
				Default:    bigInt(1),
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs:  []tx{{price: 10}},
			}, block{
				full: true,
				txs:  []tx{{price: 1}},
			}, block{
				full: true,
				txs:  []tx{{price: 5}},
			}, block{
				full: true,
				txs:  []tx{{price: 7}},
			}, block{
				full: true,
				txs:  []tx{{price: 20}},
			}),
		},
		{
			name: "some empty",
			exp:  5,
			params: Config{
				Blocks:     5,
				Percentile: 60,
				Default:    bigInt(1),
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs:  []tx{{price: 10}},
			}, block{}, block{
				full: true,
				txs:  []tx{{price: 5}},
			}, block{
				full: true,
				txs:  []tx{{price: 7}},
			}, block{}),
		},
		{
			name: "all empty",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     5,
				Percentile: 50,
			},
			backend: newTestBackend(nil, block{}, block{}, block{}, block{}, block{}),
		},
		{
			name: "all full local",
			exp:  1,
			params: Config{
				Blocks:     5,
				Percentile: 50,
				Default:    bigInt(1),
			},
			backend: newTestBackend(nil, block{
				full: true,
				txs:  []tx{{price: 10, local: true}},
			}, block{
				full: true,
				txs:  []tx{{price: 50, local: true}},
			}, block{
				full: true,
				txs:  []tx{{price: 5, local: true}},
			}, block{
				full: true,
				txs:  []tx{{price: 7, local: true}},
			}, block{
				full: true,
				txs:  []tx{{price: 20, local: true}},
			}),
		},
		{
			name: "darvaza-before",
			exp:  Default.Uint64(),
			params: Config{
				Blocks:     5,
				Percentile: 50,
				Default:    nil,
			},
			backend: newTestBackend(&params.ChainConfig{
				DarvazaBlock:      big.NewInt(140000),
				DarvazaDefaultGas: new(big.Int).Mul(Default, bigInt(2))},
				block{},
			),
		},
		{
			name: "darvaza-after",
			exp:  2 * Default.Uint64(),
			params: Config{
				Blocks:     5,
				Percentile: 50,
				Default:    nil,
			},
			backend: newTestBackend(&params.ChainConfig{
				DarvazaBlock:      big.NewInt(14),
				DarvazaDefaultGas: new(big.Int).Mul(Default, bigInt(2))},
				block{},
				block{},
			),
		},
	} {
		t.Run(test.name, test.run)
	}

}

func TestOracle_SuggestPriceContract(t *testing.T) {
	// 1. Test without contract (regular default like it is here)
	config := &params.ChainConfig{
		ChainId: big.NewInt(1),
	}
	backend := newTestBackend(config, block{
		txs: []tx{{price: 1}},
	})

	// Default params
	p := Config{
		Blocks:     1,
		Percentile: 60,
	}

	o := NewOracle(backend, p)
	got, err := o.SuggestPrice(context.Background())
	if err != nil {
		t.Fatalf("failed to suggest price: %v", err)
	}
	// Expect default behavior (median of block prices)
	if got.Uint64() != Default.Uint64() {
		t.Errorf("expected default price %d, got %d", Default.Uint64(), got.Uint64())
	}

	// 2. Set contract address
	contractAddr := common.HexToAddress("0x0000000000000000000000000000000000000123")
	config.GasPriceContractAddress = contractAddr
	// Activate the contract immediately
	config.GasPriceContractBlock = big.NewInt(0)

	// 3. Test with contract
	// Set initial price on contract
	initialPrice := big.NewInt(5000) // 5000 gwei
	backend.(*testBackend).SetContractGasPrice(initialPrice)

	// Invalidate cache by changing head
	backend.(*testBackend).lastHeader.Number.Add(backend.(*testBackend).lastHeader.Number, big.NewInt(1))

	got, err = o.SuggestPrice(context.Background())
	if err != nil {
		t.Fatalf("failed to suggest price with contract: %v", err)
	}

	if got.Cmp(initialPrice) != 0 {
		t.Errorf("expected contract price %d, got %d", initialPrice, got)
	}

	// 4. Call setGasPrice on contract (simulate by updating backend)
	newPrice := big.NewInt(10000) // 10000 gwei
	backend.(*testBackend).SetContractGasPrice(newPrice)

	// Invalidate cache by changing head
	backend.(*testBackend).lastHeader.Number.Add(backend.(*testBackend).lastHeader.Number, big.NewInt(1))

	// 5. Test for new price
	got, err = o.SuggestPrice(context.Background())
	if err != nil {
		t.Fatalf("failed to suggest new price with contract: %v", err)
	}

	if got.Cmp(newPrice) != 0 {
		t.Errorf("expected new contract price %d, got %d", newPrice, got)
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
	contractPrice *big.Int
}

type block struct {
	full bool
	txs  []tx
}

type tx struct {
	price uint64
	local bool
}

func newTestBackend(config *params.ChainConfig, blockSpec ...block) OracleBackend {
	tb := &testBackend{config: config}
	if tb.config == nil {
		tb.config = params.MainnetChainConfig
	}
	number := rand.Intn(1000)
	localKey, _ := crypto.GenerateKey()
	localAddr := crypto.PubkeyToAddress(localKey.PublicKey)
	otherKey, _ := crypto.GenerateKey()
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
		tb.blocks = append(tb.blocks, types.NewBlock(header, txs, nil, nil))
	}
	if l := len(tb.blocks); l > 0 {
		tb.lastHeader = tb.blocks[len(tb.blocks)-1].Header()
	}
	return tb
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

func (b *testBackend) CallContract(ctx context.Context, msg gochain.CallMsg, blockNumber *big.Int) ([]byte, error) {
	if b.config.GasPriceContractAddress != (common.Address{}) && msg.To != nil && *msg.To == b.config.GasPriceContractAddress {
		if string(msg.Data) == string(gasPriceMethodID) {
			if b.contractPrice != nil {
				return common.BigToHash(b.contractPrice).Bytes(), nil
			}
		}
	}
	// Test backend doesn't support contract calls - return error to trigger fallback
	return nil, errors.New("contract calls not supported in test backend")
}

func (b *testBackend) SetContractGasPrice(price *big.Int) {
	b.contractPrice = price
}

func bigInt(i uint64) *big.Int {
	return new(big.Int).SetUint64(i)
}

func transaction(nonce uint64, gasPrice uint64, key *ecdsa.PrivateKey) *types.Transaction {
	tx, _ := types.SignTx(types.NewTransaction(nonce, common.Address{}, big.NewInt(100), 100, bigInt(gasPrice), nil), types.HomesteadSigner{}, key)
	return tx
}
