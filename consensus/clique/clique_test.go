package clique

import (
	"bytes"
	"sort"
	"testing"

	"github.com/gochain/gochain/v5/common"
)

func TestExtraData(t *testing.T) {
	for _, test := range []extraDataTest{
		{
			name:          "normal-voter-vote",
			data:          []byte("vanity string!\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01#Eg\x89\xff"),
			vanity:        "vanity string!",
			candidate:     common.HexToAddress("0x123456789"),
			voterElection: true,
		},
		{
			name:          "normal-signer-vote",
			data:          []byte("test\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01#Eg\x89\x00"),
			vanity:        "test",
			candidate:     common.HexToAddress("0x123456789"),
			voterElection: false,
		},
		{
			name:   "spaces-no-vote",
			data:   []byte("  spaces   \x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"),
			vanity: "  spaces   ",
		},
	} {
		t.Run(test.name, test.run)
	}
}

type extraDataTest struct {
	name          string
	data          []byte
	vanity        string
	candidate     common.Address
	voterElection bool
}

func (test *extraDataTest) run(t *testing.T) {
	if vanity := string(bytes.TrimRight(ExtraVanity(test.data), "\x00")); vanity != test.vanity {
		t.Errorf("expected vanity %q but got %q", test.vanity, vanity)
	}
	if ExtraHasVote(test.data) {
		if test.candidate == (common.Address{}) {
			t.Errorf("unexpected vote: %s voter election: %t", ExtraCandidate(test.data), ExtraIsVoterElection(test.data))
		} else {
			candidate, isVoter := ExtraCandidate(test.data), ExtraIsVoterElection(test.data)
			if candidate != test.candidate {
				t.Errorf("expected candidate %s but got %s", test.candidate.Hex(), candidate.Hex())
			}
			if isVoter != test.voterElection {
				t.Errorf("expected voterElections %t but got %t", test.voterElection, isVoter)
			}
		}
	} else {
		if test.candidate != (common.Address{}) {
			t.Errorf("expected vote but got none: %s voter election: %t", test.candidate.Hex(), test.voterElection)
		}
	}

	var extra []byte
	extra = append(extra, test.vanity...)
	extra = ExtraEnsureVanity(extra)
	if test.candidate != (common.Address{}) {
		extra = ExtraAppendVote(extra, test.candidate, test.voterElection)
	}
	if !bytes.Equal(test.data, extra) {
		t.Errorf("expected:\n\t%q\ngot:\n\t%q", test.data, extra)
	}
}

func TestCalcDifficulty(t *testing.T) {
	addrs := []common.Address{
		common.StringToAddress("0abcdefghijklmnopqrs"),
		common.StringToAddress("1abcdefghijklmnopqrs"),
		common.StringToAddress("2abcdefghijklmnopqrs"),
		common.StringToAddress("3abcdefghijklmnopqrs"),
		common.StringToAddress("4abcdefghijklmnopqrs"),
		common.StringToAddress("5abcdefghijklmnopqrs"),
	}
	for _, test := range []testCalcDifficulty{
		// Genesis.
		{
			name: "3/genesis",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 0,
				addrs[1]: 0,
				addrs[2]: 0,
			},
		},
		{
			name: "6/genesis",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 0,
				addrs[1]: 0,
				addrs[2]: 0,
				addrs[3]: 0,
				addrs[4]: 0,
				addrs[5]: 0,
			},
		},

		// All signed.
		{
			name: "3/all-signed/in-turn",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 1,
				addrs[1]: 2,
				addrs[2]: 3,
			},
		},
		{
			name: "3/all-signed/out-of-turn",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 1,
				addrs[1]: 4,
				addrs[2]: 3,
			},
		},
		{
			name: "6/all-signed/in-turn",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 1,
				addrs[1]: 2,
				addrs[2]: 3,
				addrs[3]: 4,
				addrs[4]: 5,
				addrs[5]: 6,
			},
		},
		{
			name: "6/all-signed/out-of-turn",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 9,
				addrs[1]: 2,
				addrs[2]: 7,
				addrs[3]: 8,
				addrs[4]: 5,
				addrs[5]: 6,
			},
		},

		// One new.
		{
			name: "3/one-new",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 0,
				addrs[1]: 4,
				addrs[2]: 3,
			},
		},
		{
			name: "6/one-new",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 1,
				addrs[1]: 2,
				addrs[2]: 3,
				addrs[3]: 4,
				addrs[4]: 5,
				addrs[5]: 0,
			},
		},

		// Multiple new.
		{
			name: "3/multiple-new",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 0,
				addrs[1]: 0,
				addrs[2]: 3,
			},
		},
		{
			name: "6/multiple-new",
			lastSigned: map[common.Address]uint64{
				addrs[0]: 0,
				addrs[1]: 0,
				addrs[2]: 3,
				addrs[3]: 0,
				addrs[4]: 0,
				addrs[5]: 0,
			},
		},
	} {
		t.Run(test.name, test.run)
	}
}

type testCalcDifficulty struct {
	name       string
	lastSigned map[common.Address]uint64
}

func (test *testCalcDifficulty) run(t *testing.T) {
	var signers []common.Address
	for addr := range test.lastSigned {
		signers = append(signers, addr)
	}
	sort.Slice(signers, func(i, j int) bool {
		iAddr, jAddr := signers[i], signers[j]
		iN, jN := test.lastSigned[iAddr], test.lastSigned[jAddr]
		if iN != jN {
			return iN < jN
		}
		return bytes.Compare(iAddr[:], jAddr[:]) < 0
	})
	for i, signer := range signers {
		exp := len(signers) - i
		if exp <= len(signers)/2 {
			exp = 0
		}
		got := CalcDifficulty(test.lastSigned, signer)
		if got != uint64(exp) {
			t.Errorf("expected difficulty %d but got %d", exp, got)
		}
	}
}
