package cross

import (
	"math/big"
	"testing"

	"github.com/gochain/gochain/v3/accounts/abi"
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/crypto"
)

var (
	hashTy    abi.Type
	addressTy abi.Type
	uint256Ty abi.Type
)

func init() {
	hashTy, _ = abi.NewType("bytes32", "", nil)
	addressTy, _ = abi.NewType("address", "", nil)
	uint256Ty, _ = abi.NewType("uint256", "", nil)
}

var (
	transferSource    = common.HexToAddress("0x0000000000000000000000000000000000001234")
	transferEventID   = common.HexToHash("0xda2543cfac858777ecd10b22ad9db15a5d6fc0eb2abd01e4dbbb30f012bb168c")
	transferHashTests = []struct {
		addr   string
		amount *big.Int
		hash   string
	}{
		{addr: "0x1", amount: big.NewInt(1), hash: "0x2ed084d268563f6ebd1105b7cc07acf35eddd49baa175c1672a1cd0b20e5d1b7"},
		{addr: "0xF", amount: big.NewInt(10), hash: "0x8f61e6180ce2c962b15c7d01e7dd21a66da0f17d8b8251af39b25a969e8cca88"},
		{addr: "0xA", amount: big.NewInt(1000), hash: "0x907a2100da50a80ba06717a00d452d097fdb5d831b1b0117f923574edcc8c33c"},
		{addr: "0x9", amount: big.NewInt(1000000000000000000), hash: "0xaa1e87375db1dcda2293abe5db1dec2335610f93cd58258509681551de6c3da0"},
	}
)

func TestHashTransferEvent(t *testing.T) {
	for _, test := range transferHashTests {
		t.Run(test.addr+"/"+test.amount.String(), func(t *testing.T) {
			packed, err := abi.Arguments{
				abi.Argument{
					Type: addressTy,
				},
				abi.Argument{
					Type: hashTy,
				},
				abi.Argument{
					Type: addressTy,
				},
				abi.Argument{
					Type: uint256Ty,
				},
			}.Pack(transferSource, transferEventID, common.HexToHash(test.addr), test.amount)
			if err != nil {
				t.Fatal(err)
			}
			got := crypto.Keccak256Hash(packed)
			if got != common.HexToHash(test.hash) {
				t.Errorf("Expected %s but got %s", test.hash, got.Hex())
			}
		})
	}
}

func TestHashLog(t *testing.T) {
	for _, test := range transferHashTests {
		t.Run(test.addr+"/"+test.amount.String(), func(t *testing.T) {
			data := test.amount.Bytes()
			data = append(make([]byte, 32-len(data)), data...)
			hl := HashLog(&types.Log{
				Address: transferSource,
				Topics:  []common.Hash{transferEventID, common.HexToAddress(test.addr).Hash()},
				Data:    data,
			})
			if hl != common.HexToHash(test.hash) {
				t.Errorf("Expected %s but got %s", test.hash, hl.Hex())
			}
		})
	}
}
