package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gochain-io/gochain/ethdb"
	"github.com/gochain-io/gochain/ethdb/s3"
	"github.com/gochain-io/gochain/log"
	"github.com/syndtr/goleveldb/leveldb"
)

func main() {
	if err := run(); err == flag.ErrHelp {
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var config ethdb.Config

	log.Root().SetHandler(log.StderrHandler)

	// Parse flags.
	fs := flag.NewFlagSet("gochain-migrate", flag.ContinueOnError)
	fs.Usage = usage
	offset := fs.Int("offset", 0, "")
	fs.StringVar(&config.Endpoint, "ethdb.endpoint", "", "")
	fs.StringVar(&config.Bucket, "ethdb.bucket", "", "")
	fs.StringVar(&config.AccessKeyID, "ethdb.accesskeyid", "", "")
	fs.StringVar(&config.SecretAccessKey, "ethdb.secretaccesskey", "", "")
	fs.IntVar(&config.MaxOpenSegmentCount, "ethdb.maxopensegmentcount", config.MaxOpenSegmentCount, "")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	// Read paths from arguments.
	srcPath, dstPath := fs.Arg(0), fs.Arg(1)
	if srcPath == "" {
		return errors.New("source path required")
	} else if dstPath == "" {
		return errors.New("destination path required")
	}

	// Open source database.
	log.Info("opening src database", "path", srcPath)
	src, err := leveldb.OpenFile(srcPath, nil)
	if err != nil {
		return err
	}
	defer src.Close()

	// Open destination database.
	log.Info("opening dst database", "path", dstPath)
	dst := ethdb.NewDB(dstPath)
	dst.MaxOpenSegmentCount = config.MaxOpenSegmentCount
	s3.ConfigureDB(dst, config)
	if err := dst.Open(); err != nil {
		log.Error("cannot open dst database", "err", err)
		return err
	}
	defer src.Close()

	// Iterate over every key in the source database.
	itr := src.NewIterator(nil, nil)
	defer itr.Release()

	// Write all key/values to new database.
	log.Info("begin copying data")
	lastTime := time.Now()
	const logBatchSize = 10000
	for i := 1; itr.Next(); i++ {
		var tbl *ethdb.Table

		if i < *offset {
			continue
		}

		switch {
		case isBodyKey(itr.Key()):
			tbl = dst.BodyTable().(*ethdb.Table)
		case isHeaderKey(itr.Key()):
			tbl = dst.HeaderTable().(*ethdb.Table)
		case isReceiptKey(itr.Key()):
			tbl = dst.ReceiptTable().(*ethdb.Table)
		default:
			tbl = dst.GlobalTable().(*ethdb.Table)
		}

		if err := tbl.Put(itr.Key(), itr.Value()); err != nil {
			return fmt.Errorf("cannot insert item: tbl=%s key=%x err=%q", tbl.Name, itr.Key(), err)
		}

		if i%logBatchSize == 0 {
			log.Info("keys copied", "n", i, "per_key", time.Since(lastTime)/logBatchSize)
			lastTime = time.Now()
		}
	}
	if err := itr.Error(); err != nil {
		return err
	}
	log.Info("copy complete")

	return nil
}

func isBodyKey(key []byte) bool {
	return bytes.HasPrefix(key, []byte("b")) && len(key) == 41
}

func isHeaderKey(key []byte) bool {
	return bytes.HasPrefix(key, []byte("h")) && (len(key) == 41 || (len(key) == 10 && key[9] == 'n'))
}

func isReceiptKey(key []byte) bool {
	return bytes.HasPrefix(key, []byte("r")) && len(key) == 41
}

func usage() {
	fmt.Fprintln(os.Stderr, `
usage: gochain-migrate [args] SRC DST

Arguments

	-offset NUM
		Key offset to start from.
	-ethdb.endpoint VALUE
		S3 compatible archive endpoint.
	-ethdb.bucket VALUE
		Name of S3 archive bucket. Must already exist.
	-ethdb.accesskeyid VALUE
		S3 access key ID.
	-ethdb.secretaccesskey VALUE
		S3 secret access key.
	-ethdb.maxopensegmentcount VALUE
		Per-table open segment count.

This migration tool converts a legacy database located at directory SRC
to the new ethdb format in directory DST.
`[1:])
}
