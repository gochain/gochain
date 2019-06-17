package crypto

/*
Convenience object for dealing with keys/addresses
*/

import (
	"crypto/ecdsa"
	"encoding/hex"
	"strings"
)

func CreateKey() (*Key, error) {
	key, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	return &Key{
		key: key,
	}, nil
}

func ParsePrivateKeyHex(pkHex string) (*Key, error) {
	fromPK := strings.TrimPrefix(pkHex, "0x")
	key, err := HexToECDSA(fromPK)
	if err != nil {
		return nil, err
	}
	return &Key{
		key: key,
	}, nil
}

type Key struct {
	key *ecdsa.PrivateKey
}

func (a *Key) PrivateKey() *ecdsa.PrivateKey {
	return a.key
}

func (a *Key) PublicKeyHex() string {
	return PubkeyToAddress(a.key.PublicKey).Hex()
}

func (a *Key) PrivateKeyHex() string {
	return "0x" + hex.EncodeToString(a.key.D.Bytes())
}
