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
	"encoding/json"
	"fmt"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/params"
	"github.com/hashicorp/golang-lru"
)

// Vote represents a single vote that an authorized signer made to modify the
// list of authorizations.
type Vote struct {
	Signer    common.Address `json:"signer"`    // Authorized signer that cast this vote
	Block     uint64         `json:"block"`     // Block number the vote was cast in (expire old votes)
	Address   common.Address `json:"address"`   // Account being voted on to change its authorization
	Authorize bool           `json:"authorize"` // Whether to authorize or deauthorize the voted account
}

// Tally is a simple vote tally to keep the current score of votes. Votes that
// go against the proposal aren't counted since it's equivalent to not voting.
type Tally struct {
	Authorize bool `json:"authorize"` // Whether the vote is about authorizing or kicking someone
	Votes     int  `json:"votes"`     // Number of votes until now wanting to pass the proposal
}

// Snapshot is the state of the authorization voting at a given point in time.
type Snapshot struct {
	config   *params.CliqueConfig // Consensus engine parameters to fine tune behavior
	sigcache *lru.ARCCache        // Cache of recent block signatures to speed up ecrecover

	Number  uint64                      `json:"number"`  // Block number where the snapshot was created
	Hash    common.Hash                 `json:"hash"`    // Block hash where the snapshot was created
	Signers map[common.Address]uint64   `json:"signers"` // Each authorized signer at this moment and their most recently signed block
	Voters  map[common.Address]struct{} `json:"voters"`  // Set of authorized voters at this moment
	Votes   []*Vote                     `json:"votes"`   // List of votes cast in chronological order
	Tally   map[common.Address]Tally    `json:"tally"`   // Current vote tally to avoid recalculating
}

// newGenesisSnapshot creates a new snapshot with the specified startup parameters. This
// method does not initialize the signers most recently signed blocks, so only ever use if for
// the genesis block.
func newGenesisSnapshot(config *params.CliqueConfig, sigcache *lru.ARCCache, number uint64, hash common.Hash, signers, voters []common.Address) *Snapshot {
	snap := &Snapshot{
		config:   config,
		sigcache: sigcache,
		Number:   number,
		Hash:     hash,
		Signers:  make(map[common.Address]uint64),
		Voters:   make(map[common.Address]struct{}),
		Tally:    make(map[common.Address]Tally),
	}
	for _, signer := range signers {
		snap.Signers[signer] = 0
	}
	for _, voter := range voters {
		snap.Voters[voter] = struct{}{}
	}
	return snap
}

// loadSnapshot loads an existing snapshot from the database.
func loadSnapshot(config *params.CliqueConfig, sigcache *lru.ARCCache, db common.Database, hash common.Hash) (*Snapshot, error) {
	blob, err := db.GlobalTable().Get(append([]byte("clique-"), hash[:]...))
	if err != nil {
		return nil, err
	}
	snap := new(Snapshot)
	if err := json.Unmarshal(blob, snap); err != nil {
		return nil, err
	}
	snap.config = config
	snap.sigcache = sigcache

	return snap, nil
}

// store inserts the snapshot into the database.
func (s *Snapshot) store(db common.Database) error {
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.GlobalTable().Put(append([]byte("clique-"), s.Hash[:]...), blob)
}

// copy creates a deep copy of the snapshot, though not the individual votes.
func (s *Snapshot) copy() *Snapshot {
	cpy := &Snapshot{
		config:   s.config,
		sigcache: s.sigcache,
		Number:   s.Number,
		Hash:     s.Hash,
		Signers:  make(map[common.Address]uint64),
		Voters:   make(map[common.Address]struct{}),
		Votes:    make([]*Vote, len(s.Votes)),
		Tally:    make(map[common.Address]Tally),
	}
	for signer, signed := range s.Signers {
		cpy.Signers[signer] = signed
	}
	for voter := range s.Voters {
		cpy.Voters[voter] = struct{}{}
	}
	for address, tally := range s.Tally {
		cpy.Tally[address] = tally
	}
	copy(cpy.Votes, s.Votes)

	return cpy
}

// validVote returns whether it makes sense to cast the specified vote in the
// given snapshot context (e.g. don't try to add an already authorized voter or
// remove an not authorized signer).
func (s *Snapshot) validVote(address common.Address, authorize bool, voterElection bool) bool {
	if voterElection {
		_, voter := s.Voters[address]
		return (voter && !authorize) || (!voter && authorize)
	}
	_, signer := s.Signers[address]
	return (signer && !authorize) || (!signer && authorize)
}

// cast adds a new vote into the tally.
func (s *Snapshot) cast(address common.Address, authorize bool, voterElection bool) bool {
	// Ensure the vote is meaningful
	if !s.validVote(address, authorize, voterElection) {
		return false
	}
	// Cast the vote into an existing or new tally
	if old, ok := s.Tally[address]; ok {
		old.Votes++
		s.Tally[address] = old
	} else {
		s.Tally[address] = Tally{Authorize: authorize, Votes: 1}
	}
	return true
}

// uncast removes a previously cast vote from the tally.
func (s *Snapshot) uncast(address common.Address, authorize bool) bool {
	// If there's no tally, it's a dangling vote, just drop
	tally, ok := s.Tally[address]
	if !ok {
		return false
	}
	// Ensure we only revert counted votes
	if tally.Authorize != authorize {
		return false
	}
	// Otherwise revert the vote
	if tally.Votes > 1 {
		tally.Votes--
		s.Tally[address] = tally
	} else {
		delete(s.Tally, address)
	}
	return true
}

// apply creates a new authorization snapshot by applying the given headers to
// the original one.
func (s *Snapshot) apply(ctx context.Context, headers []*types.Header) (*Snapshot, error) {
	// Allow passing in no headers for cleaner code
	if len(headers) == 0 {
		return s, nil
	}
	// Sanity check that the headers can be applied
	for i := 0; i < len(headers)-1; i++ {
		if headers[i+1].Number.Uint64() != headers[i].Number.Uint64()+1 {
			return nil, errInvalidVotingChain
		}
	}
	if headers[0].Number.Uint64() != s.Number+1 {
		return nil, errInvalidVotingChain
	}
	// Iterate through the headers and create a new snapshot
	snap := s.copy()

	for _, header := range headers {
		// Remove any votes on checkpoint blocks
		number := header.Number.Uint64()
		if number%s.config.Epoch == 0 {
			snap.Votes = nil
			snap.Tally = make(map[common.Address]Tally)
		}
		// Resolve the authorization key and check against signers
		signer, err := ecrecover(ctx, header, s.sigcache)
		if err != nil {
			return nil, err
		}
		lastBlockSigned, authorized := snap.Signers[signer]
		if !authorized {
			return nil, fmt.Errorf("%s not authorized to sign", signer.Hex())
		}
		if lastBlockSigned > 0 {
			if next := snap.nextSignableBlockNumber(lastBlockSigned); number < next {
				return nil, fmt.Errorf("%s not authorized to sign %d: signed recently %d, next eligible signature %d", signer.Hex(), number, lastBlockSigned, next)
			}
		}
		snap.Signers[signer] = number

		// Verify if signer can vote
		if _, ok := snap.Voters[signer]; ok {

			var voterElection bool
			var candidate common.Address

			if ExtraHasVote(header.Extra) {
				candidate = ExtraCandidate(header.Extra)
				voterElection = ExtraIsVoterElection(header.Extra)
			}
			// Header authorized, discard any previous votes from the voter
			for i, vote := range snap.Votes {
				if vote.Signer == signer && vote.Address == candidate {
					// Uncast the vote from the cached tally
					snap.uncast(vote.Address, vote.Authorize)

					// Uncast the vote from the chronological list
					snap.Votes = append(snap.Votes[:i], snap.Votes[i+1:]...)
					break // only one vote allowed
				}
			}
			// Tally up the new vote from the signer
			var authorize bool
			switch {
			case bytes.Equal(header.Nonce[:], nonceAuthVote):
				authorize = true
			case bytes.Equal(header.Nonce[:], nonceDropVote):
				authorize = false
			default:
				return nil, errInvalidVote
			}

			if snap.cast(candidate, authorize, voterElection) {
				snap.Votes = append(snap.Votes, &Vote{
					Signer:    signer,
					Block:     number,
					Address:   candidate,
					Authorize: authorize,
				})
			}
			// If the vote passed, update the list of signers or voters
			if tally := snap.Tally[candidate]; tally.Votes > len(snap.Voters)/2 {
				if tally.Authorize {
					_, signer := snap.Signers[candidate]
					if !signer {
						snap.Signers[candidate] = 0
					} else {
						snap.Voters[candidate] = struct{}{}
					}
				} else {
					_, voter := snap.Voters[candidate]
					if !voter {
						delete(snap.Signers, candidate)
					} else {
						delete(snap.Voters, candidate)
						// Discard any previous votes the deauthorized voter cast
						for i := 0; i < len(snap.Votes); i++ {
							if snap.Votes[i].Signer == candidate {
								// Uncast the vote from the cached tally
								snap.uncast(snap.Votes[i].Address, snap.Votes[i].Authorize)

								// Uncast the vote from the chronological list
								snap.Votes = append(snap.Votes[:i], snap.Votes[i+1:]...)

								i--
							}
						}
					}
				}
				// Discard any previous votes around the just changed account
				for i := 0; i < len(snap.Votes); i++ {
					if snap.Votes[i].Address == candidate {
						snap.Votes = append(snap.Votes[:i], snap.Votes[i+1:]...)
						i--
					}
				}
				delete(snap.Tally, candidate)
			}
		}
	}

	snap.Number += uint64(len(headers))
	snap.Hash = headers[len(headers)-1].Hash()

	return snap, nil
}

// signers retrieves the list of authorized signers in ascending order.
func (s *Snapshot) signers() []common.Address {
	signers := make([]common.Address, 0, len(s.Signers))
	for signer := range s.Signers {
		signers = append(signers, signer)
	}
	for i := 0; i < len(signers); i++ {
		for j := i + 1; j < len(signers); j++ {
			if bytes.Compare(signers[i][:], signers[j][:]) > 0 {
				signers[i], signers[j] = signers[j], signers[i]
			}
		}
	}
	return signers
}

// voters retrieves the list of authorized voters in ascending order.
func (s *Snapshot) voters() []common.Address {
	voters := make([]common.Address, 0, len(s.Voters))
	for voter := range s.Voters {
		voters = append(voters, voter)
	}
	for i := 0; i < len(voters); i++ {
		for j := i + 1; j < len(voters); j++ {
			if bytes.Compare(voters[i][:], voters[j][:]) > 0 {
				voters[i], voters[j] = voters[j], voters[i]
			}
		}
	}
	return voters
}

// nextSignableBlockNumber returns the number of the next block legal for signature by the signer of
// lastSignedBlockNumber, based on the current number of signers.
func (s *Snapshot) nextSignableBlockNumber(lastSignedBlockNumber uint64) uint64 {
	n := uint64(len(s.Signers))
	return lastSignedBlockNumber + n/2 + 1
}
