package ethdb_test

import (
	"encoding/binary"
	"io/ioutil"

	"github.com/gochain-io/gochain/common"
)

func MustTempFile() string {
	f, err := ioutil.TempFile("", "gochain-")
	if err != nil {
		panic(err)
	}
	f.Close()
	return f.Name()
}

func MustTempDir() string {
	name, err := ioutil.TempDir("", "gochain-")
	if err != nil {
		panic(err)
	}
	return name
}

func numHashKey(prefix byte, number uint64, hash common.Hash) []byte {
	var k [41]byte
	k[0] = prefix
	binary.BigEndian.PutUint64(k[1:], number)
	copy(k[9:], hash[:])
	return k[:]
}
