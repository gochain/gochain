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

package eth

import (
	"math/big"
	"time"

	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/common/hexutil"
	"github.com/gochain/gochain/v3/core"
	"github.com/gochain/gochain/v3/eth/downloader"
	"github.com/gochain/gochain/v3/eth/gasprice"
	"github.com/gochain/gochain/v3/params"
)

// DefaultConfig contains default settings for use on the GoChain main net.
var DefaultConfig = Config{
	SyncMode:      downloader.FastSync,
	NetworkId:     params.MainnetChainID,
	LightPeers:    100,
	DatabaseCache: 768,
	TrieCache:     256,
	TrieTimeout:   60 * time.Minute,
	MinerGasFloor: params.TargetGasLimit,
	MinerGasCeil:  params.TargetGasLimit,
	MinerGasPrice: nil,
	MinerRecommit: 1 * time.Second,

	TxPool: core.DefaultTxPoolConfig,
	GPO: gasprice.Config{
		Blocks:     20,
		Percentile: 60,
		MaxPrice:   gasprice.DefaultMaxPrice,
	},
}

//go:generate gencodec -type Config -field-override configMarshaling -formats toml -out gen_config.go

type Config struct {
	// The genesis block, which is inserted if the database is empty.
	// If nil, the GoChain main net block is used.
	Genesis *core.Genesis `toml:",omitempty"`

	// Protocol options
	NetworkId uint64 // Network ID to use for selecting peers to connect to
	SyncMode  downloader.SyncMode
	NoPruning bool

	// Light client options
	LightServ  int `toml:",omitempty"` // Maximum percentage of time allowed for serving LES requests
	LightPeers int `toml:",omitempty"` // Maximum number of LES client peers

	// Database options
	SkipBcVersionCheck bool `toml:"-"`
	DatabaseHandles    int  `toml:"-"`
	DatabaseCache      int
	TrieCache          int
	TrieTimeout        time.Duration

	// Mining-related options
	Etherbase      common.Address `toml:",omitempty"`
	MinerNotify    []string       `toml:",omitempty"`
	MinerExtraData []byte         `toml:",omitempty"`
	MinerGasFloor  uint64
	MinerGasCeil   uint64
	MinerGasPrice  *big.Int // nil for default/dynamic
	MinerRecommit  time.Duration
	MinerNoverify  bool

	// Transaction pool options
	TxPool core.TxPoolConfig

	// Gas Price Oracle options
	GPO gasprice.Config

	// Enables tracking of SHA3 preimages in the VM
	EnablePreimageRecording bool

	// Miscellaneous options
	DocRoot string `toml:"-"`

	// Type of the EWASM interpreter ("" for default)
	EWASMInterpreter string

	// Type of the EVM interpreter ("" for default)
	EVMInterpreter string

	// Constantinople block override (TODO: remove after the fork)
	ConstantinopleOverride *big.Int
}

type configMarshaling struct {
	MinerExtraData hexutil.Bytes
}
