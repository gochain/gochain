// Copyright 2014 The go-ethereum Authors
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

// Package eth implements the GoChain protocol.
package eth

import (
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/gochain-io/gochain/v3/accounts"
	"github.com/gochain-io/gochain/v3/accounts/keystore"
	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/consensus"
	"github.com/gochain-io/gochain/v3/consensus/clique"
	"github.com/gochain-io/gochain/v3/core"
	"github.com/gochain-io/gochain/v3/core/bloombits"
	"github.com/gochain-io/gochain/v3/core/rawdb"
	"github.com/gochain-io/gochain/v3/core/types"
	"github.com/gochain-io/gochain/v3/core/vm"
	"github.com/gochain-io/gochain/v3/eth/downloader"
	"github.com/gochain-io/gochain/v3/eth/filters"
	"github.com/gochain-io/gochain/v3/eth/gasprice"
	"github.com/gochain-io/gochain/v3/internal/ethapi"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/miner"
	"github.com/gochain-io/gochain/v3/node"
	"github.com/gochain-io/gochain/v3/p2p"
	"github.com/gochain-io/gochain/v3/params"
	"github.com/gochain-io/gochain/v3/rpc"
)

type LesServer interface {
	Start(srvr *p2p.Server)
	Stop()
	APIs() []rpc.API
	Protocols() []p2p.Protocol
	SetBloomBitsIndexer(bbIndexer *core.ChainIndexer)
}

// GoChain implements the GoChain full node service.
type GoChain struct {
	config      *Config
	chainConfig *params.ChainConfig

	// Channel for shutting down the service
	shutdownChan chan bool // Channel for shutting down the GoChain

	// Handlers
	txPool          *core.TxPool
	blockchain      *core.BlockChain
	protocolManager *ProtocolManager
	lesServer       LesServer

	// DB interfaces
	chainDb common.Database // Block chain database

	eventMux       *core.InterfaceFeed
	engine         consensus.Engine
	accountManager *accounts.Manager

	bloomRequests chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	bloomIndexer  *core.ChainIndexer             // Bloom indexer operating during block imports

	APIBackend *EthAPIBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	etherbase common.Address

	networkID     uint64
	netRPCService *ethapi.PublicNetAPI

	lock sync.RWMutex // Protects the variadic fields (e.g. gas price and etherbase)
}

func (gc *GoChain) AddLesServer(ls LesServer) {
	gc.lesServer = ls
	ls.SetBloomBitsIndexer(gc.bloomIndexer)
}

// New creates a new GoChain object (including the
// initialisation of the common GoChain object)
func New(ctx *node.ServiceContext, config *Config) (*GoChain, error) {
	// Ensure configuration values are compatible and sane
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run eth.GoChain in light sync mode, use les.LightGoChain")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
	}
	if config.MinerGasPrice == nil || config.MinerGasPrice.Cmp(common.Big0) <= 0 {
		log.Warn("Sanitizing invalid miner gas price", "provided", config.MinerGasPrice, "updated", DefaultConfig.MinerGasPrice)
		config.MinerGasPrice = new(big.Int).Set(DefaultConfig.MinerGasPrice)
	}
	if config.NoPruning && config.TrieDirtyCache > 0 {
		config.TrieCleanCache += config.TrieDirtyCache
		config.TrieDirtyCache = 0
	}
	log.Info("Allocated trie memory caches", "clean", common.StorageSize(config.TrieCleanCache)*1024*1024, "dirty", common.StorageSize(config.TrieDirtyCache)*1024*1024)

	// Assemble the Ethereum object
	chainDb, err := CreateDB(ctx, config, "chaindata")
	if err != nil {
		return nil, err
	}
	chainConfig, genesisHash, genesisErr := core.SetupGenesisBlockWithOverride(chainDb, config.Genesis, config.ConstantinopleOverride)
	if _, ok := genesisErr.(*params.ConfigCompatError); genesisErr != nil && !ok {
		return nil, genesisErr
	}
	if config.Genesis == nil {
		if genesisHash == params.MainnetGenesisHash {
			config.Genesis = core.DefaultGenesisBlock()
		}
	}
	log.Info("Initialised chain configuration", "config", chainConfig)

	if chainConfig.Clique == nil {
		return nil, fmt.Errorf("invalid configuration, clique is nil: %v", chainConfig)
	}
	eth := &GoChain{
		config:         config,
		chainDb:        chainDb,
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		accountManager: ctx.AccountManager,
		engine:         clique.New(chainConfig.Clique, chainDb),
		shutdownChan:   make(chan bool),
		networkID:      config.NetworkId,
		gasPrice:       config.MinerGasPrice,
		etherbase:      config.Etherbase,
		bloomRequests:  make(chan chan *bloombits.Retrieval),
		bloomIndexer:   NewBloomIndexer(chainDb, params.BloomBitsBlocks, params.BloomConfirms),
	}

	bcVersion := rawdb.ReadDatabaseVersion(chainDb.GlobalTable())
	var dbVer = "<nil>"
	if bcVersion != nil {
		dbVer = fmt.Sprintf("%d", *bcVersion)
	}
	log.Info("Initialising GoChain protocol", "versions", ProtocolVersions, "network", config.NetworkId, "dbversion", dbVer)

	if !config.SkipBcVersionCheck {
		bcVersion := rawdb.ReadDatabaseVersion(chainDb.GlobalTable())
		if bcVersion != nil && *bcVersion > core.BlockChainVersion {
			return nil, fmt.Errorf("database version is v%d, GoChain %s only supports v%d", *bcVersion, params.Version, core.BlockChainVersion)
		} else if bcVersion == nil || *bcVersion < core.BlockChainVersion {
			log.Warn("Upgrade blockchain database version", "from", dbVer, "to", core.BlockChainVersion)
			rawdb.WriteDatabaseVersion(chainDb.GlobalTable(), core.BlockChainVersion)
		}
	}
	var (
		vmConfig = vm.Config{
			EnablePreimageRecording: config.EnablePreimageRecording,
			EWASMInterpreter:        config.EWASMInterpreter,
			EVMInterpreter:          config.EVMInterpreter,
		}
		cacheConfig = &core.CacheConfig{Disabled: config.NoPruning, TrieCleanLimit: config.TrieCleanCache, TrieDirtyLimit: config.TrieDirtyCache, TrieTimeLimit: config.TrieTimeout}
	)
	eth.blockchain, err = core.NewBlockChain(chainDb, cacheConfig, eth.chainConfig, eth.engine, vmConfig)
	if err != nil {
		return nil, err
	}
	// Rewind the chain in case of an incompatible config upgrade.
	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log.Warn("Rewinding chain to upgrade configuration", "err", compat)
		if err := eth.blockchain.SetHead(compat.RewindTo); err != nil {
			log.Error("Cannot set head during chain rewind", "rewind_to", compat.RewindTo, "err", err)
		}
		rawdb.WriteChainConfig(chainDb.GlobalTable(), genesisHash, chainConfig)
	}
	eth.bloomIndexer.Start(eth.blockchain)

	if config.TxPool.Journal != "" {
		config.TxPool.Journal = ctx.ResolvePath(config.TxPool.Journal)
	}
	eth.txPool = core.NewTxPool(config.TxPool, eth.chainConfig, eth.blockchain)

	if eth.protocolManager, err = NewProtocolManager(eth.chainConfig, config.SyncMode, config.NetworkId, eth.eventMux, eth.txPool, eth.engine, eth.blockchain, chainDb, nil); err != nil {
		return nil, err
	}

	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine, config.MinerRecommit, config.MinerGasFloor, config.MinerGasCeil, eth.isLocalBlock)
	eth.miner.SetExtra(makeExtraData(config.MinerExtraData))

	eth.APIBackend = &EthAPIBackend{eth: eth}
	if g := eth.config.Genesis; g != nil {
		eth.APIBackend.initialSupply = g.Alloc.Total()
	}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.MinerGasPrice
	}
	eth.APIBackend.gpo = gasprice.NewOracle(eth.APIBackend, gpoParams)

	return eth, nil
}

// Example: 2.0.73/linux-amd64/go1.10.2
var defaultExtraData []byte
var defaultExtraDataOnce sync.Once

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		defaultExtraDataOnce.Do(func() {
			defaultExtraData = []byte(fmt.Sprintf("%s/%s-%s/%s", params.Version, runtime.GOOS, runtime.GOARCH, runtime.Version()))
			defaultExtraData = defaultExtraData[:params.MaximumExtraDataSize]
		})
		return defaultExtraData
	}
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		log.Warn("Miner extra data exceed limit", "extra", string(extra), "limit", params.MaximumExtraDataSize)
		extra = extra[:params.MaximumExtraDataSize]
	}
	return extra
}

// CreateDB creates the chain database.
func CreateDB(ctx *node.ServiceContext, config *Config, name string) (common.Database, error) {
	db, err := ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
	if err != nil {
		return nil, err
	}
	return db, nil
}

// APIs return the collection of RPC services the ethereum package offers.
// NOTE, some of these services probably need to be moved to somewhere else.
func (gc *GoChain) APIs() []rpc.API {
	apis := ethapi.GetAPIs(gc.APIBackend)

	// Append any APIs exposed explicitly by the les server
	if gc.lesServer != nil {
		apis = append(apis, gc.lesServer.APIs()...)
	}
	// Append any APIs exposed explicitly by the consensus engine
	apis = append(apis, gc.engine.APIs(gc.BlockChain())...)

	// Append all the local APIs and return
	return append(apis, []rpc.API{
		{
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicEthereumAPI(gc),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicMinerAPI(gc),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   downloader.NewPublicDownloaderAPI(gc.protocolManager.downloader, gc.eventMux),
			Public:    true,
		}, {
			Namespace: "miner",
			Version:   "1.0",
			Service:   NewPrivateMinerAPI(gc),
			Public:    false,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   filters.NewPublicFilterAPI(gc.APIBackend, false),
			Public:    true,
		}, {
			Namespace: "admin",
			Version:   "1.0",
			Service:   NewPrivateAdminAPI(gc),
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPublicDebugAPI(gc),
			Public:    true,
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPrivateDebugAPI(gc.chainConfig, gc),
		}, {
			Namespace: "net",
			Version:   "1.0",
			Service:   gc.netRPCService,
			Public:    true,
		},
	}...)
}

func (gc *GoChain) ResetWithGenesisBlock(gb *types.Block) {
	if err := gc.blockchain.ResetWithGenesisBlock(gb); err != nil {
		log.Error("Cannot reset with genesis block", "err", err)
	}
}

func (gc *GoChain) Etherbase() (eb common.Address, err error) {
	gc.lock.RLock()
	etherbase := gc.etherbase
	gc.lock.RUnlock()

	if etherbase != (common.Address{}) {
		return etherbase, nil
	}
	ks := gc.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
	if wallets := ks.Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			etherbase := accounts[0].Address

			gc.lock.Lock()
			gc.etherbase = etherbase
			gc.lock.Unlock()

			log.Info("Etherbase automatically configured", "address", etherbase)
			return etherbase, nil
		}
	}
	return common.Address{}, fmt.Errorf("etherbase must be explicitly specified")
}

// isLocalBlock checks whether the specified block is mined
// by local miner accounts.
//
// We regard two types of accounts as local miner account: etherbase
// and accounts specified via `txpool.locals` flag.
func (gc *GoChain) isLocalBlock(block *types.Block) bool {
	author, err := gc.engine.Author(block.Header())
	if err != nil {
		log.Warn("Failed to retrieve block author", "number", block.NumberU64(), "hash", block.Hash(), "err", err)
		return false
	}
	// Check whether the given address is etherbase.
	gc.lock.RLock()
	etherbase := gc.etherbase
	gc.lock.RUnlock()
	if author == etherbase {
		return true
	}
	// Check whether the given address is specified by `txpool.local`
	// CLI flag.
	for _, account := range gc.config.TxPool.Locals {
		if account == author {
			return true
		}
	}
	return false
}

// SetEtherbase sets the mining reward address.
func (gc *GoChain) SetEtherbase(etherbase common.Address) {
	gc.lock.Lock()
	gc.etherbase = etherbase
	gc.lock.Unlock()

	gc.miner.SetEtherbase(etherbase)
}

// StartMining starts the miner with the given number of CPU threads. If mining
// is already running, this method adjust the number of threads allowed to use
// and updates the minimum price required by the transaction pool.
func (gc *GoChain) StartMining(threads int) error {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := gc.engine.(threaded); ok {
		log.Info("Updated mining threads", "threads", threads)
		if threads == 0 {
			threads = -1 // Disable the miner from within
		}
		th.SetThreads(threads)
	}
	// If the miner was not running, initialize it
	if !gc.IsMining() {
		// Propagate the initial price point to the transaction pool
		gc.lock.RLock()
		price := gc.gasPrice
		gc.lock.RUnlock()
		gc.txPool.SetGasPrice(price)

		// Configure the local mining address
		eb, err := gc.Etherbase()
		if err != nil {
			log.Error("Cannot start mining without etherbase", "err", err)
			return fmt.Errorf("etherbase missing: %v", err)
		}
		if clique, ok := gc.engine.(*clique.Clique); ok {
			wallet, err := gc.accountManager.Find(accounts.Account{Address: eb})
			if wallet == nil || err != nil {
				log.Error("Etherbase account unavailable locally", "err", err)
				return fmt.Errorf("signer missing: %v", err)
			}
			clique.Authorize(eb, wallet.SignData)
		}
		// If mining is started, we can disable the transaction rejection mechanism
		// introduced to speed sync times.
		atomic.StoreUint32(&gc.protocolManager.acceptTxs, 1)

		go gc.miner.Start(eb)
	}
	return nil
}

// StopMining terminates the miner, both at the consensus engine level as well as
// at the block creation level.
func (gc *GoChain) StopMining() {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := gc.engine.(threaded); ok {
		th.SetThreads(-1)
	}
	// Stop the block creating itself
	gc.miner.Stop()
}

func (gc *GoChain) IsMining() bool      { return gc.miner.Mining() }
func (gc *GoChain) Miner() *miner.Miner { return gc.miner }

func (gc *GoChain) AccountManager() *accounts.Manager  { return gc.accountManager }
func (gc *GoChain) BlockChain() *core.BlockChain       { return gc.blockchain }
func (gc *GoChain) TxPool() *core.TxPool               { return gc.txPool }
func (gc *GoChain) EventMux() *core.InterfaceFeed      { return gc.eventMux }
func (gc *GoChain) Engine() consensus.Engine           { return gc.engine }
func (gc *GoChain) ChainDb() common.Database           { return gc.chainDb }
func (gc *GoChain) IsListening() bool                  { return true } // Always listening
func (gc *GoChain) EthVersion() int                    { return int(gc.protocolManager.SubProtocols[0].Version) }
func (gc *GoChain) NetVersion() uint64                 { return gc.networkID }
func (gc *GoChain) Downloader() *downloader.Downloader { return gc.protocolManager.downloader }

// Protocols implements node.Service, returning all the currently configured
// network protocols to start.
func (gc *GoChain) Protocols() []p2p.Protocol {
	if gc.lesServer == nil {
		return gc.protocolManager.SubProtocols
	}
	return append(gc.protocolManager.SubProtocols, gc.lesServer.Protocols()...)
}

// Start implements node.Service, starting all internal goroutines needed by the
// GoChain protocol implementation.
func (gc *GoChain) Start(srvr *p2p.Server) error {
	// Start the bloom bits servicing goroutines
	gc.startBloomHandlers(params.BloomBitsBlocks)

	// Start the RPC service
	gc.netRPCService = ethapi.NewPublicNetAPI(srvr, gc.NetVersion())

	// Figure out a max peers count based on the server limits
	maxPeers := srvr.MaxPeers
	if gc.config.LightServ > 0 {
		if gc.config.LightPeers >= srvr.MaxPeers {
			return fmt.Errorf("invalid peer config: light peer count (%d) >= total peer count (%d)", gc.config.LightPeers, srvr.MaxPeers)
		}
		maxPeers -= gc.config.LightPeers
	}
	// Start the networking layer and the light server if requested
	gc.protocolManager.Start(maxPeers)
	if gc.lesServer != nil {
		gc.lesServer.Start(srvr)
	}
	return nil
}

// Stop implements node.Service, terminating all internal goroutines used by the
// GoChain protocol.
func (gc *GoChain) Stop() error {
	gc.bloomIndexer.Close()
	gc.blockchain.Stop()
	gc.protocolManager.Stop()
	if gc.lesServer != nil {
		gc.lesServer.Stop()
	}
	gc.txPool.Stop()
	gc.miner.Stop()
	gc.eventMux.Close()

	gc.chainDb.Close()
	close(gc.shutdownChan)

	return nil
}
