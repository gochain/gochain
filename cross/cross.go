package cross

import (
	"context"
	"math/big"
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

// Config holds options for a cross chain configuration.
type Config struct {
	Internal    NetConfig `toml:",omitempty"`
	External    NetConfig `toml:",omitempty"`
	ExternalURL string    `toml:",omitempty"`
}

func (c *Config) DialRPC() (*rpc.Client, error) {
	return rpc.Dial(c.ExternalURL)
}

type NetConfig struct {
	Contract      common.Address `toml:",omitempty"` // Address of the Confirmations contract.
	Confirmations uint64         `toml:",omitempty"` // Number of block confirmations to wait.
	MinGasPrice   *big.Int       `toml:",omitempty"`
}

const defaultsConfs = 30

// DefaultConfig bridges the GoChain and Ethereum mainnets.
var DefaultConfig = []Config{
	{
		Internal: NetConfig{
			Contract:      common.HexToAddress("0xTODO"),
			Confirmations: defaultsConfs,
			MinGasPrice:   new(big.Int).SetUint64(2e9),
		},
		External: NetConfig{
			Contract:      common.HexToAddress("0xTODO"),
			Confirmations: defaultsConfs,
			MinGasPrice:   new(big.Int).SetUint64(1e9), //TODO
		},
		ExternalURL: "http://ethereum:8545",
	},
}

// TestnetConfig bridges the GoChain testnet with the Ethereum Ropsten testnet.
var TestnetConfig = []Config{
	{
		Internal: NetConfig{
			Contract:      common.HexToAddress("0x92B7329C1066be06707ea811346C9eEC56706A45"),
			Confirmations: 5,
			MinGasPrice:   new(big.Int).SetUint64(2e9),
		},
		External: NetConfig{
			Contract:      common.HexToAddress("0x2acfcce7cc4c59e263af6a0d92a91520b7c7eac8"),
			Confirmations: 3,
			MinGasPrice:   new(big.Int).SetUint64(1e9),
		},
		ExternalURL: "http://ropsten:8545",
	},
}

func (config *NetConfig) sanitized() NetConfig {
	c := *config
	if c.Confirmations == 0 {
		c.Confirmations = defaultsConfs
	}
	if c.MinGasPrice == nil {
		c.MinGasPrice = big.NewInt(1)
	}
	return c
}

type Cross struct {
	wg     sync.WaitGroup
	cancel func()

	in, ex      proc
	setInClient func(*goclient.Client)
}

// NewCross creates a cross chain processor between the inRPC and exRPC networks.
func NewCross(inCfg, exCfg NetConfig, exRPC func() (*rpc.Client, error), ks *keystore.KeyStore) *Cross {
	inCfg = inCfg.sanitized()
	exCfg = exCfg.sanitized()

	exCl := &cachedClientFn{fn: exRPC}
	inCl := &cachedClientSettable{}

	return &Cross{
		setInClient: inCl.set,
		in: proc{
			logPre:     "cross/internal: ",
			confsCfg:   inCfg,
			emitCfg:    exCfg,
			keystore:   ks,
			internalCl: inCl,
			confsCl:    inCl,
			emitCl:     exCl,
		},
		ex: proc{
			logPre:     "cross/external: ",
			confsCfg:   exCfg,
			emitCfg:    inCfg,
			keystore:   ks,
			internalCl: inCl,
			confsCl:    exCl,
			emitCl:     inCl,
		},
	}
}

func (c *Cross) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.wg.Add(2)
	go c.in.run(ctx, c.wg.Done)
	go c.ex.run(ctx, c.wg.Done)
	log.Info("cross: Started")
}

func (c *Cross) Stop() {
	log.Debug("cross: Stopping")
	c.cancel()
	c.wg.Wait()
	log.Info("cross: Stopped")
}

// SetInternalClient sets the internal rpc client.
func (c *Cross) SetInternalClient(internal *goclient.Client) {
	c.setInClient(internal)
}

func (c *Cross) SetSigner(addr common.Address) {
	c.in.signer.Store(addr)
	c.ex.signer.Store(addr)
	log.Debug("cross: Signer address changed", "signer", addr.Hex())
}
