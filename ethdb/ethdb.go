package ethdb

import (
	"encoding/binary"
)

// KeyBlockNumber returns the block number for a given key and returns ok true.
// If the key does not encode the block number then ok is false.
func KeyBlockNumber(key []byte) (num uint64, ok bool) {
	if len(key) < 9 {
		return 0, false
	}
	switch key[0] {
	case 'h', 't', 'n', 'b', 'r': // copied from core/database_util.go
		return binary.BigEndian.Uint64(key[1:9]), true
	default:
		return 0, false
	}
}
