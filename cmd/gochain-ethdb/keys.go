package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/gochain/gochain/v4/ethdb"
)

type KeysCommand struct{}

func NewKeysCommand() *KeysCommand {
	return &KeysCommand{}
}

func (cmd *KeysCommand) Run(args []string) error {
	fs := flag.NewFlagSet("gochain-ethdb-keys", flag.ContinueOnError)
	tableName := fs.String("table", "", "table name")
	if err := fs.Parse(args); err != nil {
		return err
	} else if fs.NArg() == 0 {
		return errors.New("path required")
	} else if *tableName == "" {
		return errors.New("table name required")
	}

	// Open db.
	db := ethdb.NewDB(fs.Arg(0))
	db.MinCompactionAge = 0
	if err := db.Open(); err != nil {
		return err
	}
	defer db.Close()

	// Open table.
	tbl := db.Table(*tableName)
	if tbl == nil {
		return fmt.Errorf("unknown table name: %q", *tableName)
	}

	// Iterate over all segments
	segments := tbl.SegmentSlice()
	for _, segment := range segments {
		itr := segment.Iterator()
		for itr.Next() {
			fmt.Printf("%x\t%d\n", itr.Key(), len(itr.Value()))
		}
		itr.Close()
	}

	return nil
}
