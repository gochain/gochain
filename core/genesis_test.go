// Copyright 2017 The go-ethereum Authors
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
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"math/big"
	"reflect"
	"testing"

	"github.com/davecgh/go-spew/spew"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/consensus/clique"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/params"
)

func TestDefaultGenesisBlockHash(t *testing.T) {
	for _, test := range []struct {
		name     string
		exp, got common.Hash
	}{
		{"mainnet", params.MainnetGenesisHash, DefaultGenesisBlock().ToBlock(nil).Hash()},
		{"testnet", params.TestnetGenesisHash, DefaultTestnetGenesisBlock().ToBlock(nil).Hash()},
	} {
		if test.exp != test.got {
			t.Errorf("wrong %s genesis hash, got %s, want %s", test.name, test.got.Hex(), test.exp.Hex())
		}
	}
}

func TestSetupGenesis(t *testing.T) {
	ctx := context.Background()
	var (
		customghash = common.HexToHash("0x2ba5deb4ad470a1b170475fe5004f0e0cefffa8e18469e6510937e74db11094a")
		customg     = Genesis{
			Config: &params.ChainConfig{
				HomesteadBlock: big.NewInt(3),
				Clique:         params.DefaultCliqueConfig(),
			},
			Alloc: GenesisAlloc{
				{1}: {Balance: big.NewInt(1), Storage: map[common.Hash]common.Hash{{1}: {1}}},
			},
		}
		oldcustomg = customg
	)
	oldcustomg.Config = &params.ChainConfig{
		HomesteadBlock: big.NewInt(2),
		Clique:         params.DefaultCliqueConfig(),
	}
	tests := []struct {
		name       string
		fn         func(common.Database) (*params.ChainConfig, common.Hash, error)
		wantConfig *params.ChainConfig
		wantHash   common.Hash
		wantErr    error
	}{
		{
			name: "genesis without ChainConfig",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				return SetupGenesisBlock(db, new(Genesis))
			},
			wantErr:    errGenesisNoConfig,
			wantConfig: params.AllCliqueProtocolChanges,
		},
		{
			name: "no block in DB, genesis == nil",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   params.MainnetGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "mainnet block in DB, genesis == nil",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				DefaultGenesisBlock().MustCommit(db)
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   params.MainnetGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "custom block in DB, genesis == nil",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				customg.MustCommit(db)
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   customghash,
			wantConfig: customg.Config,
		},
		{
			name: "custom block in DB, genesis == testnet",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				customg.MustCommit(db)
				return SetupGenesisBlock(db, DefaultTestnetGenesisBlock())
			},
			wantErr:    &GenesisMismatchError{Stored: customghash, New: params.TestnetGenesisHash},
			wantHash:   params.TestnetGenesisHash,
			wantConfig: params.TestnetChainConfig,
		},
		{
			name: "compatible config in DB",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				oldcustomg.MustCommit(db)
				return SetupGenesisBlock(db, &customg)
			},
			wantHash:   customghash,
			wantConfig: customg.Config,
		},
		{
			name: "incompatible config in DB",
			fn: func(db common.Database) (*params.ChainConfig, common.Hash, error) {
				// Commit the 'old' genesis block with Homestead transition at #2.
				// Advance to block #4, past the homestead transition block of customg.
				genesis := oldcustomg.MustCommit(db)

				bc, _ := NewBlockChain(ctx, db, nil, oldcustomg.Config, clique.NewFullFaker(), vm.Config{})
				defer bc.Stop()

				blocks, _ := GenerateChain(ctx, oldcustomg.Config, genesis, clique.NewFaker(), db, 4, nil)
				_, err := bc.InsertChain(ctx, blocks)
				if err != nil {
					return nil, common.Hash{}, err
				}
				bc.CurrentBlock()
				// This should return a compatibility error.
				return SetupGenesisBlock(db, &customg)
			},
			wantHash:   customghash,
			wantConfig: customg.Config,
			wantErr: &params.ConfigCompatError{
				What:         "Homestead fork block",
				StoredConfig: big.NewInt(2),
				NewConfig:    big.NewInt(3),
				RewindTo:     1,
			},
		},
	}

	for _, test := range tests {
		db := ethdb.NewMemDatabase()
		config, hash, err := test.fn(db)
		// Check the return values.
		if !reflect.DeepEqual(err, test.wantErr) {
			spew := spew.ConfigState{DisablePointerAddresses: true, DisableCapacities: true}
			t.Fatalf("%s:\nhave: %#v\nwant: %#v\n", test.name, spew.NewFormatter(err), spew.NewFormatter(test.wantErr))
		}
		if !reflect.DeepEqual(config, test.wantConfig) {
			t.Errorf("%s:\nhave: %v\nwant: %v\n", test.name, config, test.wantConfig)
		}
		if hash != test.wantHash {
			t.Errorf("%s:\nhave: %s\nwant: %s\n", test.name, hash.Hex(), test.wantHash.Hex())
		} else if err == nil {
			// Check database content.
			stored := GetBlock(db, test.wantHash, 0)
			if stored.Hash() != test.wantHash {
				t.Errorf("%s: block in DB has hash %s, want %s", test.name, stored.Hash(), test.wantHash)
			}
		}
	}
}

func TestDefaultGenesisBlock(t *testing.T) {
	for _, test := range []struct {
		name    string
		file    string
		genesis *Genesis
	}{
		{"mainnet", "genesis-main.json", DefaultGenesisBlock()},
		{"testnet", "genesis-test.json", DefaultTestnetGenesisBlock()},
	} {
		exp, err := ioutil.ReadFile(test.file)
		if err != nil {
			t.Fatal(err)
		}
		exp = bytes.TrimSpace(exp)
		got, err := json.MarshalIndent(test.genesis, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(exp, got) {
			t.Errorf("mismatched %s genesis, expected:\n%s\ngot:\n%s", test.name, string(exp), string(got))
		}
	}
}

func TestGenesisAlloc_Total(t *testing.T) {
	for _, test := range []struct {
		name  string
		exp   *big.Int
		alloc GenesisAlloc
	}{
		{
			name:  "mainnet",
			exp:   new(big.Int).Mul(big.NewInt(params.Ether), big.NewInt(1000000000)),
			alloc: DefaultGenesisBlock().Alloc,
		},
		{
			name:  "testnet",
			exp:   new(big.Int).Mul(big.NewInt(params.Ether), big.NewInt(1000000000)),
			alloc: DefaultTestnetGenesisBlock().Alloc,
		},
		{
			name: "custom",
			exp:  big.NewInt(100),
			alloc: GenesisAlloc{
				common.StringToAddress("a"): {Balance: big.NewInt(1)},
				common.StringToAddress("b"): {Balance: big.NewInt(10)},
				common.StringToAddress("c"): {Balance: big.NewInt(19)},
				common.StringToAddress("d"): {Balance: big.NewInt(30)},
				common.StringToAddress("e"): {Balance: big.NewInt(40)},
			},
		},
	} {
		if got := test.alloc.Total(); got.Cmp(test.exp) != 0 {
			t.Errorf("%s: expected %s but got %s", test.name, test.exp, got)
		}
	}
}
