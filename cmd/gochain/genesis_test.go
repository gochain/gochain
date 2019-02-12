// Copyright 2016 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

type customGenesisTest struct {
	genesis string
	query   string
	result  string
}

var customGenesisTests = map[string]customGenesisTest{
	// Plain
	"plain": {
		genesis: `{
			"alloc"      : {},
			"coinbase"   : "0x0000000000000000000000000000000000000000",
			"difficulty" : "0x20000",
			"extraData"  : "",
			"gasLimit"   : "0x2fefd8",
			"nonce"      : "0x0000000000000042",
			"mixhash"    : "0x0000000000000000000000000000000000000000000000000000000000000000",
			"parentHash" : "0x0000000000000000000000000000000000000000000000000000000000000000",
			"timestamp"  : "0x00",
			"config"     : {
				"clique": {
      				"period": 5,
      				"epoch": 3000
    			}
			}
		}`,
		query:  "eth.getBlock(0).nonce",
		result: "0x0000000000000042",
	},
	// Genesis file with an empty chain configuration (ensure missing fields work)
	"empty": {
		genesis: `{
			"alloc"      : {},
			"coinbase"   : "0x0000000000000000000000000000000000000000",
			"difficulty" : "0x20000",
			"extraData"  : "",
			"gasLimit"   : "0x2fefd8",
			"nonce"      : "0x0125864321546982",
			"mixhash"    : "0x0000000000000000000000000000000000000000000000000000000000000000",
			"parentHash" : "0x0000000000000000000000000000000000000000000000000000000000000000",
			"timestamp"  : "0x00",
			"config"     : {
				"clique": {
      				"period": 5,
      				"epoch": 3000
    			}
			}
		}`,
		query:  "eth.getBlock(0).nonce",
		result: "0x0125864321546982",
	},
	// Genesis file with specific chain configurations
	"specific": {
		genesis: `{
			"alloc"      : {},
			"coinbase"   : "0x0000000000000000000000000000000000000000",
			"difficulty" : "0x20000",
			"extraData"  : "",
			"gasLimit"   : "0x2fefd8",
			"nonce"      : "0x0000159876215648",
			"mixhash"    : "0x0000000000000000000000000000000000000000000000000000000000000000",
			"parentHash" : "0x0000000000000000000000000000000000000000000000000000000000000000",
			"timestamp"  : "0x00",
			"config"     : {
				"homesteadBlock" : 314,
				"clique": {
      				"period": 5,
      				"epoch": 3000
    			}
			}
		}`,
		query:  "eth.getBlock(0).nonce",
		result: "0x0000159876215648",
	},
}

// Tests that initializing GoChain with a custom genesis block and chain definitions
// work properly.
func TestCustomGenesis(t *testing.T) {
	for name, test := range customGenesisTests {
		t.Run(name, test.run)
	}
}
func (test customGenesisTest) run(t *testing.T) {
	// Create a temporary data directory to use and inspect later
	datadir := tmpdir(t)
	defer os.RemoveAll(datadir)

	// Initialize the data directory with the custom genesis block
	json := filepath.Join(datadir, "genesis.json")
	if err := ioutil.WriteFile(json, []byte(test.genesis), 0600); err != nil {
		t.Fatalf("failed to write genesis file: %v", err)
	}
	runGoChain(t, "--datadir", datadir, "init", json).WaitExit()

	// Query the custom genesis block
	geth := runGoChain(t,
		"--datadir", datadir, "--maxpeers", "0", "--port", "0",
		"--nodiscover", "--nat", "none", "--ipcdisable",
		"--exec", test.query, "console")
	geth.ExpectRegexp(test.result)
	geth.ExpectExit()
}
