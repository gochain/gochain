// Copyright 2014 The go-ethereum Authors
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

package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/gochain-io/gochain/common"
	"github.com/gochain-io/gochain/common/hexutil"
	"github.com/gochain-io/gochain/common/math"
	"github.com/gochain-io/gochain/core/state"
	"github.com/gochain-io/gochain/core/types"
	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/log"
	"github.com/gochain-io/gochain/params"
)

//go:generate gencodec -type Genesis -field-override genesisSpecMarshaling -out gen_genesis.go
//go:generate gencodec -type GenesisAccount -field-override genesisAccountMarshaling -out gen_genesis_account.go

var errGenesisNoConfig = errors.New("genesis has no chain configuration")

// Genesis specifies the header fields, state of a genesis block. It also defines hard
// fork switch-over blocks through the chain configuration.
type Genesis struct {
	Config     *params.ChainConfig `json:"config"`
	Nonce      uint64              `json:"nonce"`
	Timestamp  uint64              `json:"timestamp"`
	ExtraData  []byte              `json:"extraData"`
	Signers    []common.Address    `json:"signers"`
	Voters     []common.Address    `json:"voters"`
	Signer     []byte              `json:"signer"`
	GasLimit   uint64              `json:"gasLimit"   gencodec:"required"`
	Difficulty *big.Int            `json:"difficulty" gencodec:"required"`
	Mixhash    common.Hash         `json:"mixHash"`
	Coinbase   common.Address      `json:"coinbase"`
	Alloc      GenesisAlloc        `json:"alloc"      gencodec:"required"`

	// These fields are used for consensus tests. Please don't use them
	// in actual genesis blocks.
	Number     uint64      `json:"number"`
	GasUsed    uint64      `json:"gasUsed"`
	ParentHash common.Hash `json:"parentHash"`
}

// GenesisAlloc specifies the initial state that is part of the genesis block.
type GenesisAlloc map[common.Address]GenesisAccount

func (ga *GenesisAlloc) UnmarshalJSON(data []byte) error {
	m := make(map[common.UnprefixedAddress]GenesisAccount)
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*ga = make(GenesisAlloc)
	for addr, a := range m {
		(*ga)[common.Address(addr)] = a
	}
	return nil
}

func (ga *GenesisAlloc) Total() *big.Int {
	sum := new(big.Int)
	if ga == nil {
		return sum
	}
	for _, st := range *ga {
		sum = sum.Add(sum, st.Balance)
	}
	return sum
}

// GenesisAccount is an account in the state of the genesis block.
type GenesisAccount struct {
	Code       []byte                      `json:"code,omitempty"`
	Storage    map[common.Hash]common.Hash `json:"storage,omitempty"`
	Balance    *big.Int                    `json:"balance" gencodec:"required"`
	Nonce      uint64                      `json:"nonce,omitempty"`
	PrivateKey []byte                      `json:"secretKey,omitempty"` // for tests
}

// field type overrides for gencodec
type genesisSpecMarshaling struct {
	Nonce      math.HexOrDecimal64
	Timestamp  math.HexOrDecimal64
	ExtraData  hexutil.Bytes
	Signer     hexutil.Bytes
	GasLimit   math.HexOrDecimal64
	GasUsed    math.HexOrDecimal64
	Number     math.HexOrDecimal64
	Difficulty *math.HexOrDecimal256
	Alloc      map[common.UnprefixedAddress]GenesisAccount
}

type genesisAccountMarshaling struct {
	Code       hexutil.Bytes
	Balance    *math.HexOrDecimal256
	Nonce      math.HexOrDecimal64
	Storage    map[storageJSON]storageJSON
	PrivateKey hexutil.Bytes
}

// storageJSON represents a 256 bit byte array, but allows less than 256 bits when
// unmarshaling from hex.
type storageJSON common.Hash

func (h *storageJSON) UnmarshalText(text []byte) error {
	text = bytes.TrimPrefix(text, []byte("0x"))
	if len(text) > 64 {
		return fmt.Errorf("too many hex characters in storage key/value %q", text)
	}
	offset := len(h) - len(text)/2 // pad on the left
	if _, err := hex.Decode(h[offset:], text); err != nil {
		fmt.Println(err)
		return fmt.Errorf("invalid hex storage key/value %q", text)
	}
	return nil
}

func (h storageJSON) MarshalText() ([]byte, error) {
	return hexutil.Bytes(h[:]).MarshalText()
}

// GenesisMismatchError is raised when trying to overwrite an existing
// genesis block with an incompatible one.
type GenesisMismatchError struct {
	Stored, New common.Hash
}

func (e *GenesisMismatchError) Error() string {
	return fmt.Sprintf("database already contains an incompatible genesis block (have %x, new %x)", e.Stored[:8], e.New[:8])
}

// SetupGenesisBlock writes or updates the genesis block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func SetupGenesisBlock(db common.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllCliqueProtocolChanges, common.Hash{}, errGenesisNoConfig
	}

	// Just commit the new block if there is no stored genesis block.
	stored := GetCanonicalHash(db, 0)
	if (stored == common.Hash{}) {
		if genesis == nil {
			log.Info("Writing default main-net genesis block")
			genesis = DefaultGenesisBlock()
		} else {
			log.Info("Writing custom genesis block")
		}
		block, err := genesis.Commit(db)
		return genesis.Config, block.Hash(), err
	}

	// Check whether the genesis block is already written.
	if genesis != nil {
		hash := genesis.ToBlock(nil).Hash()
		if hash != stored {
			return genesis.Config, hash, &GenesisMismatchError{stored, hash}
		}
	}

	// Get the existing chain configuration.
	newcfg := genesis.configOrDefault(stored)
	storedcfg, err := GetChainConfig(db.GlobalTable(), stored)
	if err != nil {
		if err == ErrChainConfigNotFound {
			// This case happens if a genesis write was interrupted.
			log.Warn("Found genesis block without chain config")
			err = WriteChainConfig(db.GlobalTable(), stored, newcfg)
		}
		return newcfg, stored, err
	}
	// Special case: don't change the existing config of a non-mainnet chain if no new
	// config is supplied. These chains would get AllProtocolChanges (and a compat error)
	// if we just continued here.
	if genesis == nil && stored != params.MainnetGenesisHash {
		return storedcfg, stored, nil
	}

	// Check config compatibility and write the config. Compatibility errors
	// are returned to the caller unless we're already at block zero.
	height := GetBlockNumber(db.GlobalTable(), GetHeadHeaderHash(db.GlobalTable()))
	if height == missingNumber {
		return newcfg, stored, fmt.Errorf("missing block number for head header hash")
	}
	compatErr := storedcfg.CheckCompatible(newcfg, height)
	if compatErr != nil && height != 0 && compatErr.RewindTo != 0 {
		return newcfg, stored, compatErr
	}
	return newcfg, stored, WriteChainConfig(db.GlobalTable(), stored, newcfg)
}

func (g *Genesis) configOrDefault(ghash common.Hash) *params.ChainConfig {
	switch {
	case g != nil:
		return g.Config
	case ghash == params.MainnetGenesisHash:
		return params.MainnetChainConfig
	case ghash == params.TestnetGenesisHash:
		return params.TestnetChainConfig
	default:
		return params.AllCliqueProtocolChanges
	}
}

// ToBlock creates the genesis block and writes state of a genesis specification
// to the given database (or discards it if nil).
func (g *Genesis) ToBlock(db common.Database) *types.Block {
	if db == nil {
		db = ethdb.NewMemDatabase()
	}
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	for addr, account := range g.Alloc {
		statedb.AddBalance(addr, account.Balance)
		statedb.SetCode(addr, account.Code)
		statedb.SetNonce(addr, account.Nonce)
		for key, value := range account.Storage {
			statedb.SetState(addr, key, value)
		}
	}
	root := statedb.IntermediateRoot(false)
	head := &types.Header{
		Number:     new(big.Int).SetUint64(g.Number),
		Nonce:      types.EncodeNonce(g.Nonce),
		Time:       new(big.Int).SetUint64(g.Timestamp),
		ParentHash: g.ParentHash,
		Extra:      g.ExtraData,
		Signers:    g.Signers,
		Voters:     g.Voters,
		Signer:     g.Signer,
		GasLimit:   g.GasLimit,
		GasUsed:    g.GasUsed,
		Difficulty: g.Difficulty,
		MixDigest:  g.Mixhash,
		Coinbase:   g.Coinbase,
		Root:       root,
	}
	if g.GasLimit == 0 {
		head.GasLimit = params.GenesisGasLimit
	}
	if g.Difficulty == nil {
		head.Difficulty = big.NewInt(1)
	}
	if _, err := statedb.Commit(false); err != nil {
		log.Error("Cannot commit genesis to state db", "err", err)
	}
	if err := statedb.Database().TrieDB().Commit(root, true); err != nil {
		log.Error("Cannot commit genesis to trie db", "err", err)
	}

	return types.NewBlock(head, nil, nil, nil)
}

// Commit writes the block and state of a genesis specification to the database.
// The block is committed as the canonical head block.
func (g *Genesis) Commit(db common.Database) (*types.Block, error) {
	block := g.ToBlock(db)
	if block.Number().Sign() != 0 {
		return nil, fmt.Errorf("can't commit genesis block with number > 0")
	}
	if err := WriteTd(db.GlobalTable(), block.Hash(), block.NumberU64(), g.Difficulty); err != nil {
		return nil, err
	}
	if err := WriteBlock(db.GlobalTable(), db.BodyTable(), db.HeaderTable(), block); err != nil {
		return nil, err
	}
	if err := WriteBlockReceipts(db.ReceiptTable(), block.Hash(), block.NumberU64(), nil); err != nil {
		return nil, err
	}
	if err := WriteCanonicalHash(db, block.Hash(), block.NumberU64()); err != nil {
		return nil, err
	}
	if err := WriteHeadBlockHash(db.GlobalTable(), block.Hash()); err != nil {
		return nil, err
	}
	if err := WriteHeadHeaderHash(db.GlobalTable(), block.Hash()); err != nil {
		return nil, err
	}
	config := g.Config
	if config == nil {
		config = params.AllCliqueProtocolChanges
	}
	return block, WriteChainConfig(db.GlobalTable(), block.Hash(), config)
}

// MustCommit writes the genesis block and state to db, panicking on error.
// The block is committed as the canonical head block.
func (g *Genesis) MustCommit(db common.Database) *types.Block {
	block, err := g.Commit(db)
	if err != nil {
		panic(err)
	}
	return block
}

// GenesisBlockForTesting creates and writes a block in which addr has the given wei balance.
func GenesisBlockForTesting(db common.Database, addr common.Address, balance *big.Int) *types.Block {
	g := Genesis{
		Config: params.AllCliqueProtocolChanges,
		Alloc:  GenesisAlloc{addr: {Balance: balance}},
		Signer: hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
	}
	return g.MustCommit(db)
}

// DefaultGenesisBlock returns the GoChain main net genesis block.
func DefaultGenesisBlock() *Genesis {
	allocAddr := common.HexToAddress("0xF75B6E2D2d69Da07f2940e239E25229350f8103f")
	alloc, ok := new(big.Int).SetString("1000000000000000000000000000", 10)
	if !ok {
		panic("failed to parse big.Int string")
	}
	var extra = []byte("GoChain")
	return &Genesis{
		Config:     params.MainnetChainConfig,
		Timestamp:  1526400000,
		ExtraData:  append(extra, make([]byte, 32-len(extra))...),
		GasLimit:   params.GenesisGasLimit,
		Difficulty: big.NewInt(1),
		Signers: []common.Address{
			common.HexToAddress("0xed7f2e81b0264177e0df8f275f97fd74fa51a896"),
			common.HexToAddress("0x3ad14430951aba12068a8167cebe3ddd57614432"),
			common.HexToAddress("0x3729d2e93e8037f87a2c9afe34cb84b7069e4dea"),
			common.HexToAddress("0xf6290b7f9f871d21317acc259f2ae23c0aa69c73"),
			common.HexToAddress("0xf7678aa7f42bc017f3d6011ca27aed400647960d"),
		},
		Voters: []common.Address{
			common.HexToAddress("0xed7f2e81b0264177e0df8f275f97fd74fa51a896"),
		},
		Signer: hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		Alloc:  GenesisAlloc{allocAddr: {Balance: alloc}},
	}
}

// DefaultTestnetGenesisBlock returns the GoChain Testnet network genesis block.
func DefaultTestnetGenesisBlock() *Genesis {
	alloc, ok := new(big.Int).SetString("1000000000000000000000000000", 10)
	if !ok {
		panic("failed to parse big.Int string")
	}
	return &Genesis{
		Config:     params.TestnetChainConfig,
		Timestamp:  1526048200,
		ExtraData:  hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000"),
		GasLimit:   params.GenesisGasLimit,
		Difficulty: big.NewInt(1),
		Signers: []common.Address{
			common.HexToAddress("0x7aeceb5d345a01f8014a4320ab1f3d467c0c086a"),
			common.HexToAddress("0xdd7e460302a911f9162a208370cdcdc37b892453"),
			common.HexToAddress("0x10a8a552c8a8945f32f6fded5e44d9101b3491d8"),
		},
		Voters: []common.Address{
			common.HexToAddress("0x7aeceb5d345a01f8014a4320ab1f3d467c0c086a"),
		},
		Signer: hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		Alloc: GenesisAlloc{
			common.HexToAddress("0x2Fe70F1Df222C85ad6Dd24a3376Eb5ac32136978"): {
				Balance: alloc,
			},
		},
	}
}

// DeveloperGenesisBlock returns the 'gochain --dev' genesis block. Note, this must
// be seeded with the faucet address.
func DeveloperGenesisBlock(period uint64, faucet common.Address) *Genesis {
	// Override the default period to the user requested one
	config := *params.AllCliqueProtocolChanges
	config.Clique.Period = period

	alloc, ok := new(big.Int).SetString("1000000000000000000000000000", 10)
	if !ok {
		panic("failed to parse big.Int string")
	}
	var extra = []byte(faucet.Hex())[:32]
	// Assemble and return the genesis with the precompiles and faucet pre-funded
	return &Genesis{
		Config:     &config,
		Timestamp:  uint64(time.Now().Unix()),
		ExtraData:  append(extra, make([]byte, 32-len(extra))...),
		GasLimit:   params.GenesisGasLimit,
		Difficulty: big.NewInt(1),
		Signers:    []common.Address{faucet},
		Voters:     []common.Address{faucet},
		Signer:     hexutil.MustDecode("0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		Alloc:      GenesisAlloc{faucet: {Balance: alloc}},
	}
}
