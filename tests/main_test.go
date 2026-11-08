package tests

import (
	"flag"
	"os"
	"testing"

	"github.com/gochain/gochain/v5/core/vm"
)

var vmConfig vm.Config

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}
