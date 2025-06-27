package gasprice

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"

	"math/rand"

	"github.com/gochain/gochain/v4/accounts/abi"
	"github.com/gochain/gochain/v4/accounts/abi/bind"
	"github.com/gochain/gochain/v4/accounts/abi/bind/backends"
	"github.com/gochain/gochain/v4/common"
	"github.com/gochain/gochain/v4/core"
	"github.com/gochain/gochain/v4/core/types"
	"github.com/gochain/gochain/v4/crypto"
	"github.com/gochain/gochain/v4/params"
	"github.com/gochain/gochain/v4/rpc"
)

func TestDeployAndBindGasPriceContract(t *testing.T) {
	type ContractData struct {
		ABI      abi.ABI `json:"abi"`
		Bytecode string  `json:"bytecode"`
	}
	raw, err := os.ReadFile("../../contracts/gasPrice.json")
	if err != nil {
		t.Fatalf("Failed to read gasPrice.json: %v", err)
	}
	var contractData ContractData
	if err := json.Unmarshal(raw, &contractData); err != nil {
		t.Fatalf("Failed to parse contract JSON: %v", err)
	}
	bytecode := common.FromHex(contractData.Bytecode)
	testKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}
	backend := backends.NewSimulatedBackend(
		core.GenesisAlloc{
			crypto.PubkeyToAddress(testKey.PublicKey): {Balance: big.NewInt(10000000000)},
		},
	)
	// Create the transaction.
	tx := types.NewContractCreation(0, big.NewInt(0), 300000, big.NewInt(1), bytecode)
	tx, _ = types.SignTx(tx, types.HomesteadSigner{}, testKey)
	// Wait for it to get mined in the background.
	var (
		address common.Address
		mined   = make(chan struct{})
		ctx     = context.Background()
	)
	go func() {
		address, err = bind.WaitDeployed(ctx, backend, tx)
		close(mined)
	}()
	// Send and mine the transaction.
	backend.SendTransaction(ctx, tx)
	backend.Commit()
	select {
	case <-mined:
	case <-time.After(2 * time.Second):
		t.Errorf("test %q: timeout", t.Name())
	}
	// Create the transaction.
	auth := bind.NewKeyedTransactor(testKey)
	if err != nil {
		t.Fatalf("Failed to create authorized transactor: %v", err)
	}
	boundContract := bind.NewBoundContract(address, contractData.ABI, backend, backend, backend)
	// Call the 'setGasPrice' method on the contract.
	setPrice := big.NewInt(98765)
	setTx, err := boundContract.Transact(auth, "setGasPrice", setPrice)
	if err != nil {
		t.Fatalf("Failed to transact 'setGasPrice': %v", err)
	}
	backend.Commit()
	if _, err := bind.WaitMined(context.Background(), backend, setTx); err != nil {
		t.Fatalf("Failed to mine 'setGasPrice' transaction: %v", err)
	}
	// Call the 'gasPrice' view method to retrieve the value.
	var result []interface{}
	if err := boundContract.Call(&bind.CallOpts{}, &result, "gasPrice"); err != nil {
		t.Fatalf("Failed to call 'gasPrice': %v", err)
	}
	// Validate the result.
	if len(result) == 0 {
		t.Fatal("Expected a result from 'gasPrice' call, got none")
	}

	if retrievedPrice, ok := result[0].(*big.Int); !ok || setPrice.Cmp(retrievedPrice) != 0 {
		t.Errorf("Gas price mismatch: want %s, got %v", setPrice, result[0])
	}

}

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
	got, err := o.SuggestGasPrice(context.Background())
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

func bigInt(i uint64) *big.Int {
	return new(big.Int).SetUint64(i)
}

func transaction(nonce uint64, gasPrice uint64, key *ecdsa.PrivateKey) *types.Transaction {
	tx, _ := types.SignTx(types.NewTransaction(nonce, common.Address{}, big.NewInt(100), 100, bigInt(gasPrice), nil), types.HomesteadSigner{}, key)
	return tx
}
