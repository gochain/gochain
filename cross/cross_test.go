package cross_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gochain/gochain/v3/accounts"
	"github.com/gochain/gochain/v3/accounts/abi/bind"
	"github.com/gochain/gochain/v3/accounts/keystore"
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/common/math"
	"github.com/gochain/gochain/v3/consensus/clique"
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

var oneGwei, _ = new(big.Int).SetString("1000000000", 0)

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
	CliqueAPI          func(*testing.T, common.Address) *clique.API
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
		if err := tn.gochain.StartMining(1, nil); err != nil {
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

	internal := cross.NetConfig{
		Contract:      inAddr,
		Confirmations: uint64(signerCount/2) + 1,
	}
	external := cross.NetConfig{
		Contract:      exAddr,
		Confirmations: 1,
	}
	exRPCFn := func() (*rpc.Client, error) { return exRPC, nil }

	// Spawn cross chain processing.
	for _, tn := range inNodes {
		ks := tn.node.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)

		c := cross.NewCross(internal, external, exRPCFn, ks)
		c.SetSigner(tn.coinbase)
		inRPC, err := tn.node.Attach()
		if err != nil {
			t.Fatal(err)
		}
		defer inRPC.Close()
		c.SetInternalClient(goclient.NewClient(inRPC))
		c.Start()
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
		CliqueAPI: func(t *testing.T, addr common.Address) *clique.API {
			for _, node := range inNodes {
				if node.coinbase != addr {
					continue
				}
				for _, api := range node.gochain.APIs() {
					if api.Namespace == "clique" {
						return api.Service.(*clique.API)
					}
				}
				t.Fatalf("clique api not found for node %s", addr.Hex())
			}
			t.Fatalf("node not found for addr %s", addr.Hex())
			return nil
		},
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
	if err := gochain.StartMining(1, nil); err != nil {
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
	seeds := []common.Address{crypto.PubkeyToAddress(userKey.PublicKey)}

	test := func(signers, voters int) func(t *testing.T) {
		return func(t *testing.T) {
			testFn := testCrossConfirmations(t, userKey, new(big.Int).SetUint64(2e9))
			crossTest(t, signers, voters, seeds, testFn)
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

func testCrossConfirmations(t *testing.T, userKey *ecdsa.PrivateKey, externalMinPrice *big.Int) func(*C) {
	userOpts := bind.NewKeyedTransactor(userKey)
	internalMinPrice := new(big.Int).SetUint64(2e9)
	return func(c *C) {
		t.Run("import", fixture{
			userOpts:      userOpts,
			confs:         c.InConfs,
			confsCl:       c.InClient,
			confsMinPrice: internalMinPrice,
			emitCl:        c.ExClient,
			emitMinPrice:  externalMinPrice,
		}.tests)
		t.Run("export", fixture{
			userOpts:      userOpts,
			confs:         c.ExConfs,
			confsCl:       c.ExClient,
			confsMinPrice: externalMinPrice,
			emitCl:        c.InClient,
			emitMinPrice:  internalMinPrice,
		}.tests)
	}
}

type addrSetStringer []common.Address

func (as addrSetStringer) String() string {
	var sb strings.Builder
	for i, addr := range as {
		if i != 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(addr.Hex())
	}
	return sb.String()
}

type addrMapStringer map[common.Address]struct{}

func (as addrMapStringer) String() string {
	var sb strings.Builder
	first := true
	for addr := range as {
		if !first {
			sb.WriteString(", ")
		}
		first = false
		sb.WriteString(addr.Hex())
	}
	return sb.String()
}

// TestAdminRemove votes out signers and voters, and then verifies the changes are propagated to the contracts.
func TestAdminRemove(t *testing.T) {
	const timeout = 30 * time.Second
	crossTest(t, 3, 2, nil, func(c *C) {
		signers, voters := cliqueAdmins(context.Background(), t, c.InClient)
		// Sort: voters < signers
		mVoters := make(map[common.Address]struct{})
		for _, v := range voters {
			mVoters[v] = struct{}{}
		}
		sort.Slice(signers, func(i, j int) bool {
			_, iv := mVoters[signers[i]]
			_, jv := mVoters[signers[j]]
			if iv != jv {
				return iv
			}
			return bytes.Compare(signers[i].Bytes(), signers[j].Bytes()) < 0
		})
		sort.Slice(voters, func(i, j int) bool {
			return bytes.Compare(voters[i].Bytes(), voters[j].Bytes()) < 0
		})

		t.Logf("Signers: %v", addrSetStringer(signers))
		t.Logf("Voters: %v", addrSetStringer(voters))

		t.Log("Voting out third signer...", signers[2].Hex())
		c.CliqueAPI(t, voters[0]).Propose(signers[2], false)
		c.CliqueAPI(t, voters[1]).Propose(signers[2], false)

		waitForAdmins(t, c, signers[:2], voters, timeout)

		t.Log("Voting out second voter...", voters[1].Hex())
		c.CliqueAPI(t, voters[0]).ProposeVoter(voters[1], false)
		c.CliqueAPI(t, voters[1]).ProposeVoter(voters[1], false)

		waitForAdmins(t, c, signers[:2], voters[:1], timeout)

		t.Log("Voting out second signer...", signers[1].Hex())
		c.CliqueAPI(t, voters[0]).Propose(signers[1], false)

		waitForAdmins(t, c, signers[:1], voters[:1], timeout)
	})
}

// cliqueAdmins gets the latest clique signer and voter sets.
func cliqueAdmins(ctx context.Context, t *testing.T, client *goclient.Client) ([]common.Address, []common.Address) {
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

// confsAdmins gets the latest signer and voter sets from the confs contract.
func confsAdmins(t *testing.T, session cross.ConfirmationsSession) (map[common.Address]struct{}, map[common.Address]struct{}) {
	signers, err := cross.ConfirmationsSigners(session)
	if err != nil {
		t.Fatalf("failed to get confs signers: %v", err)
	}
	voters, err := cross.ConfirmationsVoters(session)
	if err != nil {
		t.Fatalf("failed to get confs voters: %v", err)
	}
	return signers, voters
}

// waitForAdmins waits up to timeout for each of clique, internal confs, and external confs to
// sync with signers and voters.
func waitForAdmins(t *testing.T, c *C, signers, voters []common.Address, timeout time.Duration) {
	t.Helper()
	waitForClique(t, c.InClient, signers, voters, timeout)
	waitForConfsAdmins(t, c.InConfs, signers, voters, timeout, "internal")
	waitForConfsAdmins(t, c.ExConfs, signers, voters, timeout, "external")
}

// waitForClique polls the clique state until it matches signers and voters, or
// fails the test after timeout.
func waitForClique(t *testing.T, client *goclient.Client, signers, voters []common.Address, timeout time.Duration) {
	t.Helper()
	mSigners := map[common.Address]struct{}{}
	for _, s := range signers {
		mSigners[s] = struct{}{}
	}
	mVoters := map[common.Address]struct{}{}
	for _, v := range voters {
		mVoters[v] = struct{}{}
	}

	ctx, _ := context.WithTimeout(context.Background(), timeout)

poll:
	for {
		clSigners, clVoters := cliqueAdmins(ctx, t, client)
		if len(clSigners) != len(signers) || len(clVoters) != len(voters) {
			if sleepCtx(ctx, time.Second) != nil {
				t.Fatal("Timed out waiting for clique state")
			}
			continue poll
		}
		for _, s := range clSigners {
			if _, ok := mSigners[s]; !ok {
				if sleepCtx(ctx, time.Second) != nil {
					t.Fatal("Timed out waiting for clique state")
				}
				continue poll
			}
		}
		for _, v := range clVoters {
			if _, ok := mVoters[v]; !ok {
				if sleepCtx(ctx, time.Second) != nil {
					t.Fatal("Timed out waiting for clique state")
				}
				continue poll
			}
		}
		// Match
		return
	}
}

// waitForConfsAdmins polls confs until it matches signers and voters, or fails the test after timeout.
func waitForConfsAdmins(t *testing.T, confs *cross.Confirmations, signers, voters []common.Address, timeout time.Duration, kind string) {
	t.Helper()
	ctx, _ := context.WithTimeout(context.Background(), timeout)

	session := cross.ConfirmationsSession{Contract: confs, CallOpts: bind.CallOpts{Context: ctx}}
poll:
	for {
		confsSigners, confsVoters := confsAdmins(t, session)
		if len(confsSigners) != len(signers) || len(confsVoters) != len(voters) {
			if sleepCtx(ctx, time.Second) != nil {
				t.Fatal("Timed out waiting for", kind, "confs admins")
			}
			continue poll
		}
		for _, s := range signers {
			if _, ok := confsSigners[s]; !ok {
				if sleepCtx(ctx, time.Second) != nil {
					t.Fatal("Timed out waiting for", kind, "confs admins")
				}
				continue poll
			}
		}
		for _, v := range voters {
			if _, ok := confsVoters[v]; !ok {
				if sleepCtx(ctx, time.Second) != nil {
					t.Fatal("Timed out waiting for", kind, "confs admins")
				}
				continue poll
			}
		}
		// Match
		return
	}
}

type fixture struct {
	userOpts                    *bind.TransactOpts
	confs                       *cross.Confirmations
	confsCl, emitCl             *goclient.Client
	confsMinPrice, emitMinPrice *big.Int

	mockContract *Test
}

func (f fixture) tests(t *testing.T) {
	f.mockContract = f.deployMockContract(t)

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

	price, err := f.emitCl.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if price.Cmp(f.emitMinPrice) < 0 {
		price.Set(f.emitMinPrice)
	}
	opts := *f.userOpts
	opts.GasPrice = price
	tx, err := f.mockContract.Emit(&opts, value, addr, num)
	if err != nil {
		t.Fatal(err)
	}
	toConfirm, l := waitForFirstLog(t, tx, f.emitCl)

	_ = confirmTestEvent(t, f.mockContract, l, value, addr, num)

	// Request confirmation of event.
	hash := cross.HashLog(l)
	logIdx := new(big.Int).SetUint64(uint64(l.Index))
	tx = f.requestConfirmation(t, toConfirm.BlockNumber, logIdx, hash)
	_, requestLog := waitForFirstLog(t, tx, f.confsCl)
	_ = confirmConfirmationRequested(t, f.confs, requestLog, toConfirm.BlockNumber, logIdx, hash)

	// Verify confirmation.
	if !f.confirmHash(t, toConfirm.BlockNumber, logIdx, hash) {
		t.Error("confirmation status is invalid")
	}
}

// confirmHash returns true if the status is confirmed or false if invalid.
// Errors or a delay of more than 1m will fail the test.
func (f *fixture) confirmHash(t *testing.T, block *big.Int, logIdx *big.Int, hash common.Hash) bool {
	// Poll for confirmation.
	timeout := time.After(time.Minute)
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
	if price.Cmp(f.confsMinPrice) < 0 {
		price.Set(f.confsMinPrice)
	}
	totalGas, err := f.confs.TotalConfirmGas(nil)
	if err != nil {
		t.Fatal(err)
	}
	reqOpts := *f.userOpts
	reqOpts.GasLimit = 3000000
	reqOpts.GasPrice = price
	reqOpts.Value = new(big.Int).Mul(totalGas, reqOpts.GasPrice)
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

	price, err := f.emitCl.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if price.Cmp(f.emitMinPrice) < 0 {
		price.Set(f.emitMinPrice)
	}
	opts := *f.userOpts
	opts.GasPrice = price
	tx, err := f.mockContract.Emit(&opts, value, addr, num)
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

func (f *fixture) deployMockContract(t *testing.T) *Test {
	price, err := f.emitCl.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if price.Cmp(f.emitMinPrice) < 0 {
		price.Set(f.emitMinPrice)
	}
	deployOpts := *f.userOpts
	deployOpts.GasLimit = 1000000
	deployOpts.GasPrice = price
	_, tx, transactor, err := DeployTest(&deployOpts, f.emitCl)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bind.WaitDeployed(context.Background(), f.emitCl, tx)
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
