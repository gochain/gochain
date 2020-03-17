// +build integration

package cross_test

import (
	"math/big"
	"os"
	"testing"

	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/consensus/clique"
	"github.com/gochain/gochain/v3/cross"
	"github.com/gochain/gochain/v3/crypto"
	"github.com/gochain/gochain/v3/goclient"
	"github.com/gochain/gochain/v3/rpc"
)

var integrationPrivateKeyHex = os.Getenv("WEB3_PRIVATE_KEY")

// Requires WEB3_PRIVATE_KEY and a light ropsten node on http://localhost:8545
func TestIntegration(t *testing.T) {
	userKey, err := crypto.ParsePrivateKeyHex(integrationPrivateKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cross.TestnetConfig[0]
	cfg.ExternalURL = "http://localhost:8545"
	c, close := dialConfig(t, cfg)
	defer close()
	testCrossConfirmations(t, userKey.PrivateKey(), new(big.Int).SetUint64(1e9))(c)
}

func dialConfig(t *testing.T, cfg cross.Config) (*C, func()) {
	var c C
	var err error
	c.InRPC, err = rpc.Dial("https://testnet-rpc.gochain.io")
	if err != nil {
		t.Fatal(err)
	}
	c.ExRPC, err = cfg.DialRPC()
	if err != nil {
		t.Fatal(err)
	}
	c.InClient = goclient.NewClient(c.InRPC)
	c.ExClient = goclient.NewClient(c.ExRPC)
	c.InConfs, err = cross.NewConfirmations(cfg.Internal.Contract, c.InClient)
	if err != nil {
		t.Fatal(err)
	}
	c.ExConfs, err = cross.NewConfirmations(cfg.External.Contract, c.ExClient)
	if err != nil {
		t.Fatal(err)
	}
	c.CliqueAPI = func(t *testing.T, addr common.Address) *clique.API {
		t.Fatalf("unsupported for non-local integration")
		return nil
	}
	return &c, func() { c.InRPC.Close(); c.ExRPC.Close() }
}
