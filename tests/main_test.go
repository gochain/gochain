package tests

import (
	"flag"
	"os"
	"testing"

	"github.com/gochain/gochain/v3/cmd/utils"
	"github.com/gochain/gochain/v3/core/vm"
)

var vmConfig vm.Config

// The VM config for state tests accepts --vm.* command line arguments.
func TestMain(m *testing.M) {
	flag.StringVar(&vmConfig.EVMInterpreter, utils.EVMInterpreterFlag.Name, utils.EVMInterpreterFlag.Value, utils.EVMInterpreterFlag.Usage)
	flag.StringVar(&vmConfig.EWASMInterpreter, utils.EWASMInterpreterFlag.Name, utils.EWASMInterpreterFlag.Value, utils.EWASMInterpreterFlag.Usage)
	flag.Parse()
	os.Exit(m.Run())
}
