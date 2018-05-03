// Copyright 2016 The go-ethereum Authors
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

package api

import (
	"github.com/gochain-io/gochain/swarm/network"
)

type Control struct {
	api  *Api
	hive *network.Hive
}

func NewControl(api *Api, hive *network.Hive) *Control {
	return &Control{api, hive}
}

func (c *Control) BlockNetworkRead(on bool) {
	c.hive.BlockNetworkRead(on)
}

func (c *Control) SyncEnabled(on bool) {
	c.hive.SyncEnabled(on)
}

func (c *Control) SwapEnabled(on bool) {
	c.hive.SwapEnabled(on)
}

func (c *Control) Hive() string {
	return c.hive.String()
}
