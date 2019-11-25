package cross

import (
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/crypto"
)

// HashLog returns keccak256(abi.encode(source, topics..., data)).
func HashLog(log *types.Log) common.Hash {
	var b []byte
	b = append(b, log.Address.Hash().Bytes()...)
	for _, t := range log.Topics {
		b = append(b, t.Bytes()...)
	}
	b = append(b, log.Data...)
	return crypto.Keccak256Hash(b)
}
