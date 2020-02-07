package cross

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
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
)

// proc manages a Confirmations contract by processing confirmation requests
// and handling administrative duties.
//
// If the node is not a signer/voter on the Confirmations contract, then it will
// wait to be voted in.
//
// For voters, the first priority is to manage the voter and signer set in Auth.
//  1. Vote out any contract voter who is no longer a voter on-chain (TODO latest, no-confirmed delay, since already delayed by real majority vote)
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
	signer            common.Address
	keystore          *keystore.KeyStore

	internalCl *goclient.Client // Where we are a signer.
	confsCl    *goclient.Client // Where we are confirming.
	emitCl     *goclient.Client // Where events are emitted from.

	confs            *Confirmations
	isContractSigner bool

	confsConfNum uint64
	emitConfNum  uint64
	reqs         []ConfirmationRequest
}

// run executes the main process.
func (p *proc) run(ctx context.Context, done func()) {
	defer done()

	confsT := time.NewTicker(time.Second)
	defer confsT.Stop()
	emitT := time.NewTicker(time.Second)
	defer emitT.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-confsT.C:
			latest, err := p.confsCl.LatestBlockNumber(ctx)
			if err != nil {
				log.Error(p.logPre+"Failed to get latest confs head", "err", err)
				continue
			}
			if l := latest.Uint64(); l > p.confsConfNum+p.confsCfg.Confirmations {
				conf := new(big.Int).SetUint64(l - p.confsCfg.Confirmations)
				err := p.newConfirmedHead(ctx, conf)
				if err != nil {
					log.Error(p.logPre+"Failed to process new confirmed confs head", "num", conf, "err", err)
					continue
				}
			}

		case <-emitT.C:
			latest, err := p.emitCl.LatestBlockNumber(ctx)
			if err != nil {
				log.Error(p.logPre+"Failed to get latest emit head", "err", err)
				continue
			}
			if l := latest.Uint64(); l-p.emitConfNum > p.emitCfg.Confirmations {
				p.emitConfNum = l - p.emitCfg.Confirmations
				err := p.confirmRequests(ctx)
				if err != nil {
					log.Error(p.logPre+"Failed to process new confirmed emit head", "num", p.emitConfNum, "err", err)
					continue
				}
			}
		}
	}
}

// newConfirmedHead processes the new confirmed header at confirmedNum, if possible.
func (p *proc) newConfirmedHead(ctx context.Context, confsConfNum *big.Int) error {
	if p.confs == nil {
		if err := p.initConfs(ctx, confsConfNum); err != nil {
			return err
		}
	}
	// Ensure we are currently a chain signer.
	latestSnap, err := p.internalCl.SnapshotAt(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to get latest snapshot: %v", err)
	}
	if _, ok := latestSnap.Signers[p.signer]; !ok {
		log.Warn(p.logPre+"Not a signer", "number", latestSnap.Number)
		return nil
		//TODO what if we are still a voter on-contract and need to vote ourself out?
	}

	// Ensure client state caught up.
	if err := p.voterAdmin(ctx, latestSnap); err != nil {
		return err
	}
	// TODO do we need to be able to resubmit w/ increased gas price to 'unstick' txs?

	if err := p.signerAdmin(ctx, latestSnap, confsConfNum.Uint64()); err != nil {
		return err
	}

	return nil
}

// initConfs initializes p.confs if the contract is deployed as of head, otherwise it returns an error.
func (p *proc) initConfs(ctx context.Context, head *big.Int) error {
	if code, err := p.confsCl.CodeAt(ctx, p.confsCfg.Contract, head); err != nil {
		return err
	} else if len(code) == 0 {
		return fmt.Errorf("no confirmations contract as of block %s", head)
	}
	confs, err := NewConfirmations(p.confsCfg.Contract, p.confsCl)
	if err != nil {
		return fmt.Errorf("failed to bind to confirmations contract: %v", err)
	}
	p.confs = confs
	return nil
}

func (p *proc) voterAdmin(ctx context.Context, latestSnap *clique.Snapshot) error {
	confsLatest, err := p.confsCl.LatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest Confirmations block number: %v", err)
	}
	_, isHeadVoter := latestSnap.Voters[p.signer]
	latestOpts := &bind.CallOpts{Context: ctx, BlockNumber: confsLatest}
	isContractVoter, err := p.confs.IsVoter(latestOpts, p.signer)
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
	voters, err := ConfirmationsVoters(ctx, confsLatest, p.confs)
	if err != nil {
		return fmt.Errorf("failed to get voters from Confirmations contract: %v", err)
	}

	pendingOpts := &bind.CallOpts{Context: ctx, Pending: true}

	// Check for voters to remove.
	if remove := difference(voters, latestSnap.Voters); len(remove) > 0 {
		toRemove := firstAlpha(remove)
		isVoterPending, err := p.confs.IsVoter(pendingOpts, toRemove)
		if err != nil {
			return fmt.Errorf("failed to check pending state for voter: %v", err)
		}
		if !isVoterPending {
			// Already voted out (pending). Nothing to do.
			return nil
		}
		vote, err := p.confs.Votes(pendingOpts, p.signer)
		if err != nil {
			return fmt.Errorf("failed to get pending vote: %v", err)
		}
		if vote.Addr == (common.Address{}) {
			// No pending vote, so set one.
			transactOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: p.signer})
			if err != nil {
				return fmt.Errorf("failed to create keystore transactor: %v", err)
			}
			_, err = p.confs.SetVote(transactOpts, toRemove, true, false)
			if err != nil {
				return fmt.Errorf("failed to vote to remove voter: %v", err)
			}
			return nil
		}
		if vote.Addr == toRemove && vote.Voter && !vote.Add {
			// Pending vote is correct. Nothing to do.
			return nil
		}
		//TODO replace pending vote
		return fmt.Errorf("remove voter: replace pending (%s,%t,%t) with %s: unimplemented",
			vote.Addr.String(), vote.Add, vote.Voter, toRemove.String())
	}
	//TODO if removed self? stop?

	// Check for voters to add.
	if add := difference(latestSnap.Voters, voters); len(add) > 0 {
		toAdd := firstAlpha(add)
		isVoterPending, err := p.confs.IsVoter(pendingOpts, toAdd)
		if err != nil {
			return fmt.Errorf("failed to check pending state for voter: %v", err)
		}
		if isVoterPending {
			// Already voted in (pending). Nothing to do.
			return nil
		}
		vote, err := p.confs.Votes(pendingOpts, p.signer)
		if err != nil {
			return fmt.Errorf("failed to get pending vote: %v", err)
		}
		if vote.Addr == (common.Address{}) {
			// No pending vote, so set one.
			transactOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: p.signer})
			if err != nil {
				return fmt.Errorf("failed to create keystore transactor: %v", err)
			}
			_, err = p.confs.SetVote(transactOpts, toAdd, true, true)
			if err != nil {
				return fmt.Errorf("failed to vote to add voter: %v", err)
			}
			return nil
		}
		if vote.Addr == toAdd && vote.Voter && vote.Add {
			// Pending vote is correct. Nothing to do.
			return nil
		}
		//TODO replace pending vote
		return fmt.Errorf("add voter: replace pending (%s,%t,%t) with %s: unimplemented",
			vote.Addr.String(), vote.Add, vote.Voter, toAdd.String())
	}

	signers, err := ConfirmationsSigners(ctx, confsLatest, p.confs)
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
		toRemove := firstAlpha(remove)
		isSignerPending, err := p.confs.IsSigner(pendingOpts, toRemove)
		if err != nil {
			return fmt.Errorf("failed to check pending state for signer: %v", err)
		}
		if !isSignerPending {
			// Already voted out (pending). Nothing to do.
			return nil
		}
		vote, err := p.confs.Votes(pendingOpts, p.signer)
		if err != nil {
			return fmt.Errorf("failed to get pending vote: %v", err)
		}
		if vote.Addr == (common.Address{}) {
			// No pending vote, so set one.
			transactOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: p.signer})
			if err != nil {
				return fmt.Errorf("failed to create keystore transactor: %v", err)
			}
			_, err = p.confs.SetVote(transactOpts, toRemove, false, false)
			if err != nil {
				return fmt.Errorf("failed to vote to remove signer: %v", err)
			}
			return nil
		}
		if vote.Addr == toRemove && !vote.Voter && !vote.Add {
			// Pending vote is correct. Nothing to do.
			return nil
		}
		//TODO replace pending vote
		return fmt.Errorf("remove signer: replace pending (%s,%t,%t) with %s: unimplemented",
			vote.Addr.String(), vote.Add, vote.Voter, toRemove.String())
	}
	//TODO if removed self, stop?

	// Check for signers to add.
	var add []common.Address
	for s := range latestSnap.Signers {
		if _, ok := signers[s]; !ok {
			add = append(add, s)
		}
	}
	if len(add) > 0 {
		toAdd := firstAlpha(add)
		isSignerPending, err := p.confs.IsSigner(pendingOpts, toAdd)
		if err != nil {
			return fmt.Errorf("failed to check pending state for signer: %v", err)
		}
		if isSignerPending {
			// Already voted in (pending). Nothing to do.
			return nil
		}
		vote, err := p.confs.Votes(pendingOpts, p.signer)
		if err != nil {
			return fmt.Errorf("failed to get pending vote: %v", err)
		}
		if vote.Addr == (common.Address{}) {
			// No pending vote, so set one.
			transactOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: p.signer})
			if err != nil {
				return fmt.Errorf("failed to create keystore transactor: %v", err)
			}
			_, err = p.confs.SetVote(transactOpts, toAdd, false, true)
			if err != nil {
				return fmt.Errorf("failed to vote to add signer: %v", err)
			}
			return nil
		}
		if vote.Addr == toAdd && !vote.Voter && vote.Add {
			// Pending vote is correct. Nothing to do.
			return nil
		}
		//TODO replace pending vote
		return fmt.Errorf("add signer: replace pending (%s,%t,%t) with %s: unimplemented",
			vote.Addr.String(), vote.Add, vote.Voter, toAdd.String())
	}
	return nil
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

func (p *proc) signerAdmin(ctx context.Context, latestSnap *clique.Snapshot, confirmedNum uint64) error {
	_, isHeadSigner := latestSnap.Signers[p.signer]
	isContractSigner, err := p.confs.IsSigner(&bind.CallOpts{Context: ctx}, p.signer)
	if err != nil {
		return fmt.Errorf("failed to check signer on Confirmations contract: %v", err)
	}
	if !isContractSigner {
		p.isContractSigner = false
		// Can't confirm.
		if isHeadSigner {
			// New signer.
			log.Info("Signer not yet voted in on Confirmations", "network", "GoChain")
		}
		return nil
	}
	p.isContractSigner = true

	// Update the local set of requested confirmations.
	log.Debug(p.logPre+"Updating pending confirmations", "confirmed", confirmedNum)

	reqs, err := pendingRequests(p.confs, big.NewInt(int64(confirmedNum)))
	if err != nil {
		return fmt.Errorf("failed to get pending requests at block %d: %v", confirmedNum, err)
	}
	p.reqs = reqs
	p.confsConfNum = confirmedNum

	log.Debug(p.logPre+"Updated pending confirmations", "count", len(p.reqs))

	// Process them.
	return p.confirmRequests(ctx)
}

// confirmRequests processes ip.reqs and votes to confirm any outstanding requests which do not already have a pending vote.
func (p *proc) confirmRequests(ctx context.Context) error {
	if p.confs == nil || !p.isContractSigner || len(p.reqs) == 0 {
		return nil
	}
	log.Debug(p.logPre+"Confirming pending requests", "count", len(p.reqs))
	signerConfirmOpts, err := bind.NewKeyStoreTransactor(p.keystore, accounts.Account{Address: p.signer})
	if err != nil {
		return fmt.Errorf("failed to create keystore transactor: %v", err)
	}
	signerConfirmOpts.GasLimit = 500000
	logsCache := map[uint64][]types.Log{}
	var confirmed int
	for _, r := range p.reqs {
		if r.BlockNum.Uint64() > p.emitConfNum {
			// Too soon to confirm.
			continue
		}

		// Check the PENDING status of this confirmation, so that pending tx votes are included.
		pendingOpts := &bind.CallOpts{Pending: true, From: p.signer}
		status, err := p.confs.Status(pendingOpts, r.BlockNum, r.LogIndex, r.EventHash)
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
		allow, err := p.confs.ConfirmAllowed(pendingOpts, r.BlockNum, r.LogIndex, r.EventHash)
		if err != nil {
			log.Error(p.logPre+"Failed to check if confirmation allowed",
				"num", r.BlockNum.String(), "idx", r.LogIndex.String(),
				"hash", common.Hash(r.EventHash).Hex(), "err", err)
			continue
		}
		if !allow {
			// Either we've already voted, or it is no longer pending.
			// (TODO possible edge case where pending vote need to be updated?)
			continue
		}

		// Fetch logs.
		logs, ok := logsCache[r.BlockNum.Uint64()]
		if !ok {
			logs, err = p.emitCl.FilterLogs(ctx, gochain.FilterQuery{FromBlock: r.BlockNum, ToBlock: r.BlockNum})
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

		// Vote.
		tx, err := p.confs.Confirm(signerConfirmOpts, r.BlockNum, r.LogIndex, r.EventHash, valid)
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
	log.Info(p.logPre+"Confirmed events", "count", confirmed)

	return nil
}
