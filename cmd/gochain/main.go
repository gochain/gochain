// Copyright 2014 The go-ethereum Authors
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

// geth is the official command-line client for GoChain.
package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gochain/gochain/v5/console/prompt"

	"contrib.go.opencensus.io/exporter/stackdriver"
	"github.com/urfave/cli"
	"go.opencensus.io/trace"

	"github.com/gochain/gochain/v5/accounts/keystore"
	"github.com/gochain/gochain/v5/cmd/utils"
	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/eth"
	"github.com/gochain/gochain/v5/internal/debug"
	"github.com/gochain/gochain/v5/metrics"
	"github.com/gochain/gochain/v5/node"
)

const (
	clientIdentifier = "GoChain" // Client identifier to advertise over the network
)

var (
	// Git SHA1 commit hash of the release (set via linker flags)
	gitCommit = ""
	// GoChain address of the GoChain release oracle.
	relOracle = common.HexToAddress("0xfa7b9770ca4cb04296cac84f37736d4041251cdf")
	// The app that holds all commands and flags.
	app = utils.NewApp(gitCommit, "the GoChain command line interface")
	// flags that configure the node
	nodeFlags = []cli.Flag{
		utils.IdentityFlag,
		utils.UnlockedAccountFlag,
		utils.PasswordFileFlag,
		utils.BootnodesFlag,
		utils.BootnodesV4Flag,
		utils.DataDirFlag,
		utils.KeyStoreDirFlag,
		utils.TxPoolNoLocalsFlag,
		utils.TxPoolJournalFlag,
		utils.TxPoolRejournalFlag,
		utils.TxPoolPriceLimitFlag,
		utils.TxPoolPriceBumpFlag,
		utils.TxPoolAccountSlotsFlag,
		utils.TxPoolGlobalSlotsFlag,
		utils.TxPoolAccountQueueFlag,
		utils.TxPoolGlobalQueueFlag,
		utils.TxPoolLifetimeFlag,
		utils.FastSyncFlag,
		utils.SyncModeFlag,
		utils.GCModeFlag,
		utils.LightKDFFlag,
		utils.AllowInsecureUnlockFlag,
		utils.CacheFlag,
		utils.CacheDatabaseFlag,
		utils.CacheGCFlag,
		utils.TrieCacheGenFlag,
		utils.ListenPortFlag,
		utils.MaxPeersFlag,
		utils.MaxPendingPeersFlag,
		utils.MiningEnabledFlag,
		utils.MinerGasTargetFlag,
		utils.MinerLegacyGasTargetFlag,
		utils.MinerGasLimitFlag,
		utils.MinerGasPriceFlag,
		utils.MinerLegacyGasPriceFlag,
		utils.MinerEtherbaseFlag,
		utils.MinerLegacyEtherbaseFlag,
		utils.MinerExtraDataFlag,
		utils.MinerLegacyExtraDataFlag,
		utils.MinerRecommitIntervalFlag,
		utils.NATFlag,
		utils.NoDiscoverFlag,
		utils.NetrestrictFlag,
		utils.NodeKeyFileFlag,
		utils.NodeKeyHexFlag,
		utils.DeveloperFlag,
		utils.DeveloperPeriodFlag,
		utils.TestnetFlag,
		utils.LocalFlag,
		utils.LocalFundFlag,
		utils.VMEnableDebugFlag,
		utils.NetworkIdFlag,
		utils.ConstantinopleOverrideFlag,
		utils.RPCCORSDomainFlag,
		utils.RPCVirtualHostsFlag,
		utils.NetStatsURLFlag,
		utils.MetricsEnabledFlag,
		utils.TracingStackdriverFlag,
		utils.TracingSampleRateFlag,
		utils.NoCompactionFlag,
		utils.GpoBlocksFlag,
		utils.GpoPercentileFlag,
		utils.EthdbEndpointFlag,
		utils.EthdbBucketFlag,
		utils.EthdbAccessKeyIDFlag,
		utils.EthdbSecretAccessKeyFlag,
		utils.EthdbMaxOpenSegmentCountFlag,
		configFileFlag,
	}

	rpcFlags = []cli.Flag{
		utils.RPCEnabledFlag,
		utils.RPCListenAddrFlag,
		utils.RPCPortFlag,
		utils.RPCApiFlag,
		utils.RPCGasCapFlag,
		utils.RPCEVMTimeoutFlag,
		utils.RPCTxFeeCapFlag,
		utils.WSEnabledFlag,
		utils.WSListenAddrFlag,
		utils.WSPortFlag,
		utils.WSApiFlag,
		utils.WSAllowedOriginsFlag,
		utils.IPCDisabledFlag,
		utils.IPCPathFlag,
	}
)

func init() {
	// Initialize the CLI app and start GoChain
	app.Action = gochain
	app.HideVersion = true // we have a command to print the version
	app.Copyright = "Copyright 2018 The GoChain Authors"
	app.Commands = []cli.Command{
		// See chaincmd.go:
		initCommand,
		importCommand,
		exportCommand,
		copydbCommand,
		removedbCommand,
		dumpCommand,
		// See accountcmd.go:
		accountCommand,
		// See consolecmd.go:
		consoleCommand,
		attachCommand,
		javascriptCommand,
		// See misccmd.go:
		versionCommand,
		bugCommand,
		licenseCommand,
		// See config.go
		dumpConfigCommand,
	}
	sort.Sort(cli.CommandsByName(app.Commands))

	app.Flags = append(app.Flags, nodeFlags...)
	app.Flags = append(app.Flags, rpcFlags...)
	app.Flags = append(app.Flags, consoleFlags...)
	app.Flags = append(app.Flags, debug.Flags...)

	app.Before = func(ctx *cli.Context) error {
		runtime.GOMAXPROCS(runtime.NumCPU())
		if err := debug.Setup(ctx); err != nil {
			return err
		}
		// Start system runtime metrics collection
		go metrics.CollectProcessMetrics(3 * time.Second)

		return nil
	}

	app.After = func(ctx *cli.Context) error {
		debug.Exit()
		prompt.Stdin.Close() // Resets terminal mode.
		return nil
	}
}

func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// geth is the main entry point into the system if no special subcommand is ran.
// It creates a default node based on the command line arguments and runs it in
// blocking mode, waiting for it to be shut down.
func gochain(ctx *cli.Context) error {
	if ctx.GlobalIsSet(utils.TracingStackdriverFlag.Name) {
		gcpProjectID := ctx.GlobalString(utils.TracingStackdriverFlag.Name)
		// Enable the Stackdriver Tracing exporter.
		sd, err := stackdriver.NewExporter(stackdriver.Options{
			ProjectID: gcpProjectID,
		})
		if err != nil {
			return fmt.Errorf("failed to create the Stackdriver exporter: %v", err)
		}
		defer sd.Flush()

		// Register/enable the trace exporter.
		trace.RegisterExporter(sd)

		if ctx.GlobalIsSet(utils.TracingSampleRateFlag.Name) {
			rate := ctx.GlobalFloat64(utils.TracingSampleRateFlag.Name)
			trace.ApplyConfig(trace.Config{DefaultSampler: trace.ProbabilitySampler(rate)})
		}
	}

	node := makeFullNode(ctx)
	startNode(ctx, node)
	node.Wait()
	return nil
}

// startNode boots up the system node and all registered protocols, after which
// it unlocks any requested accounts, and starts the RPC/IPC interfaces and the
// miner.
func startNode(ctx *cli.Context, stack *node.Node) {
	// Start up the node itself
	utils.StartNode(stack)

	// Unlock any account specifically requested
	ks := stack.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)

	passwords := utils.MakePasswordList(ctx)
	unlocks := strings.Split(ctx.GlobalString(utils.UnlockedAccountFlag.Name), ",")
	for i, account := range unlocks {
		if trimmed := strings.TrimSpace(account); trimmed != "" {
			if (stack.HTTPEndpoint() != "" || stack.WSEndpoint() != "") && !stack.AccountManager().Config().InsecureUnlockAllowed {
				utils.Fatalf("Account unlock with HTTP access is forbidden! Use --allow-insecure-unlock to override.")
			}
			unlockAccount(ctx, ks, trimmed, i, passwords)
		}
	}
	// Start auxiliary services if enabled
	if ctx.GlobalBool(utils.MiningEnabledFlag.Name) || ctx.GlobalBool(utils.DeveloperFlag.Name) || ctx.GlobalBool(utils.LocalFlag.Name) {
		var gochain *eth.GoChain
		if err := stack.Service(&gochain); err != nil {
			utils.Fatalf("GoChain service not running: %v", err)
		}
		// Set the gas price to the limits from the CLI and start mining
		gasprice := utils.GlobalBig(ctx, utils.MinerLegacyGasPriceFlag.Name)
		if ctx.IsSet(utils.MinerGasPriceFlag.Name) {
			gasprice = utils.GlobalBig(ctx, utils.MinerGasPriceFlag.Name)
		}
		gochain.TxPool().SetGasPrice(gasprice)

		if err := gochain.StartMining(); err != nil {
			utils.Fatalf("Failed to start mining: %v", err)
		}
	}
}
