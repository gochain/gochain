package clique

import (
	"bytes"
	"testing"

	"github.com/gochain-io/gochain/common"
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
