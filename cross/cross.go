package cross

import (
	"context"
	"sync"

	"github.com/gochain/gochain/v3/accounts/keystore"
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/goclient"
	"github.com/gochain/gochain/v3/log"
	"github.com/gochain/gochain/v3/rpc"
)

const (
	StatusNone = iota
	StatusPending
	StatusInvalid
	StatusConfirmed
)

var (
	//TODO fill in or remove
	ContractGOMainnet  = common.HexToAddress("0x")
	ContractGOTestnet  = common.HexToAddress("0x")
	ContractETHMainnet = common.HexToAddress("0x")
	ContractETHTestnet = common.HexToAddress("0x")
)

//TODO wite to --cross.* flags
//TODO need a default for mainnet signer and --testnet signer, otherwise custom, should support --local as well?
type Config struct {
	Internal NetConfig `toml:",omitempty"`
	External NetConfig `toml:",omitempty"`
}

type NetConfig struct {
	Contract      common.Address `toml:",omitempty"` // Address of the Confirmations contract.
	Confirmations uint64         `toml:",omitempty"` // Number of block confirmations to wait.
}

//TODO local rpc to (in-mem) eth light node?
var DefaultConfig = Config{
	Internal: NetConfig{
		Contract:      ContractGOMainnet,
		Confirmations: 30,
	},
	External: NetConfig{
		Contract:      ContractETHMainnet,
		Confirmations: 30,
	},
}

//TODO https://ropsten-rpc.linkpool.io/
var TestnetConfig = Config{
	Internal: NetConfig{
		Contract:      ContractGOTestnet,
		Confirmations: 5,
	},
	External: NetConfig{
		Contract:      ContractETHTestnet,
		Confirmations: 5,
	},
}

func (config *Config) sanitize() Config {
	c := *config
	if c.Internal.Confirmations == 0 {
		c.Internal.Confirmations = DefaultConfig.Internal.Confirmations
	}
	if c.External.Confirmations == 0 {
		c.External.Confirmations = DefaultConfig.External.Confirmations
	}
	return c
}

type Cross struct {
	wg     sync.WaitGroup
	cancel func()
}

// NewCross starts cross chain processing between the inRPC and exRPC networks.
//TODO when to launch? wait during sync? or handle that internally? (e.g. just consider time sufficient?)
func NewCross(config Config, inRPC, exRPC *rpc.Client, signer common.Address, ks *keystore.KeyStore) (*Cross, error) {
	config = (&config).sanitize()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Cross{
		cancel: cancel,
	}

	inClient := goclient.NewClient(inRPC)
	exClient := goclient.NewClient(exRPC)

	in := proc{
		logPre:     "cross/internal: ",
		confsCfg:   config.Internal,
		emitCfg:    config.External,
		signer:     signer,
		keystore:   ks,
		internalCl: inClient,
		confsCl:    inClient,
		emitCl:     exClient,
	}

	ex := proc{
		logPre:     "cross/external: ",
		confsCfg:   config.External,
		emitCfg:    config.Internal,
		signer:     signer,
		keystore:   ks,
		internalCl: inClient,
		confsCl:    exClient,
		emitCl:     inClient,
	}

	c.wg.Add(2)
	go in.run(ctx, c.wg.Done)
	go ex.run(ctx, c.wg.Done)

	log.Info("cross: Started")

	return c, nil
}

func (c *Cross) Stop() {
	log.Debug("cross: Stopping")
	c.cancel()
	c.wg.Wait()
	log.Info("cross: Stopped")
}
