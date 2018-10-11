// Copyright 2017 The go-ethereum Authors
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

package clique

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/crypto"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/params"
)

type testerVote struct {
	signer        string
	voted         string
	auth          bool
	voterElection bool
}

// testerAccountPool is a pool to maintain currently active tester accounts,
// mapped from textual names used in the tests below to actual Ethereum private
// keys capable of signing transactions.
type testerAccountPool struct {
	accounts map[string]*ecdsa.PrivateKey
}

func newTesterAccountPool() *testerAccountPool {
	return &testerAccountPool{
		accounts: make(map[string]*ecdsa.PrivateKey),
	}
}

func (ap *testerAccountPool) sign(header *types.Header, signer string) {
	// Ensure we have a persistent key for the signer
	if ap.accounts[signer] == nil {
		ap.accounts[signer], _ = crypto.GenerateKey()
	}
	// Sign the header and embed the signature in extra data
	sig, _ := crypto.Sign(sigHash(context.Background(), header).Bytes(), ap.accounts[signer])
	header.Signer = sig
}

func (ap *testerAccountPool) address(account string) common.Address {
	// Ensure we have a persistent key for the account
	if ap.accounts[account] == nil {
		ap.accounts[account], _ = crypto.GenerateKey()
	}
	// Resolve and return the Ethereum address
	return crypto.PubkeyToAddress(ap.accounts[account].PublicKey)
}

// testerChainReader implements consensus.ChainReader to access the genesis
// block. All other methods and requests will panic.
type testerChainReader struct {
	db common.Database
}

func (r *testerChainReader) Config() *params.ChainConfig                 { return params.AllCliqueProtocolChanges }
func (r *testerChainReader) CurrentHeader() *types.Header                { panic("not supported") }
func (r *testerChainReader) GetHeader(common.Hash, uint64) *types.Header { panic("not supported") }
func (r *testerChainReader) GetBlock(common.Hash, uint64) *types.Block   { panic("not supported") }
func (r *testerChainReader) GetHeaderByHash(common.Hash) *types.Header   { panic("not supported") }
func (r *testerChainReader) GetHeaderByNumber(number uint64) *types.Header {
	if number == 0 {
		return core.GetHeader(r.db.HeaderTable(), core.GetCanonicalHash(r.db, 0), 0)
	}
	panic("not supported")
}

// Tests that voting is evaluated correctly for various simple and complex scenarios.
func TestVoting(t *testing.T) {
	// Define the various voting scenarios to test
	tests := []votingTest{
		{
			// 0: Single signer, no votes cast
			name:           "1-no-votes",
			signers:        []string{"A"},
			voters:         []string{"A"},
			votes:          []testerVote{{signer: "A"}},
			signersResults: []string{"A"},
			votersResults:  []string{"A"},
		},
		{
			// 1: Single signer, voting to add two others (only accept first)
			name:    "1-vote-2",
			signers: []string{"A"},
			voters:  []string{"A"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: true},
				{signer: "B"},
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: true},
			},
			signersResults: []string{"A", "B"},
			votersResults:  []string{"A"},
		},
		{
			// 2: Two signers, voting to add three others (only accept first two)
			name:    "1-vote-3",
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B", voted: "C", auth: true},
				{signer: "A", voted: "D", auth: true},
				{signer: "B", voted: "D", auth: true},
				{signer: "C"},
				{signer: "A", voted: "E", auth: false},
				{signer: "B", voted: "E", auth: false},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 3: Single signer, dropping itself (weird, but one less cornercase by explicitly allowing this)
			name:    "1-drop",
			signers: []string{"A"},
			voters:  []string{"A"},
			votes: []testerVote{
				{signer: "A", voted: "A", auth: false},
			},
			signersResults: []string{"A"},
			votersResults:  []string{},
		},
		{
			// 4: Two signers, actually needing mutual consent to drop either of them (not fulfilled)
			name:    "2-drop-fail",
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: false},
			},
			signersResults: []string{"A", "B"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 5: Two signers, actually needing mutual consent to drop either of them (fulfilled)
			name:    "2-drop",
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: false},
				{signer: "B", voted: "B", auth: false},
			},
			signersResults: []string{"A", "B"},
			votersResults:  []string{"A"},
		},
		{
			// 6: Three signers, two of them deciding to drop the third
			name:    "3-drop",
			signers: []string{"A", "B", "C"},
			voters:  []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
			},
			signersResults: []string{"A", "B", "C"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 7: Four signers, consensus of two not being enough to drop anyone
			name:    "4-drop-fail",
			signers: []string{"A", "B", "C", "D"},
			voters:  []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B", "C", "D"},
		},
		{
			// 8: Four signers, consensus of three already being enough to drop someone
			name:    "4-drop",
			signers: []string{"A", "B", "C", "D"},
			voters:  []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B", "C"},
		},
		{
			// 9: Authorizations are counted once per signer per target
			name:    "auth-count",
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", voted: "C", auth: true},
			},
			signersResults: []string{"A", "B"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 10: Authorizing multiple accounts concurrently is permitted
			name:    "auth-mult",
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", voted: "D", auth: true},
				{signer: "B"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: true},
				{signer: "A"},
				{signer: "B", voted: "C", auth: true},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 11: Deauthorizations are counted once per signer per target
			name:    "deauth-count",
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: false},
				{signer: "B"},
				{signer: "A", voted: "B", auth: false},
				{signer: "B"},
				{signer: "A", voted: "B", auth: false},
			},
			signersResults: []string{"A", "B"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 12: Deauthorizing multiple accounts concurrently is permitted
			name:    "deauth-mult",
			signers: []string{"A", "B", "C", "D"},
			voters:  []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
				{signer: "A"},
				{signer: "B", voted: "C", auth: false},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 13: Votes from deauthorized signers are discarded immediately (deauth votes)
			name:    "deauth-discard-deauth",
			signers: []string{"A", "B", "C"},
			voters:  []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "C", voted: "B", auth: false},
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "A", voted: "B", auth: false},
			},
			signersResults: []string{"A", "B", "C"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 14: Votes from deauthorized signers are discarded immediately (auth votes)
			name:    "deauth-discard-auth",
			signers: []string{"A", "B", "C"},
			voters:  []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "C", voted: "D", auth: true},
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "A", voted: "D", auth: true},
			},
			signersResults: []string{"A", "B", "C"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 15: Cascading changes are not allowed, only the account being voted on may change
			name:    "no-cascade",
			signers: []string{"A", "B", "C", "D"},
			voters:  []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B", "C"},
		},
		{
			// 16: Changes reaching consensus out of bounds (via a deauth) execute on touch
			name:    "out-of-bounds",
			signers: []string{"A", "B", "C", "D"},
			voters:  []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
				{signer: "A"},
				{signer: "B"},
				{signer: "C", voted: "C", auth: true},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B"},
		},
		{
			// 17: Changes reaching consensus out of bounds (via a deauth) may go out of consensus on first touch
			name:    "out-of-bounds-out",
			signers: []string{"A", "B", "C", "D"},
			voters:  []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false, voterElection: true},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false, voterElection: true},
				{signer: "B", voted: "C", auth: false, voterElection: true},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false, voterElection: true},
				{signer: "C", voted: "D", auth: false, voterElection: true},
				{signer: "A"},
				{signer: "B", voted: "C", auth: true, voterElection: true},
			},
			signersResults: []string{"A", "B", "C", "D"},
			votersResults:  []string{"A", "B", "C"},
		},
		{
			// 18: Ensure that pending votes don't survive authorization status changes. This
			// corner case can only appear if a signer is quickly added, removed and then
			// readded (or the inverse), while one of the original voters dropped. If a
			// past vote is left cached in the system somewhere, this will interfere with
			// the final signer outcome.
			name:    "discard-pending",
			signers: []string{"A", "B", "C", "D", "E", "F"},
			voters:  []string{"A", "B", "C", "D", "E"},
			votes: []testerVote{
				{signer: "A", voted: "F", auth: true, voterElection: true}, // Authorize F, 3 votes needed
				{signer: "B", voted: "F", auth: true, voterElection: true},
				{signer: "C", voted: "F", auth: true, voterElection: true},
				{signer: "D", voted: "F", auth: false, voterElection: true}, // Deauthorize F, 3 votes needed (leave A's previous vote "unchanged")
				{signer: "E", voted: "F", auth: false, voterElection: true},
				{signer: "B", voted: "F", auth: false, voterElection: true},
				{signer: "C", voted: "F", auth: false, voterElection: true},
				{signer: "D", voted: "F", auth: true, voterElection: true}, // Almost authorize F as a voter, 2/3 votes needed
				{signer: "E", voted: "F", auth: true, voterElection: true},
				{signer: "B", voted: "A", auth: false, voterElection: true}, // Deauthorize A as a voter, 3 votes needed
				{signer: "C", voted: "A", auth: false, voterElection: true},
				{signer: "D", voted: "A", auth: false, voterElection: true},
				{signer: "E"},
				{signer: "B", voted: "F", auth: true, voterElection: true}, // Finish authorizing F as a voter, 3/3 votes needed
			},
			//results: []string{"B", "C", "D", "E", "F"},
			signersResults: []string{"A", "B", "C", "D", "E", "F"},
			votersResults:  []string{"B", "C", "D", "E", "F"},
		},
		{
			// 19: Epoch transitions reset all votes to allow chain checkpointing
			name:    "epoch-reset",
			epoch:   3,
			signers: []string{"A", "B"},
			voters:  []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A"}, // Checkpoint block, (don't vote here, it's validated outside of snapshots)
				{signer: "B", voted: "C", auth: true},
			},
			signersResults: []string{"A", "B"},
			votersResults:  []string{"A", "B"},
		},
	}
	// Run through the scenarios and test them
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

type votingTest struct {
	name           string
	epoch          uint64
	signers        []string
	voters         []string
	votes          []testerVote
	signersResults []string
	votersResults  []string
}

func (tt *votingTest) run(t *testing.T) {
	// Create the account pool and generate the initial set of signers
	accounts := newTesterAccountPool()

	signers := make([]common.Address, len(tt.signers))
	voters := make([]common.Address, len(tt.voters))
	for j, signer := range tt.signers {
		signers[j] = accounts.address(signer)
	}
	for j, voter := range tt.voters {
		voters[j] = accounts.address(voter)
	}
	for j := 0; j < len(signers); j++ {
		for k := j + 1; k < len(signers); k++ {
			if bytes.Compare(signers[j][:], signers[k][:]) > 0 {
				signers[j], signers[k] = signers[k], signers[j]
			}
		}
	}
	for j := 0; j < len(voters); j++ {
		for k := j + 1; k < len(voters); k++ {
			if bytes.Compare(voters[j][:], voters[k][:]) > 0 {
				voters[j], voters[k] = voters[k], voters[j]
			}
		}
	}
	// Create the genesis block with the initial set of signers
	genesis := &core.Genesis{
		ExtraData: make([]byte, extraVanity),
		Signers:   signers,
		Voters:    voters,
		Signer:    make([]byte, signatureLength),
	}
	// Create a pristine blockchain with the genesis injected
	db := ethdb.NewMemDatabase()
	genesis.Commit(db)

	// Assemble a chain of headers from the cast votes
	headers := make([]*types.Header, len(tt.votes))
	for j, vote := range tt.votes {
		headers[j] = &types.Header{
			Number: big.NewInt(int64(j) + 1),
			Time:   big.NewInt(int64(j) * int64(params.DefaultCliquePeriod)),
			Signer: make([]byte, signatureLength),
			Extra:  make([]byte, extraVanity),
		}
		if j > 0 {
			headers[j].ParentHash = headers[j-1].Hash()
		}
		if vote.auth {
			copy(headers[j].Nonce[:], nonceAuthVote)
		}
		headers[j].Extra = ExtraAppendVote(headers[j].Extra, accounts.address(vote.voted), vote.voterElection)
		accounts.sign(headers[j], vote.signer)
	}
	// Pass all the headers through clique and ensure tallying succeeds
	head := headers[len(headers)-1]

	snap, err := New(&params.CliqueConfig{Epoch: tt.epoch}, db).
		snapshot(context.Background(), &testerChainReader{db: db}, head.Number.Uint64(), head.Hash(), headers)
	if err != nil {
		t.Errorf("failed to create voting snapshot: %v", err)
		return
	}
	// Verify the final list of signers against the expected ones
	signers = make([]common.Address, len(tt.signersResults))
	for j, signer := range tt.signersResults {
		signers[j] = accounts.address(signer)
	}
	for j := 0; j < len(signers); j++ {
		for k := j + 1; k < len(signers); k++ {
			if bytes.Compare(signers[j][:], signers[k][:]) > 0 {
				signers[j], signers[k] = signers[k], signers[j]
			}
		}
	}
	signersResult := snap.signers()
	if len(signersResult) != len(signers) {
		t.Errorf("signers mismatch: have %x, want %x", signersResult, signers)
		return
	}
	for j := 0; j < len(signersResult); j++ {
		if !bytes.Equal(signersResult[j][:], signers[j][:]) {
			t.Errorf("signer %d: signer mismatch: have %x, want %x", j, signersResult[j], signers[j])
		}
	}
	// Verify the final list of voters against the expected ones
	voters = make([]common.Address, len(tt.votersResults))
	for j, voter := range tt.votersResults {
		voters[j] = accounts.address(voter)
	}
	for j := 0; j < len(voters); j++ {
		for k := j + 1; k < len(voters); k++ {
			if bytes.Compare(voters[j][:], voters[k][:]) > 0 {
				voters[j], voters[k] = voters[k], voters[j]
			}
		}
	}
	votersResult := snap.voters()
	if len(votersResult) != len(voters) {
		t.Errorf("voters mismatch: have %x, want %x", votersResult, voters)
		return
	}
	for j := 0; j < len(votersResult); j++ {
		if !bytes.Equal(votersResult[j][:], voters[j][:]) {
			t.Errorf("voter %d: voter mismatch: have %x, want %x", j, votersResult[j], voters[j])
		}
	}
}
