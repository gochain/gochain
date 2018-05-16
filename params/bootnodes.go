// Copyright 2015 The go-ethereum Authors
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

package params

// MainnetBootnodes are the enode URLs of the P2P bootstrap nodes running on
// the main Ethereum network.
var MainnetBootnodes = []string{
	"enode://1f285e244d778fa55198346ee1f126242f380235c37627635e6f9491e8011f63deb25dba8c9e157ad1a27e75df35cf549bbd2c2e103803435bfb181df32ca480@159.65.70.117:30301",
}

// TestnetBootnodes are the enode URLs of the P2P bootstrap nodes running on the
// test network.
var TestnetBootnodes = []string{
	"enode://bfbb22927487ae6b74d39817e73ee3ba776b12772fc92e00809c3bfca06ad0f1d07276b845f9cd1b1747529b9d2b762c24f877efde7b219cefbc0e012f6c5115@159.65.104.177:30301",
}
