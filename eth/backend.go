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
	"context"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/gochain-io/gochain/accounts"
	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/consensus"
	"github.com/gochain-io/gochain/consensus/clique"
	"github.com/gochain-io/gochain/core"
	"github.com/gochain-io/gochain/core/bloombits"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/core/vm"
	"github.com/gochain-io/gochain/eth/downloader"
	"github.com/gochain-io/gochain/eth/filters"
	"github.com/gochain-io/gochain/eth/gasprice"
	"github.com/gochain-io/gochain/event"
	"github.com/gochain-io/gochain/internal/ethapi"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/miner"
	"github.com/gochain-io/gochain/node"
	"github.com/gochain-io/gochain/p2p"
	"github.com/gochain-io/gochain/params"
	"github.com/gochain-io/gochain/rpc"
)

type LesServer interface {
	Start(srvr *p2p.Server)
	Stop()
	Protocols() []p2p.Protocol
	SetBloomBitsIndexer(bbIndexer *core.ChainIndexer)
}

// GoChain implements the GoChain full node service.
type GoChain struct {
	config      *Config
	chainConfig *params.ChainConfig

	// Channel for shutting down the service
	shutdownChan  chan bool    // Channel for shutting down the ethereum
	stopDbUpgrade func() error // stop chain db sequential key upgrade

	// Handlers
	txPool          *core.TxPool
	blockchain      *core.BlockChain
	protocolManager *ProtocolManager
	lesServer       LesServer

	// DB interfaces
	chainDb common.Database // Block chain database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	bloomRequests chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	bloomIndexer  *core.ChainIndexer             // Bloom indexer operating during block imports

	ApiBackend *EthApiBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	etherbase common.Address

	networkId     uint64
	netRPCService *ethapi.PublicNetAPI

	lock sync.RWMutex // Protects the variadic fields (e.g. gas price and etherbase)
}

func (gc *GoChain) AddLesServer(ls LesServer) {
	gc.lesServer = ls
	ls.SetBloomBitsIndexer(gc.bloomIndexer)
}

// New creates a new GoChain object (including the
// initialisation of the common GoChain object)
func New(sctx *node.ServiceContext, config *Config) (*GoChain, error) {
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run eth.GoChain in light sync mode, use les.LightGoChain")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
	}
	chainDb, err := CreateDB(sctx, config, "chaindata")
	if err != nil {
		return nil, err
	}

	stopDbUpgrade := func() error { return nil } // upgradeDeduplicateData(chainDb)

	chainConfig, genesisHash, genesisErr := core.SetupGenesisBlock(chainDb, config.Genesis)
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
		eventMux:       sctx.EventMux,
		accountManager: sctx.AccountManager,
		engine:         clique.New(chainConfig.Clique, chainDb),
		shutdownChan:   make(chan bool),
		stopDbUpgrade:  stopDbUpgrade,
		networkId:      config.NetworkId,
		gasPrice:       config.GasPrice,
		etherbase:      config.Etherbase,
		bloomRequests:  make(chan chan *bloombits.Retrieval),
		bloomIndexer:   NewBloomIndexer(chainDb, params.BloomBitsBlocks),
	}

	log.Info("Initialising GoChain protocol", "versions", ProtocolVersions, "network", config.NetworkId)

	if !config.SkipBcVersionCheck {
		bcVersion := core.GetBlockChainVersion(chainDb.GlobalTable())
		if bcVersion != core.BlockChainVersion && bcVersion != 0 {
			return nil, fmt.Errorf("Blockchain DB version mismatch (%d / %d). Run geth upgradedb.\n", bcVersion, core.BlockChainVersion)
		}
		core.WriteBlockChainVersion(chainDb.GlobalTable(), core.BlockChainVersion)
	}
	var (
		vmConfig    = vm.Config{EnablePreimageRecording: config.EnablePreimageRecording}
		cacheConfig = &core.CacheConfig{Disabled: config.NoPruning, TrieNodeLimit: config.TrieCache, TrieTimeLimit: config.TrieTimeout}
	)
	eth.blockchain, err = core.NewBlockChain(context.Background(), chainDb, cacheConfig, eth.chainConfig, eth.engine, vmConfig)
	if err != nil {
		return nil, err
	}
	/*
		if arDB, ok := eth.chainDb.(*archive.DB); ok {
			arDB.Start(func(prefix byte) uint64 {
				switch prefix {
				case 'h':
					return eth.blockchain.CurrentHeader().Number.Uint64()
				case 'b', 'r':
					return eth.blockchain.CurrentBlock().Number().Uint64()
				}
				return 0
			})
		}
	*/
	// Rewind the chain in case of an incompatible config upgrade.
	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log.Warn("Rewinding chain to upgrade configuration", "err", compat)
		if err := eth.blockchain.SetHead(compat.RewindTo); err != nil {
			log.Error("Cannot set head during chain rewind", "rewind_to", compat.RewindTo, "err", err)
		}
		if err := core.WriteChainConfig(chainDb.GlobalTable(), genesisHash, chainConfig); err != nil {
			log.Error("Cannot write chain config during rewind", "hash", genesisHash, "err", err)
		}
	}
	eth.bloomIndexer.Start(eth.blockchain)

	if config.TxPool.Journal != "" {
		config.TxPool.Journal = sctx.ResolvePath(config.TxPool.Journal)
	}
	eth.txPool = core.NewTxPool(config.TxPool, eth.chainConfig, eth.blockchain)

	if eth.protocolManager, err = NewProtocolManager(context.Background(), eth.chainConfig, config.SyncMode, config.NetworkId, eth.eventMux, eth.txPool, eth.engine, eth.blockchain, chainDb); err != nil {
		return nil, err
	}
	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine)
	if err := eth.miner.SetExtra(makeExtraData(config.ExtraData)); err != nil {
		log.Error("Cannot set extra chain data", "err", err)
	}

	eth.ApiBackend = &EthApiBackend{
		eth: eth,
	}
	if g := eth.config.Genesis; g != nil {
		eth.ApiBackend.initialSupply = g.Alloc.Total()
	}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.GasPrice
	}
	eth.ApiBackend.gpo = gasprice.NewOracle(eth.ApiBackend, gpoParams)

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

// APIs returns the collection of RPC services the ethereum package offers.
// NOTE, some of these services probably need to be moved to somewhere else.
func (gc *GoChain) APIs() []rpc.API {
	apis := ethapi.GetAPIs(gc.ApiBackend)

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
			Service:   filters.NewPublicFilterAPI(gc.ApiBackend, false),
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
	if wallets := gc.AccountManager().Wallets(); len(wallets) > 0 {
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

// set in js console via admin interface or wrapper from cli flags
func (gc *GoChain) SetEtherbase(etherbase common.Address) {
	gc.lock.Lock()
	gc.etherbase = etherbase
	gc.lock.Unlock()

	gc.miner.SetEtherbase(etherbase)
}

func (gc *GoChain) StartMining(local bool) error {
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
		clique.Authorize(eb, wallet.SignHash)
	}
	if local {
		// If local (CPU) mining is started, we can disable the transaction rejection
		// mechanism introduced to speed sync times. CPU mining on mainnet is ludicrous
		// so noone will ever hit this path, whereas marking sync done on CPU mining
		// will ensure that private networks work in single miner mode too.
		atomic.StoreUint32(&gc.protocolManager.acceptTxs, 1)
	}
	go gc.miner.Start(eb)
	return nil
}

func (gc *GoChain) StopMining()         { gc.miner.Stop() }
func (gc *GoChain) IsMining() bool      { return gc.miner.Mining() }
func (gc *GoChain) Miner() *miner.Miner { return gc.miner }

func (gc *GoChain) AccountManager() *accounts.Manager  { return gc.accountManager }
func (gc *GoChain) BlockChain() *core.BlockChain       { return gc.blockchain }
func (gc *GoChain) TxPool() *core.TxPool               { return gc.txPool }
func (gc *GoChain) EventMux() *event.TypeMux           { return gc.eventMux }
func (gc *GoChain) Engine() consensus.Engine           { return gc.engine }
func (gc *GoChain) ChainDb() common.Database           { return gc.chainDb }
func (gc *GoChain) IsListening() bool                  { return true } // Always listening
func (gc *GoChain) EthVersion() int                    { return int(gc.protocolManager.SubProtocols[0].Version) }
func (gc *GoChain) NetVersion() uint64                 { return gc.networkId }
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
	gc.startBloomHandlers()

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
	if gc.stopDbUpgrade != nil {
		if err := gc.stopDbUpgrade(); err != nil {
			log.Error("Cannot stop db upgrade", "err", err)
		}
	}
	if err := gc.bloomIndexer.Close(); err != nil {
		log.Error("Cannot stop bloom indexer", "err", err)
	}
	gc.blockchain.Stop()
	gc.protocolManager.Stop()
	if gc.lesServer != nil {
		gc.lesServer.Stop()
	}
	gc.txPool.Stop()
	gc.miner.Stop()
	gc.eventMux.Stop()

	gc.chainDb.Close()
	close(gc.shutdownChan)

	return nil
}
