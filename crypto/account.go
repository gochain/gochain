package crypto

/*
Convenience object for dealing with keys/addresses
*/

import (
	"crypto/ecdsa"
	"encoding/hex"
	"strings"
)

func CreateAccount() (*Account, error) {
	key, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	return &Account{
		key: key,
	}, nil
}

func ParsePrivateKey(pkHex string) (*Account, error) {
	fromPK := strings.TrimPrefix(pkHex, "0x")
	key, err := HexToECDSA(fromPK)
	if err != nil {
		return nil, err
	}
	return &Account{
		key: key,
	}, nil
}

type Account struct {
	key *ecdsa.PrivateKey
}

func (a *Account) PrivateKey() *ecdsa.PrivateKey {
	return a.key
}

func (a *Account) PublicKeyHex() string {
	return PubkeyToAddress(a.key.PublicKey).Hex()
}

func (a *Account) PrivateKeyHex() string {
	return "0x" + hex.EncodeToString(a.key.D.Bytes())
}
