package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/gochain-io/gochain/ethdb"
)

var (
	ErrUnsupportedCheckType = errors.New("unsupported check type")
)

type CheckCommand struct{}

func NewCheckCommand() *CheckCommand {
	return &CheckCommand{}
}

func (cmd *CheckCommand) Run(args []string) error {
	// Parse flags.
	fs := flag.NewFlagSet("gochain-ethdb-check", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		return errors.New("path required")
	}

	// Iterate over each path passed in.
	for _, path := range fs.Args() {
		if err := cmd.check(path); err == ErrUnsupportedCheckType {
			fmt.Printf("%s: not currently supported, skipping\n", path)
			continue
		} else if err != nil {
			return fmt.Errorf("%s: %s", path, err)
		}
	}
	return nil
}

func (cmd *CheckCommand) check(path string) error {
	typ, err := ethdb.SegmentFileType(path)
	if err != nil {
		return err
	}

	switch typ {
	case ethdb.SegmentLDB1:
		return cmd.checkLDBSegment(path)
	case ethdb.SegmentETH1:
		return cmd.checkETHSegment(path)
	default:
		return fmt.Errorf("unknown segment type: %q", typ)
	}
}

func (cmd *CheckCommand) checkLDBSegment(path string) error {
	return ErrUnsupportedCheckType
}

func (cmd *CheckCommand) checkETHSegment(path string) error {
	s := ethdb.NewFileSegment(filepath.Base(path), path)
	if err := s.Open(); err != nil {
		return err
	}
	defer s.Close()

	// Print stats.
	fmt.Printf("[eth1] %s\n", path)
	fmt.Printf("SIZE: %d bytes\n", s.Size())
	fmt.Printf("IDX: %d bytes\n", len(s.Index()))
	fmt.Printf("LEN: %d items\n", s.Len())
	fmt.Printf("CAP: %d items\n", s.Cap())
	fmt.Printf("CHKSUM: %x\n", s.Checksum())

	// Verify checksum integrity.
	if chksum, err := ethdb.ChecksumFileSegment(path); err != nil {
		return err
	} else if !bytes.Equal(chksum, s.Checksum()) {
		return fmt.Errorf("checksum mismatch: %x != %x", chksum, s.Checksum())
	}

	// Verify index is the correct size.
	if len(s.Index()) != s.Cap()*8 {
		return fmt.Errorf("unexpected index size: %d != %d", len(s.Index()), s.Cap()*8)
	}

	// Verify every index in bounds.
	data, idx := s.Data(), s.Index()
	for i := 0; i < s.Cap(); i++ {
		offset := int64(binary.BigEndian.Uint64(idx[i*8:]))
		if offset == 0 {
			continue
		} else if offset > int64(len(data)) {
			return fmt.Errorf("offset out of bound: i=%d offset=%d", i, offset)
		}
	}

	fmt.Println("")

	return nil
}
