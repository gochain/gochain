package cross

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gochain/gochain/v3"
	"github.com/gochain/gochain/v3/accounts"
	"github.com/gochain/gochain/v3/accounts/abi/bind"
	"github.com/gochain/gochain/v3/accounts/keystore"
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/consensus/clique"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/goclient"
	"github.com/gochain/gochain/v3/log"
	"github.com/gochain/gochain/v3/rpc"
)

// proc manages a Confirmations contract by processing confirmation requests
// and handling administrative duties.
//
// If the node is not a signer/voter on the Confirmations contract, then it will
// wait to be voted in.
//
// For voters, the first priority is to manage the voter and signer set in Auth.
//  1. Vote out any contract voter who is no longer a voter on-chain
//  2. Vote in any chain voter who is not listed on-contract
//  3. Vote out any contract signer who is no longer a signer on-chain
//  4. Vote in any chain signer who is not listed on-contract
// Voters can respond to the chain state very quickly, since it is already delayed
// by a majority vote.
// Only one vote can take place at a time, so a voter waits for a pending vote
// to complete before voting again, and votes must be cast in the same order by
// everyone (increasing).
//
// The second priority for voters, and the only for signers, is to respond to
// event confirmation requests, by voting valid or invalid after enough blocks
// have been confirmed. These votes have no sequential restriction, and
// can happen in any order and even simultaneously.
type proc struct {
	logPre            string
	confsCfg, emitCfg NetConfig
	keystore          *keystore.KeyStore
	internalCl        cachedClient // Signing network - source of truth for voter/signer set.
	confsCl           cachedClient // Confirming network.
	emitCl            cachedClient // Network emitting events to confirm.

	signer           atomic.Value // common.Address
	confs            *Confirmations
	totalConfirmGas  uint64
	isContractSigner bool
	confsConfNum     uint64
	emitConfNum      uint64
	reqs             []ConfirmationRequest
}

// run executes the main process, alternating between
// the internal and external chain each tick.
func (p *proc) run(ctx context.Context, done func()) {
	defer done()

	t := time.NewTicker(time.Second)
	defer t.Stop()
	confs := true

	for {
		select {
		case <-ctx.Done():
			return

		case <-t.C:
			if confs = !confs; confs {
				if !p.latestConfs(ctx) {
					return
				}
			} else {
				if !p.latestEmit(ctx) {
					return
				}
			}
		}
	}
}
func (p *proc) latestConfs(ctx context.Context) bool {
	cl, err := p.confsCl.get(ctx)
	if err != nil {
		return false
	}
	latest, err := cl.LatestBlockNumber(ctx)
	if err != nil {
		log.Error(p.logPre+"Failed to get latest confs head", "err", err)
		return true
	}
	if l := latest.Uint64(); l > p.confsConfNum+p.confsCfg.Confirmations {
		conf := new(big.Int).SetUint64(l - p.confsCfg.Confirmations)
		err := p.newConfirmedHead(ctx, conf)
		if err != nil {
			log.Error(p.logPre+"Failed to process new confirmed confs head", "num", conf, "err", err)
			return true
		}
	}
	return true
}

func (p *proc) latestEmit(ctx context.Context) bool {
	cl, err := p.emitCl.get(ctx)
	if err != nil {
		return false
	}
	latest, err := cl.LatestBlockNumber(ctx)
	if err != nil {
		log.Error(p.logPre+"Failed to get latest emit head", "err", err)
		return true
	}
	if l := latest.Uint64(); l-p.emitConfNum > p.emitCfg.Confirmations {
		p.emitConfNum = l - p.emitCfg.Confirmations
		signer := p.signer.Load().(common.Address)
		err := p.confirmRequests(ctx, signer)
		if err != nil {
			log.Error(p.logPre+"Failed to process new confirmed emit head", "num", p.emitConfNum, "err", err)
			return true
		}
	}
	return true
}

// newConfirmedHead processes the new confirmed header at confirmedNum, if possible.
func (p *proc) newConfirmedHead(ctx context.Context, confsConfNum *big.Int) error {
	if p.confs == nil {
		if err := p.initConfs(ctx, confsConfNum); err != nil {
			return err
		}
	}
	// Ensure we are currently a chain signer.
	cl, err := p.internalCl.get(ctx)
	if err != nil {
		return err
	}
	latestSnap, err := cl.SnapshotAt(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to get latest snapshot: %v", err)
	}
	signer := p.signer.Load().(common.Address)
	if _, ok := latestSnap.Signers[signer]; !ok {
		log.Warn(p.logPre+"Not a signer", "number", latestSnap.Number)
		return nil
		//TODO what if we are still a voter on-contract and need to vote ourself out?
	}

	// Ensure client state caught up.
	if err := p.voterAdmin(ctx, signer, latestSnap); err != nil {
		return err
	}
	// TODO do we need to be able to resubmit w/ increased gas price to 'unstick' txs?

	if err := p.signerAdmin(ctx, signer, latestSnap, confsConfNum.Uint64()); err != nil {
		return err
	}

	return nil
}

// initConfs initializes p.confs if the contract is deployed as of head, otherwise it returns an error.
func (p *proc) initConfs(ctx context.Context, head *big.Int) error {
	cl, err := p.confsCl.get(ctx)
	if err != nil {
		return err
	}
	if code, err := cl.CodeAt(ctx, p.confsCfg.Contract, head); err != nil {
		return err
	} else if len(code) == 0 {
		return fmt.Errorf("no confirmations contract as of block %s", head)
	}
	confs, err := NewConfirmations(p.confsCfg.Contract, cl)
	if err != nil {
		return fmt.Errorf("failed to bind to confirmations contract: %v", err)
	}
	p.confs = confs
	return nil
}

// pendingTxCount returns the pending tx count for an address, by subtracting latest from pending nonce.
func pendingTxCount(ctx context.Context, cl *goclient.Client, addr common.Address) (uint64, error) {
	pending, err := cl.PendingNonceAt(ctx, addr)
	if err != nil {
		return 0, err
	}
	latest, err := cl.NonceAt(ctx, addr, nil)
	if err != nil {
		return 0, err
	}
	return pending - latest, nil
}

// voterAdmin performs the administrative duties of a voter.
func (p *proc) voterAdmin(ctx context.Context, signer common.Address, latestSnap *clique.Snapshot) error {
	cl, err := p.confsCl.get(ctx)
	if err != nil {
		return err
	}
	if txs, err := pendingTxCount(ctx, cl, signer); err != nil {
		return err
	} else if txs > 0 {
		log.Debug(p.logPre+"Waiting for pending txs before voter admin", "txs", txs)
		return nil
	}

	confsLatest, err := cl.LatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest Confirmations block number: %v", err)
	}
	_, isHeadVoter := latestSnap.Voters[signer]
	latestSession := ConfirmationsSession{Contract: p.confs,
		CallOpts: bind.CallOpts{Context: ctx, BlockNumber: confsLatest}}
	isContractVoter, err := latestSession.IsVoter(signer)
	if err != nil {
		return fmt.Errorf("failed to check voter on Confirmations contract: %v", err)
	}
	if !isContractVoter {
		// Can't vote.
		if isHeadVoter {
			// New voter.
			log.Debug(p.logPre + "Not yet voted in on Confirmations")
		}
		return nil
	}

	// As a contract voter, sync go auth.
	voters, err := ConfirmationsVoters(latestSession)
	if err != nil {
		return fmt.Errorf("failed to get voters from Confirmations contract: %v", err)
	}

	// Check for voters to remove.
	if remove := difference(voters, latestSnap.Voters); len(remove) > 0 {
		return p.removeVoter(ctx, signer, firstAlpha(remove))
	}

	// Check for voters to add.
	if add := difference(latestSnap.Voters, voters); len(add) > 0 {
		return p.addVoter(ctx, signer, firstAlpha(add))
	}

	signers, err := ConfirmationsSigners(latestSession)
	if err != nil {
		return fmt.Errorf("failed to get signers from Confirmations contract: %v", err)
	}
	// Check for signers to remove.
	var remove []common.Address
	for s := range signers {
		if _, ok := latestSnap.Signers[s]; !ok {
			remove = append(remove, s)
		}
	}
	if len(remove) > 0 {
		return p.removeSigner(ctx, signer, firstAlpha(remove))
	}

	// Check for signers to add.
	var add []common.Address
	for s := range latestSnap.Signers {
		if _, ok := signers[s]; !ok {
			add = append(add, s)
		}
	}
	if len(add) > 0 {
		return p.addSigner(ctx, signer, firstAlpha(add))
	}
	return nil
}

func (p *proc) addVoter(ctx context.Context, signer common.Address, addr common.Address) error {
	return p.ensureVote(ctx, signer, true, true, addr)
}

func (p *proc) removeVoter(ctx context.Context, signer common.Address, addr common.Address) error {
	return p.ensureVote(ctx, signer, false, true, addr)
}

func (p *proc) addSigner(ctx context.Context, signer common.Address, addr common.Address) error {
	return p.ensureVote(ctx, signer, true, false, addr)
}

func (p *proc) removeSigner(ctx context.Context, signer common.Address, addr common.Address) error {
	return p.ensureVote(ctx, signer, false, false, addr)
}

// ensureVote checks the pending state and casts the vote if it is still necessary.
func (p *proc) ensureVote(ctx context.Context, signer common.Address, add bool, voter bool, addr common.Address) error {
	pendingSession := ConfirmationsSession{Contract: p.confs, CallOpts: bind.CallOpts{Context: ctx, Pending: true}}

	// Get pending vote first, so we don't race after checking state.
	vote, err := pendingSession.Votes(signer)
	if err != nil {
		return fmt.Errorf("failed to get pending vote: %v", err)
	}

	// Check pending state (which may have already processed the vote).
	var isFn func(voter common.Address) (bool, error)
	if voter {
		isFn = pendingSession.IsVoter
	} else {
		isFn = pendingSession.IsSigner
	}
	isPending, err := isFn(addr)
	if err != nil {
		return fmt.Errorf("failed to check pending state: %v", err)
	}
	if isPending == add {
		// Already voted (pending). Nothing to do.
		log.Debug(p.logPre+"Pending processed", "signer", signer.Hex(), "addr", addr.String(), "add", add, "voter", voter)
		return nil
	}

	// Inspect the previously fetched pending vote.
	if vote.Addr == (common.Address{}) {
		// No pending vote, so set one.
		transactOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: signer})
		if err != nil {
			return fmt.Errorf("failed to create keystore transactor: %v", err)
		}
		_, err = p.confs.SetVote(transactOpts, addr, voter, add)
		if err != nil {
			return fmt.Errorf("failed to set vote (%s,add:%t,voter:%t): %v", addr.String(), add, voter, err)
		}
		log.Debug(p.logPre+"Set vote", "signer", signer.Hex(), "addr", addr.String(), "add", add, "voter", voter)
		return nil
	}
	if vote.Addr == addr && vote.Voter == voter && vote.Add == add {
		// Pending vote is correct. Nothing to do.
		log.Debug(p.logPre+"Pending vote is correct", "signer", signer.Hex())
		return nil
	}
	//TODO replace pending vote
	return fmt.Errorf("unimplemented: replace pending vote (%s,add:%t,voter:%t) with (%s,add:%t,voter:%t)",
		vote.Addr.String(), vote.Add, vote.Voter, addr.String(), add, voter)
}

func firstAlpha(as []common.Address) common.Address {
	l := as[0]
	for _, a := range as[1:] {
		if bytes.Compare(a.Bytes(), l.Bytes()) < 0 {
			l = a
		}
	}
	return l
}

// signerAdmin performs the administrative duties of a signer.
func (p *proc) signerAdmin(ctx context.Context, signer common.Address, latestSnap *clique.Snapshot, confirmedNum uint64) error {
	_, isHeadSigner := latestSnap.Signers[signer]
	isContractSigner, err := p.confs.IsSigner(&bind.CallOpts{Context: ctx}, signer)
	if err != nil {
		return fmt.Errorf("failed to check signer on Confirmations contract: %v", err)
	}
	if !isContractSigner {
		p.isContractSigner = false
		// Can't confirm.
		if isHeadSigner {
			// New signer.
			log.Info(p.logPre + "Signer not yet voted in on Confirmations")
		}
		return nil
	}
	p.isContractSigner = true

	// Update the local set of requested confirmations.
	log.Debug(p.logPre+"Updating pending confirmations", "confirmed", confirmedNum)

	session := ConfirmationsSession{Contract: p.confs,
		CallOpts: bind.CallOpts{Context: ctx, BlockNumber: big.NewInt(int64(confirmedNum))}}
	reqs, err := pendingRequests(session)
	if err != nil {
		return fmt.Errorf("failed to get pending requests at block %d: %v", confirmedNum, err)
	}
	p.reqs = reqs
	p.confsConfNum = confirmedNum

	// Process them.
	return p.confirmRequests(ctx, signer)
}

// confirmRequests processes ip.reqs and votes to confirm any outstanding requests which do not already have a pending vote.
func (p *proc) confirmRequests(ctx context.Context, signer common.Address) error {
	if p.confs == nil || !p.isContractSigner || len(p.reqs) == 0 {
		return nil
	}

	cl, err := p.confsCl.get(ctx)
	if err != nil {
		return err
	}
	if txs, err := pendingTxCount(ctx, cl, signer); err != nil {
		return err
	} else if txs > 0 {
		log.Debug(p.logPre+"Waiting for pending txs before confirming requests", "txs", txs, "reqs", len(p.reqs))
		return nil
	}

	log.Debug(p.logPre+"Confirming pending requests", "reqs", len(p.reqs))
	signerConfirmOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: signer})
	if err != nil {
		return fmt.Errorf("failed to create keystore transactor: %v", err)
	}
	logsCache := map[uint64][]types.Log{}
	var confirmed int
	for _, r := range p.reqs {
		if r.BlockNum.Uint64() > p.emitConfNum {
			// Too soon to confirm.
			log.Debug(p.logPre+"Too soon to confirm",
				"confirmed", p.emitConfNum, "num", r.BlockNum.String(),
				"idx", r.LogIndex.String(), "hash", common.Hash(r.EventHash).Hex())
			continue
		}

		pendingSession := ConfirmationsSession{Contract: p.confs,
			CallOpts: bind.CallOpts{Context: ctx, Pending: true, From: signer}}
		// Check the PENDING status of this confirmation, so that pending tx votes are included.
		status, err := pendingSession.Status(r.BlockNum, r.LogIndex, r.EventHash)
		if err != nil {
			log.Error(p.logPre+"Failed to get status of confirmation",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex(), "err", err)
			continue
		}
		switch status {
		case StatusConfirmed, StatusInvalid:
			// Already handled, no need to act.
			log.Debug(p.logPre+"Already confirmed",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex())
			continue
		case StatusNone:
			// It shouldn't be possible to transition backwards to 'none'.
			log.Error(p.logPre+"Confirmation status NONE after formerly PENDING",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex())
			continue
		case StatusPending:
			// Still pending, so we may need to vote.
		default:
			log.Error(p.logPre+"Unrecognized confirmation status",
				"status", status, "num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex())
			continue
		}

		// Check the PENDING state to see if we've already confirmed.
		allow, requestPrice, err := pendingSession.ShouldConfirm(r.BlockNum, r.LogIndex, r.EventHash)
		if err != nil {
			log.Error(p.logPre+"Failed to check if confirmation allowed",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex(), "err", err)
			continue
		}
		if !allow {
			// Either we've already voted, or it is no longer pending.
			log.Debug(p.logPre+"Confirmation already pending",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex())
			// (TODO possible edge case where pending vote need to be updated?)
			continue
		}

		// Ignore underpriced requests.
		if requestPrice.Cmp(p.confsCfg.MinGasPrice) < 0 {
			log.Warn(p.logPre+"Requested gas price lower than min",
				"request", requestPrice.String(), "min", p.confsCfg.MinGasPrice.String(),
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex())
			continue
		}

		// Fetch logs.
		logs, ok := logsCache[r.BlockNum.Uint64()]
		if !ok {
			cl, err := p.emitCl.get(ctx)
			if err != nil {
				return err
			}
			logs, err = cl.FilterLogs(ctx, gochain.FilterQuery{FromBlock: r.BlockNum, ToBlock: r.BlockNum})
			if err != nil {
				log.Error(p.logPre+"Failed to get logs", "block", r.BlockNum.String())
				continue
			}
			logsCache[r.BlockNum.Uint64()] = logs
		}
		// Determine if the requested event is valid.
		var valid bool
		if l := r.LogIndex.Uint64(); l < uint64(len(logs)) {
			// Log exists.
			lg := logs[l]
			if !lg.Removed {
				// Not removed.
				hash := HashLog(&lg)
				if hash == r.EventHash {
					// Hash matches.
					valid = true
				}
			}
		}

		// Confirm.
		opts := *signerConfirmOpts
		opts.GasPrice = requestPrice
		opts.GasLimit, err = p.getTotalConfirmGas(ctx)
		if err != nil {
			return fmt.Errorf("failed to get totalConfirmGas: %v", err)
		}
		opts.GasLimit *= 2 // Double it to be safe.
		tx, err := p.confs.Confirm(&opts, r.BlockNum, r.LogIndex, r.EventHash, valid)
		if err != nil {
			log.Error(p.logPre+"Failed to confirm event",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex(), "valid", valid, "err", err)
			continue
		}
		confirmed++
		log.Debug(p.logPre+"Confirmed event", "num", r.BlockNum.String(), "idx", r.LogIndex.String(),
			"hash", common.Hash(r.EventHash).Hex(), "valid", valid, "hash", tx.Hash().Hex())
	}
	log.Info(p.logPre+"Confirmed events", "count", confirmed, "reqs", len(p.reqs))

	return nil
}

func (p *proc) getTotalConfirmGas(ctx context.Context) (uint64, error) {
	if p.totalConfirmGas == 0 {
		totalGas, err := p.confs.TotalConfirmGas(&bind.CallOpts{Context: ctx})
		if err != nil {
			return 0, err
		}
		if !totalGas.IsUint64() {
			return 0, fmt.Errorf("total confirm gas overflows uint64: %s", totalGas.String())
		}
		p.totalConfirmGas = totalGas.Uint64()
	}
	return p.totalConfirmGas, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// cachedClient caches a client reference which may be delayed or updated.
type cachedClient interface {
	// get blocks until a client is available, or returns an error if the context is cancelled.
	get(context.Context) (*goclient.Client, error)
}

// cachedClientFn blocks until a client is available
// via fn, and caches the first non-error result.
type cachedClientFn struct {
	mu sync.RWMutex
	fn func() (*rpc.Client, error)
	cl *goclient.Client
}

func (c *cachedClientFn) get(ctx context.Context) (*goclient.Client, error) {
	c.mu.RLock()
	r := c.cl
	c.mu.RUnlock()
	if r != nil {
		return r, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for c.cl == nil {
		rpc, err := c.fn()
		if err != nil {
			log.Warn("Failed to create rpc client", "err", err)
			if sleepCtx(ctx, time.Second) != nil {
				return nil, ctx.Err()
			}
			continue
		}
		c.cl = goclient.NewClient(rpc)
	}
	return c.cl, nil
}

// cachedClientSettable blocks until a client is set, caches it,
// and may be updated again later.
type cachedClientSettable struct {
	val atomic.Value
}

func (c *cachedClientSettable) get(ctx context.Context) (*goclient.Client, error) {
	l := c.val.Load()
	for l == nil {
		if sleepCtx(ctx, time.Second) != nil {
			return nil, ctx.Err()
		}
		l = c.val.Load()
	}
	return l.(*goclient.Client), nil
}

func (c *cachedClientSettable) set(cl *goclient.Client) {
	c.val.Store(cl)
}
