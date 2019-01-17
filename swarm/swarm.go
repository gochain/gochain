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

package swarm

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"net"

	"github.com/gochain-io/gochain/v3/accounts/abi/bind"
	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/contracts/chequebook"
	"github.com/gochain-io/gochain/v3/contracts/ens"
	"github.com/gochain-io/gochain/v3/goclient"
	"github.com/gochain-io/gochain/v3/log"
	"github.com/gochain-io/gochain/v3/p2p"
	"github.com/gochain-io/gochain/v3/p2p/discover"
	"github.com/gochain-io/gochain/v3/rpc"
	"github.com/gochain-io/gochain/v3/swarm/api"
	httpapi "github.com/gochain-io/gochain/v3/swarm/api/http"
	"github.com/gochain-io/gochain/v3/swarm/fuse"
	"github.com/gochain-io/gochain/v3/swarm/network"
	"github.com/gochain-io/gochain/v3/swarm/storage"
)

// the swarm stack
type Swarm struct {
	config      *api.Config            // swarm configuration
	api         *api.Api               // high level api layer (fs/manifest)
	dns         api.Resolver           // DNS registrar
	dbAccess    *network.DbAccess      // access to local chunk db iterator and storage counter
	storage     storage.ChunkStore     // internal access to storage, common interface to cloud storage backends
	dpa         *storage.DPA           // distributed preimage archive, the local API to the storage with document level storage/retrieval support
	depo        network.StorageHandler // remote request handler, interface between bzz protocol and the storage
	cloud       storage.CloudStore     // procurement, cloud storage backend (can multi-cloud)
	hive        *network.Hive          // the logistic manager
	backend     chequebook.Backend     // simple blockchain Backend
	privateKey  *ecdsa.PrivateKey
	corsString  string
	swapEnabled bool
	lstore      *storage.LocalStore // local store, needs to store for releasing resources after node stopped
	sfs         *fuse.SwarmFS       // need this to cleanup all the active mounts on node exit
}

type SwarmAPI struct {
	Api     *api.Api
	Backend chequebook.Backend
	PrvKey  *ecdsa.PrivateKey
}

func (s *Swarm) API() *SwarmAPI {
	return &SwarmAPI{
		Api:     s.api,
		Backend: s.backend,
		PrvKey:  s.privateKey,
	}
}

// creates a new swarm service instance
// implements node.Service
func NewSwarm(backend chequebook.Backend, ensClient *goclient.Client, config *api.Config, swapEnabled, syncEnabled bool, cors string) (self *Swarm, err error) {
	if bytes.Equal(common.FromHex(config.PublicKey), storage.ZeroKey) {
		return nil, fmt.Errorf("empty public key")
	}
	if bytes.Equal(common.FromHex(config.BzzKey), storage.ZeroKey) {
		return nil, fmt.Errorf("empty bzz key")
	}

	self = &Swarm{
		config:      config,
		swapEnabled: swapEnabled,
		backend:     backend,
		privateKey:  config.Swap.PrivateKey(),
		corsString:  cors,
	}
	log.Debug(fmt.Sprintf("Setting up Swarm service components"))

	hash := storage.MakeHashFunc(config.ChunkerParams.Hash)
	self.lstore, err = storage.NewLocalStore(hash, config.StoreParams)
	if err != nil {
		return
	}

	// setup local store
	log.Debug(fmt.Sprintf("Set up local storage"))

	self.dbAccess = network.NewDbAccess(self.lstore)
	log.Debug(fmt.Sprintf("Set up local db access (iterator/counter)"))

	// set up the kademlia hive
	self.hive = network.NewHive(
		common.HexToHash(self.config.BzzKey), // key to hive (kademlia base address)
		config.HiveParams,                    // configuration parameters
		swapEnabled,                          // SWAP enabled
		syncEnabled,                          // syncronisation enabled
	)
	log.Debug(fmt.Sprintf("Set up swarm network with Kademlia hive"))

	// setup cloud storage backend
	self.cloud = network.NewForwarder(self.hive)
	log.Debug(fmt.Sprintf("-> set swarm forwarder as cloud storage backend"))

	// setup cloud storage internal access layer
	self.storage = storage.NewNetStore(hash, self.lstore, self.cloud, config.StoreParams)
	log.Debug(fmt.Sprintf("-> swarm net store shared access layer to Swarm Chunk Store"))

	// set up Depo (storage handler = cloud storage access layer for incoming remote requests)
	self.depo = network.NewDepo(hash, self.lstore, self.storage)
	log.Debug(fmt.Sprintf("-> REmote Access to CHunks"))

	// set up DPA, the cloud storage local access layer
	dpaChunkStore := storage.NewDpaChunkStore(self.lstore, self.storage)
	log.Debug(fmt.Sprintf("-> Local Access to Swarm"))
	// Swarm Hash Merklised Chunking for Arbitrary-length Document/File storage
	self.dpa = storage.NewDPA(dpaChunkStore, self.config.ChunkerParams)
	log.Debug(fmt.Sprintf("-> Content Store API"))

	// set up high level api
	transactOpts := bind.NewKeyedTransactor(self.privateKey)

	if ensClient == nil {
		log.Warn("No ENS, please specify non-empty --ens-api to use domain name resolution")
	} else {
		self.dns, err = ens.NewENS(transactOpts, config.EnsRoot, ensClient)
		if err != nil {
			return nil, err
		}
	}
	log.Debug(fmt.Sprintf("-> Swarm Domain Name Registrar @ address %v", config.EnsRoot.Hex()))

	self.api = api.NewApi(self.dpa, self.dns)
	// Manifests for Smart Hosting
	log.Debug(fmt.Sprintf("-> Web3 virtual server API"))

	self.sfs = fuse.NewSwarmFS(self.api)
	log.Debug("-> Initializing Fuse file system")

	return self, nil
}

/*
Start is called when the stack is started
* starts the network kademlia hive peer management
* (starts netStore level 0 api)
* starts DPA level 1 api (chunking -> store/retrieve requests)
* (starts level 2 api)
* starts http proxy server
* registers url scheme handlers for bzz, etc
* TODO: start subservices like sword, swear, swarmdns
*/
// implements the node.Service interface
func (s *Swarm) Start(srv *p2p.Server) error {
	connectPeer := func(url string) error {
		node, err := discover.ParseNode(url)
		if err != nil {
			return fmt.Errorf("invalid node URL: %v", err)
		}
		srv.AddPeer(node)
		return nil
	}
	// set chequebook
	if s.swapEnabled {
		ctx := context.Background() // The initial setup has no deadline.
		err := s.SetChequebook(ctx)
		if err != nil {
			return fmt.Errorf("Unable to set chequebook for SWAP: %v", err)
		}
		log.Debug(fmt.Sprintf("-> cheque book for SWAP: %v", s.config.Swap.Chequebook()))
	} else {
		log.Debug(fmt.Sprintf("SWAP disabled: no cheque book set"))
	}

	log.Warn(fmt.Sprintf("Starting Swarm service"))
	s.hive.Start(
		discover.PubkeyID(&srv.PrivateKey.PublicKey),
		func() string { return srv.ListenAddr },
		connectPeer,
	)
	log.Info(fmt.Sprintf("Swarm network started on bzz address: %v", s.hive.Addr()))

	s.dpa.Start()
	log.Debug(fmt.Sprintf("Swarm DPA started"))

	// start swarm http proxy server
	if s.config.Port != "" {
		addr := net.JoinHostPort(s.config.ListenAddr, s.config.Port)
		go httpapi.StartHttpServer(s.api, &httpapi.ServerConfig{
			Addr:       addr,
			CorsString: s.corsString,
		})
		log.Info(fmt.Sprintf("Swarm http proxy started on %v", addr))

		if s.corsString != "" {
			log.Debug(fmt.Sprintf("Swarm http proxy started with corsdomain: %v", s.corsString))
		}
	}

	return nil
}

// implements the node.Service interface
// stops all component services.
func (s *Swarm) Stop() error {
	s.dpa.Stop()
	err := s.hive.Stop()
	if ch := s.config.Swap.Chequebook(); ch != nil {
		ch.Stop()
		ch.Save()
	}

	if s.lstore != nil {
		s.lstore.DbStore.Close()
	}
	s.sfs.Stop()
	return err
}

// implements the node.Service interface
func (s *Swarm) Protocols() []p2p.Protocol {
	proto, err := network.Bzz(s.depo, s.backend, s.hive, s.dbAccess, s.config.Swap, s.config.SyncParams, s.config.NetworkId)
	if err != nil {
		return nil
	}
	return []p2p.Protocol{proto}
}

// implements node.Service
// Apis returns the RPC Api descriptors the Swarm implementation offers
func (s *Swarm) APIs() []rpc.API {
	return []rpc.API{
		// public APIs
		{
			Namespace: "bzz",
			Version:   "0.1",
			Service:   &Info{s.config, chequebook.ContractParams},
			Public:    true,
		},
		// admin APIs
		{
			Namespace: "bzz",
			Version:   "0.1",
			Service:   api.NewControl(s.api, s.hive),
			Public:    false,
		},
		{
			Namespace: "chequebook",
			Version:   chequebook.Version,
			Service:   chequebook.NewApi(s.config.Swap.Chequebook),
			Public:    false,
		},
		{
			Namespace: "swarmfs",
			Version:   fuse.Swarmfs_Version,
			Service:   s.sfs,
			Public:    false,
		},
		// storage APIs
		// DEPRECATED: Use the HTTP API instead
		{
			Namespace: "bzz",
			Version:   "0.1",
			Service:   api.NewStorage(s.api),
			Public:    true,
		},
		{
			Namespace: "bzz",
			Version:   "0.1",
			Service:   api.NewFileSystem(s.api),
			Public:    false,
		},
		// {Namespace, Version, api.NewAdmin(s), false},
	}
}

func (s *Swarm) Api() *api.Api {
	return s.api
}

// SetChequebook ensures that the local checquebook is set up on chain.
func (s *Swarm) SetChequebook(ctx context.Context) error {
	err := s.config.Swap.SetChequebook(ctx, s.backend, s.config.Path)
	if err != nil {
		return err
	}
	log.Info(fmt.Sprintf("new chequebook set (%v): saving config file, resetting all connections in the hive", s.config.Swap.Contract.Hex()))
	s.hive.DropAll()
	return nil
}

// serialisable info about swarm
type Info struct {
	*api.Config
	*chequebook.Params
}

func (i *Info) Info() *Info {
	return i
}
