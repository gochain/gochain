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
	"github.com/gochain-io/gochain/v3/miner"
	"math/big"
	"time"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/core"
	"github.com/gochain-io/gochain/v3/eth/downloader"
	"github.com/gochain-io/gochain/v3/eth/gasprice"
	"github.com/gochain-io/gochain/v3/params"
)

// DefaultConfig contains default settings for use on the GoChain main net.
var DefaultConfig = Config{
	SyncMode:       downloader.FastSync,
	NetworkId:      params.MainnetChainID,
	LightPeers:     100,
	DatabaseCache:  512,
	TrieCleanCache: 256,
	TrieDirtyCache: 256,
	TrieTimeout:    60 * time.Minute,
	Miner: miner.Config{
		GasFloor: params.TargetGasLimit,
		GasCeil:  params.TargetGasLimit,
		GasPrice: gasprice.Default,
		Recommit: 1 * time.Second,
	},
	TxPool: core.DefaultTxPoolConfig,
	GPO: gasprice.Config{
		Blocks:     5,
		Percentile: 60,
	},
}

//go:generate gencodec -type Config -formats toml -out gen_config.go

type Config struct {
	// The genesis block, which is inserted if the database is empty.
	// If nil, the GoChain main net block is used.
	Genesis *core.Genesis `toml:",omitempty"`

	// Protocol options
	NetworkId uint64 // Network ID to use for selecting peers to connect to
	SyncMode  downloader.SyncMode

	NoPruning  bool // Whether to disable pruning and flush everything to disk
	NoPrefetch bool // Whether to disable prefetching and only load state on demand

	// Whitelist of required block number -> hash values to accept
	Whitelist map[uint64]common.Hash `toml:"-"`

	// Light client options
	LightServ         int  `toml:",omitempty"` // Maximum percentage of time allowed for serving LES requests
	LightBandwidthIn  int  `toml:",omitempty"` // Incoming bandwidth limit for light servers
	LightBandwidthOut int  `toml:",omitempty"` // Outgoing bandwidth limit for light servers
	LightPeers        int  `toml:",omitempty"` // Maximum number of LES client peers
	OnlyAnnounce      bool // Maximum number of LES client peers

	// Ultra Light client options
	ULC *ULCConfig `toml:",omitempty"`

	// Database options
	SkipBcVersionCheck bool `toml:"-"`
	DatabaseHandles    int  `toml:"-"`
	DatabaseCache      int

	TrieCleanCache int
	TrieDirtyCache int
	TrieTimeout    time.Duration

	// Mining options
	Miner miner.Config

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

	// RPCGasCap is the global gas cap for eth-call variants.
	RPCGasCap *big.Int `toml:",omitempty"`
}
