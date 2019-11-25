package cross_test

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/gochain/gochain/v3/accounts"
	"github.com/gochain/gochain/v3/accounts/abi/bind"
	"github.com/gochain/gochain/v3/accounts/keystore"
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/common/math"
	"github.com/gochain/gochain/v3/core"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/cross"
	"github.com/gochain/gochain/v3/crypto"
	"github.com/gochain/gochain/v3/eth"
	"github.com/gochain/gochain/v3/eth/downloader"
	"github.com/gochain/gochain/v3/goclient"
	"github.com/gochain/gochain/v3/log"
	"github.com/gochain/gochain/v3/node"
	"github.com/gochain/gochain/v3/rpc"
)

func init() {
	log.Root().SetHandler(log.LvlFilterHandler(
		log.LvlInfo,
		log.StreamHandler(os.Stdout, log.TerminalFormat(false)),
	))
}

type C struct {
	InRPC, ExRPC       *rpc.Client
	InClient, ExClient *goclient.Client
	InConfs, ExConfs   *cross.Confirmations
}

// crossTest executes a cross chain test function.
//TODO support more signers
func crossTest(t *testing.T, seeds []common.Address, testFn func(c *C)) {
	//TODO create genesis with all signers

	//TODO spawn one node per signer
	inNode, inRPC, signer := newInternalNode(t, seeds)
	defer inNode.Stop()
	defer inRPC.Close()

	exNode, exRPC := newExternalNode(t, append(seeds, signer))
	defer exNode.Stop()
	defer exRPC.Close()

	inClient, exClient := goclient.NewClient(inRPC), goclient.NewClient(exRPC)

	ks := inNode.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
	inAddr, inConfs := deployConfirmations(t, inClient, signer, ks)
	//TODO this second call uses the cached address from the first call, which is on the other chain
	exAddr, exConfs := deployConfirmations(t, exClient, signer, ks)

	cfg := cross.Config{
		Internal: cross.NetConfig{
			Contract:      inAddr,
			Confirmations: 1,
		},
		External: cross.NetConfig{
			Contract:      exAddr,
			Confirmations: 1,
		},
	}

	//TODO spawn for each signer
	c, err := cross.NewCross(cfg, inRPC, exRPC, signer, ks)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	testFn(&C{
		InRPC: inRPC, ExRPC: exRPC,
		InClient: inClient, ExClient: exClient,
		InConfs: inConfs, ExConfs: exConfs,
	})
}

// newInternalNode starts a new internal test node with funds allocated to seeds accounts.
func newInternalNode(t *testing.T, seeds []common.Address) (*node.Node, *rpc.Client, common.Address) {
	n, err := node.New(&node.Config{Name: "cross-test-internal"})
	if err != nil {
		t.Fatal(err)
	}
	ks := n.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
	acct, err := ks.NewAccount("")
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Unlock(acct, ""); err != nil {
		t.Fatal(err)
	}
	if err := n.Register(func(sctx *node.ServiceContext) (node.Service, error) {
		alloc := make(core.GenesisAlloc)
		for _, a := range append(seeds, acct.Address) {
			alloc[a] = core.GenesisAccount{Balance: new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(10000))}
		}
		cfg := &eth.Config{
			Etherbase:     acct.Address,
			SyncMode:      downloader.FullSync,
			Genesis:       core.LocalGenesisBlock(2, acct.Address, alloc),
			MinerGasPrice: big.NewInt(1),
		}
		cfg.NetworkId = cfg.Genesis.Config.ChainId.Uint64()
		return eth.New(sctx, cfg)
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}

	var gochain *eth.GoChain
	if err := n.Service(&gochain); err != nil {
		t.Fatal(err)
	}
	if err := gochain.StartMining(1); err != nil {
		t.Fatal(err)
	}

	client, err := n.Attach()
	if err != nil {
		t.Fatal(err)
	}
	return n, client, acct.Address
}

// newExternalNode starts a new external test node with funds allocated to seeds accounts.
func newExternalNode(t *testing.T, seeds []common.Address) (*node.Node, *rpc.Client) {
	n, err := node.New(&node.Config{Name: "cross-test-external"})
	if err != nil {
		t.Fatal(err)
	}
	ks := n.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
	acct, err := ks.NewAccount("")
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Unlock(acct, ""); err != nil {
		t.Fatal(err)
	}
	if err := n.Register(func(sctx *node.ServiceContext) (node.Service, error) {
		alloc := make(core.GenesisAlloc)
		for _, a := range seeds {
			alloc[a] = core.GenesisAccount{Balance: new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(10000))}
		}
		cfg := &eth.Config{
			Etherbase:     acct.Address,
			SyncMode:      downloader.FullSync,
			Genesis:       core.LocalGenesisBlock(2, acct.Address, alloc),
			MinerGasPrice: big.NewInt(1),
		}
		cfg.NetworkId = cfg.Genesis.Config.ChainId.Uint64()
		return eth.New(sctx, cfg)
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}

	var gochain *eth.GoChain
	if err := n.Service(&gochain); err != nil {
		t.Fatal(err)
	}
	if err := gochain.StartMining(1); err != nil {
		t.Fatal(err)
	}

	client, err := n.Attach()
	if err != nil {
		t.Fatal(err)
	}
	return n, client
}

func deployConfirmations(t *testing.T, client *goclient.Client, signer common.Address, ks *keystore.KeyStore) (common.Address, *cross.Confirmations) {
	opts, err := bind.NewKeyStoreTransactor(ks, accounts.Account{Address: signer})
	if err != nil {
		t.Fatal(err)
	}
	opts.GasLimit = 4000000
	_, tx, confs, err := cross.DeployConfirmations(opts, client, signer)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := bind.WaitDeployed(context.Background(), client, tx)
	if err != nil {
		t.Fatal(err)
	}

	return addr, confs
}

func TestCross_confirmations(t *testing.T) {
	userKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	userOpts := bind.NewKeyedTransactor(userKey)
	crossTest(t, []common.Address{userOpts.From}, func(c *C) {
		t.Run("import", func(t *testing.T) {
			testConfirm(t, userOpts, c.InClient, c.ExClient, c.InConfs)
		})
		t.Run("export", func(t *testing.T) {
			testConfirm(t, userOpts, c.ExClient, c.InClient, c.ExConfs)
		})
		//TODO invalid
		//TODO unusual gas price
		//TODO multiple signers
		//TODO concurrent requests
	})
}

func testConfirm(t *testing.T, userOpts *bind.TransactOpts, confsCl, emitCl *goclient.Client, confs *cross.Confirmations) {
	// Deploy a test contract.
	deployOpts := *userOpts
	deployOpts.GasLimit = 1000000
	_, tx, ethTest, err := DeployTest(&deployOpts, emitCl)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bind.WaitDeployed(context.Background(), emitCl, tx)
	if err != nil {
		t.Fatal(err)
	}

	// Emit an event.
	tx, err = ethTest.Emit(userOpts, "test", userOpts.From, big.NewInt(99))
	if err != nil {
		t.Fatal(err)
	}
	toConfirm, err := bind.WaitMined(context.Background(), emitCl, tx)
	if err != nil {
		t.Fatal(err)
	}
	if toConfirm.Status != types.ReceiptStatusSuccessful {
		t.Fatal("tx failed")
	}
	if toConfirm == nil || len(toConfirm.Logs) < 1 {
		t.Fatal(toConfirm)
	}

	// Confirm event.
	l := *toConfirm.Logs[0]
	if l.Removed {
		t.Fatal("log removed")
	}
	ev, err := ethTest.ParseTestEvent(l)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Addr != userOpts.From {
		t.Fatalf("expected event addr %q but got %q", userOpts.From.Hex(), ev.Addr.Hex())
	}
	if ev.Number.Cmp(big.NewInt(99)) != 0 {
		t.Fatalf("expected event number %q but got %q", big.NewInt(99), ev.Number)
	}
	if hash := crypto.Keccak256Hash([]byte("test")); ev.Value != hash {
		t.Fatalf("expected event value hash %q but got %q", hash, ev.Value)
	}

	// Request confirmation of event.
	userOpts.GasLimit = 3000000
	l = *toConfirm.Logs[0]
	if l.Removed {
		t.Fatal("log removed")
	}
	hash := cross.HashLog(&l)
	price, err := confsCl.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	totalGas, err := confs.TotalConfirmGas(nil)
	if err != nil {
		t.Fatal(err)
	}
	reqOpts := *userOpts
	reqOpts.GasPrice = price
	reqOpts.Value = new(big.Int).Mul(totalGas, price)
	tx, err = confs.Request(&reqOpts, toConfirm.BlockNumber, big.NewInt(0), hash)
	if err != nil {
		t.Fatal(err)
	}
	r, err := bind.WaitMined(context.Background(), confsCl, tx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != types.ReceiptStatusSuccessful {
		t.Fatal("tx failed")
	}
	if len(r.Logs) != 1 {
		t.Fatalf("expected 1 log but got %d", len(r.Logs))
	}
	cr, err := confs.ParseConfirmationRequested(*r.Logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if cr.BlockNum.Cmp(toConfirm.BlockNumber) != 0 {
		t.Fatalf("expected block %s but got %s", toConfirm.BlockNumber.String(), cr.BlockNum.String())
	}
	if cr.LogIndex.Uint64() != 0 {
		t.Fatalf("expected log index 0 but got %s", cr.LogIndex.String())
	}
	if cr.EventHash != hash {
		t.Fatalf("expected event hash %s but got %s", hash.Hex(), common.Hash(cr.EventHash).Hex())
	}

	// Poll for confirmation.
	timeout := time.After(10 * time.Second)
poll:
	for {
		status, err := confs.Status(nil, toConfirm.BlockNumber, big.NewInt(0), hash)
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case cross.StatusNone:
			t.Fatal("confirmation status is none")
		case cross.StatusPending:
			select {
			case <-timeout:
				t.Fatal("timed out waiting for request confirmation")
			case <-time.After(time.Second / 2):
				continue
			}
		case cross.StatusInvalid:
			t.Error("confirmation status is invalid")
		case cross.StatusConfirmed:
			// Done.
			break poll
		default:
			t.Fatalf("unrecognized status: %d", status)
		}
	}
}
