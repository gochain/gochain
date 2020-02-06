package cross_test

import (
	"context"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"sync"
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
	"github.com/gochain/gochain/v3/p2p"
	"github.com/gochain/gochain/v3/p2p/discover"
	"github.com/gochain/gochain/v3/rpc"
	whisper "github.com/gochain/gochain/v3/whisper/whisperv6"
)

const testBlockPeriodSeconds = 2 // TODO 1 should be possible, but currently causes failures

func init() {
	log.Root().SetHandler(log.LvlFilterHandler(
		log.LvlWarn,
		log.StreamHandler(os.Stdout, log.TerminalFormat(false)),
	))
}

type C struct {
	InRPC, ExRPC       *rpc.Client
	InClient, ExClient *goclient.Client
	InConfs, ExConfs   *cross.Confirmations
}

type testNode struct {
	node     *node.Node
	enode    *discover.Node
	coinbase common.Address
	gochain  *eth.GoChain
}

// crossTest executes a cross chain test function.
func crossTest(t *testing.T, signerCount, voterCount int, seeds []common.Address, testFn func(c *C)) {
	if voterCount > signerCount {
		t.Fatal("voters must not exceed signers")
	}
	var genesis core.Genesis
	genesisCh := make(chan struct{}) // Closed after genesis is set.
	signerCh := make(chan idxSigner)

	inNodes := make([]*testNode, signerCount) // Available after wg is done.
	var wg sync.WaitGroup
	wg.Add(signerCount)
	for i := 0; i < signerCount; i++ {
		go func(i int) {
			defer wg.Done()
			inNodes[i] = newInternalNode(t, i, signerCh, genesisCh, &genesis)
		}(i)
	}

	// Receive signer addresses.
	signers := make([]common.Address, signerCount)
	for i := 0; i < signerCount; i++ {
		signer := <-signerCh
		signers[signer.idx] = signer.addr
	}
	seeds = append(seeds, signers...)
	// Create genesis.
	alloc := make(core.GenesisAlloc)
	for _, a := range seeds {
		alloc[a] = core.GenesisAccount{Balance: new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(10000))}
	}
	genesis = *core.LocalGenesisBlock(testBlockPeriodSeconds, signers, voterCount, alloc)
	// Notify genesis ready.
	close(genesisCh)
	// Wait until nodes created.
	wg.Wait()
	close(signerCh)

	defer func() {
		for i, tn := range inNodes {
			if err := tn.node.Stop(); err != nil {
				t.Logf("error stopping internal node %d: %v\n", i, err)
			}
		}
	}()

	// Connect peers.
	for i, tn := range inNodes {
		s := tn.node.Server()
		for j, peer := range inNodes {
			if i != j {
				s.AddPeer(peer.enode)
			}
		}
	}

	// Start signing.
	for _, tn := range inNodes {
		if err := tn.gochain.StartMining(1); err != nil {
			t.Fatal(err)
		}
	}

	exNode, exRPC := newExternalNode(t, seeds)
	defer func() {
		if err := exNode.Stop(); err != nil {
			t.Logf("error stopping external node: %v\n", err)
		}
	}()
	defer exRPC.Close()

	inRPC, err := inNodes[0].node.Attach()
	if err != nil {
		t.Fatal(err)
	}
	defer inRPC.Close()
	inClient, exClient := goclient.NewClient(inRPC), goclient.NewClient(exRPC)

	// Deploy contracts.
	ks := inNodes[0].node.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
	signer := inNodes[0].coinbase
	inAddr, inConfs := deployConfirmations(t, inClient, signer, ks)
	exAddr, exConfs := deployConfirmations(t, exClient, signer, ks)
	time.Sleep(testBlockPeriodSeconds * time.Second)

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

	// Spawn cross chain processing.
	for i := 0; i < signerCount; i++ {
		inRPC, err := inNodes[i].node.Attach()
		if err != nil {
			t.Fatal(err)
		}
		defer inRPC.Close()

		ks := inNodes[i].node.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
		signer := inNodes[i].coinbase

		c, err := cross.NewCross(cfg, inRPC, exRPC, signer, ks)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Stop()
	}

	// Wait for signers to be voted in.
	for {
		s, err := inConfs.SignersLength(nil)
		if err != nil {
			t.Fatal(err)
		}
		si := int(s.Int64())
		if si == signerCount {
			break
		}
		time.Sleep(testBlockPeriodSeconds * time.Second)
	}
	for {
		s, err := exConfs.SignersLength(nil)
		if err != nil {
			t.Fatal(err)
		}
		si := int(s.Int64())
		if si == signerCount {
			break
		}
		time.Sleep(testBlockPeriodSeconds * time.Second)
	}

	testFn(&C{
		InRPC: inRPC, ExRPC: exRPC,
		InClient: inClient, ExClient: exClient,
		InConfs: inConfs, ExConfs: exConfs,
	})
}

type idxSigner struct {
	idx  int
	addr common.Address
}

// newInternalNode starts a new internal test node.
func newInternalNode(t *testing.T, idx int, signerCh chan idxSigner, genesisCh chan struct{}, genesis *core.Genesis) *testNode {
	n, err := node.New(&node.Config{
		Name: "cross-test-internal-" + strconv.Itoa(idx),
		P2P: p2p.Config{
			ListenAddr:  "0.0.0.0:0",
			NoDiscovery: true,
			MaxPeers:    25,
		},
	})
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
	// Send coinbase to be included in genesis.
	signerCh <- idxSigner{idx, acct.Address}
	// Wait until genesis is ready.
	<-genesisCh

	tn := &testNode{
		node:     n,
		coinbase: acct.Address,
	}
	if err := n.Register(func(sctx *node.ServiceContext) (node.Service, error) {
		var err error
		tn.gochain, err = eth.New(sctx, &eth.Config{
			Etherbase:     acct.Address,
			SyncMode:      downloader.FullSync,
			Genesis:       genesis,
			NetworkId:     genesis.Config.ChainId.Uint64(),
			MinerGasPrice: big.NewInt(1),
			TxPool:        core.DefaultTxPoolConfig,
		})
		return tn.gochain, err
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.Register(func(n *node.ServiceContext) (node.Service, error) {
		return whisper.New(nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}

	tn.enode = n.Server().Self()

	return tn
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
			Genesis:       core.LocalGenesisBlock(testBlockPeriodSeconds, []common.Address{acct.Address}, 0, alloc),
			MinerGasPrice: big.NewInt(1),
			TxPool:        core.DefaultTxPoolConfig,
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
	test := func(signers, voters int) func(t *testing.T) {
		return func(t *testing.T) {
			crossTest(t, signers, voters, []common.Address{userOpts.From}, func(c *C) {
				t.Run("import", fixture{
					userOpts: userOpts,
					confs:    c.InConfs,
					confsCl:  c.InClient,
					emitCl:   c.ExClient,
				}.tests)
				t.Run("export", fixture{
					userOpts: userOpts,
					confs:    c.ExConfs,
					confsCl:  c.ExClient,
					emitCl:   c.InClient,
				}.tests)
			})
		}
	}

	t.Run("1/1", test(1, 1))
	//TODO run some of these combos in another noop?
	//t.Run("2/1", test(2, 1))
	//t.Run("2/2", test(2, 2))
	//t.Run("3/2", test(3, 1))
	//t.Run("3/3", test(3, 3))
	//t.Run("5/1", test(5, 1))
	//t.Run("5/2", test(5, 2))
	//t.Run("15/1", test(15, 1))
	//t.Run("15/15", test(15, 15))
	//t.Run("50/1", test(50, 1))
	//t.Run("50/50", test(50, 50))
}

func TestRemove(t *testing.T) {
	userKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	userOpts := bind.NewKeyedTransactor(userKey)
	//TODO drop seed?
	crossTest(t, 3, 2, []common.Address{userOpts.From}, func(c *C) {
		signers, voters := cliqueAdmin(context.Background(), t, c.InClient)

		//TODO vote out 3rd signer
		//TODO confirm processed
		waitForClique(t, 2, 2, time.Minute)

		//TODO poll until 2/2 in both places (or timeout)
		waitForConfsAdmin(t, c.InConfs, 2, 2, time.Minute)
		waitForConfsAdmin(t, c.ExConfs, 2, 2, time.Minute)

		//TODO vote out 2nd voter
		//TODO confirm processed
		waitForClique(t, 1, 1, time.Minute)

		//TODO poll until 1/1 in both places
		waitForConfsAdmin(t, c.InConfs, 1, 1, time.Minute)
		waitForConfsAdmin(t, c.ExConfs, 1, 1, time.Minute)
	})
}

func cliqueAdmin(ctx context.Context, t *testing.T, client *goclient.Client) ([]common.Address, []common.Address) {
	signers, err := client.SignersAt(ctx, nil)
	if err != nil {
		t.Fatalf("failed to get signers: %v", err)
	}
	voters, err := client.VotersAt(ctx, nil)
	if err != nil {
		t.Fatalf("failed to get voters: %v", err)
	}
	return signers, voters
}

func confsAdmin(ctx context.Context, t *testing.T, confs *cross.Confirmations) (map[common.Address]struct{}, map[common.Address]struct{}) {
	signers, err := cross.ConfirmationsSigners(ctx, nil, confs)
	if err != nil {
		t.Fatalf("failed to get confs signers: %v", err)
	}
	voters, err := cross.ConfirmationsVoters(ctx, nil, confs)
	if err != nil {
		t.Fatalf("failed to get confs voters: %v", err)
	}
	return signers, voters
}

func waitForClique(t *testing.T, signers, voters []common.Address, timeout time.Duration) {
	//TODO polling loop
	//TODO doc
}

func waitForConfsAdmin(t *testing.T, confs *cross.Confirmations, signers, voters []common.Address, timeout time.Duration) {
	ctx, _ := context.WithTimeout(context.Background(), timeout)

poll:
	for {
		confsSigners, confsVoters := confsAdmin(ctx, t, confs)
		if len(confsSigners) != len(signers) || len(confsVoters) != len(voters) {
			if sleepCtx(ctx, time.Second) != nil {
				return
			}
			continue poll
		}
		for _, s := range signers {
			if _, ok := confsSigners[s]; !ok {
				if sleepCtx(ctx, time.Second) != nil {
					return
				}
				continue poll
			}
		}
		for _, v := range voters {
			if _, ok := confsVoters[v]; !ok {
				if sleepCtx(ctx, time.Second) != nil {
					return
				}
				continue poll
			}
		}
	}
}

type fixture struct {
	userOpts        *bind.TransactOpts
	confs           *cross.Confirmations
	confsCl, emitCl *goclient.Client

	mockContract *Test
}

func (f fixture) tests(t *testing.T) {
	f.mockContract = deployMockContract(t, f.userOpts, f.emitCl)

	t.Run("confirm", f.testConfirm)
	t.Run("invalid", f.testInvalid)
	//TODO t.Run("gasPrice", f.testGasPrice)
	//TODO t.Run("concurrent", f.testConcurrent)
}

// testConfirm verifies confirmation of an event.
func (f *fixture) testConfirm(t *testing.T) {
	const value = "test"
	addr := f.userOpts.From
	num := big.NewInt(99)

	tx, err := f.mockContract.Emit(f.userOpts, value, addr, num)
	if err != nil {
		t.Fatal(err)
	}
	toConfirm, l := waitForFirstLog(t, tx, f.emitCl)

	_ = confirmTestEvent(t, f.mockContract, l, value, addr, num)

	// Request confirmation of event.
	hash := cross.HashLog(l)
	logIdx := big.NewInt(0)
	tx = f.requestConfirmation(t, toConfirm.BlockNumber, logIdx, hash)
	_, requestLog := waitForFirstLog(t, tx, f.confsCl)
	_ = confirmConfirmationRequested(t, f.confs, requestLog, toConfirm.BlockNumber, logIdx, hash)

	// Verify confirmation.
	if !f.confirmHash(t, toConfirm.BlockNumber, logIdx, hash) {
		t.Error("confirmation status is invalid")
	}
}

//TODO doc returns true if confirmed, or false if invalid.
func (f *fixture) confirmHash(t *testing.T, block *big.Int, logIdx *big.Int, hash common.Hash) bool {
	// Poll for confirmation.
	timeout := time.After(10 * time.Second)
	for {
		status, err := f.confs.Status(nil, block, logIdx, hash)
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
			return false
		case cross.StatusConfirmed:
			return true
		default:
			t.Fatalf("unrecognized status: %d", status)
		}
	}
}

// requestConfirmation submits a tx to request confirmation of an event and returns the tx.
func (f *fixture) requestConfirmation(t *testing.T, blockNum *big.Int, logIndex *big.Int, hash common.Hash) *types.Transaction {
	price, err := f.confsCl.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	totalGas, err := f.confs.TotalConfirmGas(nil)
	if err != nil {
		t.Fatal(err)
	}
	reqOpts := *f.userOpts
	reqOpts.GasLimit = 3000000
	reqOpts.GasPrice = price
	reqOpts.Value = new(big.Int).Mul(totalGas, price)
	tx, err := f.confs.Request(&reqOpts, blockNum, logIndex, hash)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// testInvalid verifies invalid confirmation of invalid events.
func (f *fixture) testInvalid(t *testing.T) {
	const value = "test"
	addr := f.userOpts.From
	num := big.NewInt(99)

	tx, err := f.mockContract.Emit(f.userOpts, value, addr, num)
	if err != nil {
		t.Fatal(err)
	}
	toConfirm, l := waitForFirstLog(t, tx, f.emitCl)
	block := toConfirm.BlockNumber
	logIdx := big.NewInt(0)

	_ = confirmTestEvent(t, f.mockContract, l, value, addr, num)

	// Returns a test for a block/logIdx/log combination.
	test := func(block, logIdx *big.Int, log *types.Log) func(t *testing.T) {
		hash := cross.HashLog(log)
		return func(t *testing.T) {
			tx = f.requestConfirmation(t, block, logIdx, hash)
			_, requestLog := waitForFirstLog(t, tx, f.confsCl)
			_ = confirmConfirmationRequested(t, f.confs, requestLog, block, logIdx, hash)

			if f.confirmHash(t, block, logIdx, hash) {
				t.Error("unexpected confirmation of invalid log")
			}
		}
	}

	// Try to confirm invalid variations.
	t.Run("block", test(new(big.Int).Add(block, big.NewInt(1)), logIdx, l))
	t.Run("log-idx", test(block, big.NewInt(10), l))
	t.Run("empty", test(block, logIdx, &types.Log{}))
	t.Run("addr", test(block, logIdx, &types.Log{
		Address: common.BigToAddress(big.NewInt(rand.Int63())),
		Topics:  l.Topics,
		Data:    l.Data,
	}))
	t.Run("single-topic", test(block, logIdx, &types.Log{
		Address: l.Address,
		Topics:  l.Topics[:1], // single topic
		Data:    l.Data,
	}))
	t.Run("skip-topic", test(block, logIdx, &types.Log{
		Address: l.Address,
		Topics:  l.Topics[1:], // skip first topic
		Data:    l.Data,
	}))
	t.Run("single-data", test(block, logIdx, &types.Log{
		Address: l.Address,
		Topics:  l.Topics,
		Data:    l.Data[:1], // single data
	}))
	t.Run("skip-data", test(block, logIdx, &types.Log{
		Address: l.Address,
		Topics:  l.Topics,
		Data:    l.Data[1:], // skipped data
	}))
}

func deployMockContract(t *testing.T, userOpts *bind.TransactOpts, client *goclient.Client) *Test {
	deployOpts := *userOpts
	deployOpts.GasLimit = 1000000
	_, tx, transactor, err := DeployTest(&deployOpts, client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bind.WaitDeployed(context.Background(), client, tx)
	if err != nil {
		t.Fatal(err)
	}
	return transactor
}

// waitForFirstLog waits for the tx to be mined, then confirms it was successful and produced a log which was not
// removed, and returns the receipt and log.
func waitForFirstLog(t *testing.T, tx *types.Transaction, client *goclient.Client) (*types.Receipt, *types.Log) {
	toConfirm, err := bind.WaitMined(context.Background(), client, tx)
	if err != nil {
		t.Fatal(err)
	}
	if toConfirm.Status != types.ReceiptStatusSuccessful {
		t.Fatal("tx failed")
	}
	if len(toConfirm.Logs) == 0 {
		t.Fatal("no logs")
	}
	l := toConfirm.Logs[0]
	if l.Removed {
		t.Fatal("log removed")
	}
	return toConfirm, l
}

// confirmTestEvent parses a TestEvent from the log and confirms it is consistent with the given value/addr/num.
func confirmTestEvent(t *testing.T, mockContract *Test, l *types.Log, value string, addr common.Address, num *big.Int) *TestTestEvent {
	ev, err := mockContract.ParseTestEvent(*l)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Addr != addr {
		t.Fatalf("expected event addr %q but got %q", addr.Hex(), ev.Addr.Hex())
	}
	if ev.Number.Cmp(num) != 0 {
		t.Fatalf("expected event number %q but got %q", num, ev.Number)
	}
	hash := crypto.Keccak256Hash([]byte(value))
	if ev.Value != hash {
		t.Fatalf("expected event value hash %q but got %q", hash, ev.Value)
	}
	return ev
}

// confirmConfirmationRequested parses a ConfirmationRequested event from the log and confirms it is consistent with the given block/logIdx/hash.
func confirmConfirmationRequested(t *testing.T, confs *cross.Confirmations, l *types.Log, block *big.Int, logIdx *big.Int, hash common.Hash) *cross.ConfirmationsConfirmationRequested {
	cr, err := confs.ParseConfirmationRequested(*l)
	if err != nil {
		t.Fatal(err)
	}
	if cr.BlockNum.Cmp(block) != 0 {
		t.Fatalf("expected block %s but got %s", block.String(), cr.BlockNum.String())
	}
	if cr.LogIndex.Cmp(logIdx) != 0 {
		t.Fatalf("expected log index %d but got %s", logIdx, cr.LogIndex.String())
	}
	if cr.EventHash != hash {
		t.Fatalf("expected event hash %s but got %s", hash.Hex(), common.Hash(cr.EventHash).Hex())
	}
	return cr
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
