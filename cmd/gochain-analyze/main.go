package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strconv"

	"github.com/gochain-io/gochain/ethdb"
	"github.com/syndtr/goleveldb/leveldb"
)

func main() {
	if err := run(os.Args[1:]); err == flag.ErrHelp {
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("gochain-analyze", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "verbose")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*verbose {
		log.SetOutput(ioutil.Discard)
	}

	stats, err := readStats(fs.Arg(0))
	if err != nil {
		return err
	}

	if err := printReport(stats); err != nil {
		return err
	}
	return nil
}

func readStats(path string) (Stats, error) {
	// Open LevelDB database.
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return Stats{}, err
	}
	defer db.Close()

	// Iterate over all keys.
	iter := db.NewIterator(nil, nil)
	defer iter.Release()

	stats := NewStats()

	var n int
	for iter.Next() {
		key, value := iter.Key(), iter.Value()

		// Aggregate by key type.
		var st *KeyValueStats
		switch {
		case key[0] == 'h':
			st = &stats.Header
		case key[0] == 't':
			st = &stats.TD
		case key[0] == 'n':
			st = &stats.Num
		case key[0] == 'H':
			st = &stats.BlockHash
		case key[0] == 'b':
			st = &stats.Body
		case key[0] == 'r':
			st = &stats.BlockReceipts
		case key[0] == 'l':
			st = &stats.Lookup
		case key[0] == 'B':
			st = &stats.BloomBits
		case bytes.HasPrefix(key, []byte("secure-key-")):
			st = &stats.TrieNodePreimages
		default:
			st = &stats.Unknown
		}

		if st != nil {
			st.KeySize += len(key)
			st.ValueSize += len(value)
			st.N++
		}

		// Calculate stats by block number
		blockNumber, ok := ethdb.KeyBlockNumber(key)
		if ok {
			st := stats.Blocks[blockNumber]
			if st == nil {
				st = &KeyValueStats{}
				stats.Blocks[blockNumber] = st
			}
			st.KeySize += len(key)
			st.ValueSize += len(value)
			st.N++
		} else {
			stats.Global.KeySize += len(key)
			stats.Global.ValueSize += len(value)
			stats.Global.N++
		}

		n++

		// Print log every once in a while.
		if n%1000000 == 0 {
			log.Printf("PROCESSED[%09d]", n)
		}
	}

	return stats, nil
}

func printReport(stats Stats) error {
	cw := csv.NewWriter(os.Stdout)
	defer cw.Flush()

	cw.Write([]string{"name", "key_size", "value_size", "count"})

	// Write totals by key type.
	for _, item := range []struct {
		typ string
		st  KeyValueStats
	}{
		{"header", stats.Header},
		{"td", stats.TD},
		{"num", stats.Num},
		{"block_hash", stats.BlockHash},
		{"body", stats.Body},
		{"block_receipts", stats.BlockReceipts},
		{"lookup", stats.Lookup},
		{"bloom_bits", stats.BloomBits},
		{"tree_node_preimages", stats.TrieNodePreimages},
		{"unknown", stats.Unknown},
	} {
		cw.Write([]string{
			item.typ,
			strconv.Itoa(item.st.KeySize),
			strconv.Itoa(item.st.ValueSize),
			strconv.Itoa(item.st.N),
		})
	}

	cw.Write([]string{"---", "", "", ""})

	// Write non-block number specific totals.
	cw.Write([]string{
		"GLOBAL",
		strconv.Itoa(stats.Global.KeySize),
		strconv.Itoa(stats.Global.ValueSize),
		strconv.Itoa(stats.Global.N),
	})

	// Write per-block number totals.
	for _, blockNumber := range stats.BlockNumbers() {
		st := stats.Blocks[blockNumber]
		cw.Write([]string{
			fmt.Sprintf("%d", blockNumber),
			strconv.Itoa(st.KeySize),
			strconv.Itoa(st.ValueSize),
			strconv.Itoa(st.N),
		})
	}

	return nil
}

type Stats struct {
	Header            KeyValueStats `json:"header"`
	TD                KeyValueStats `json:"td"`
	Num               KeyValueStats `json:"num"`
	BlockHash         KeyValueStats `json:"block_hash"`
	Body              KeyValueStats `json:"body"`
	BlockReceipts     KeyValueStats `json:"block_receipts"`
	Lookup            KeyValueStats `json:"lookup"`
	BloomBits         KeyValueStats `json:"bloom_bits"`
	TrieNodePreimages KeyValueStats `json:"trie_node_preimages"`
	Unknown           KeyValueStats `json:"unknown"`

	// Stats per-block.
	Blocks map[uint64]*KeyValueStats `json:"blocks"`

	// Non-block specific data
	Global KeyValueStats `json:"global"`
}

func NewStats() Stats {
	return Stats{
		Blocks: make(map[uint64]*KeyValueStats),
	}
}

func (s Stats) BlockNumbers() []uint64 {
	a := make([]uint64, 0, len(s.Blocks))
	for num := range s.Blocks {
		a = append(a, num)
	}
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	return a
}

type KeyValueStats struct {
	KeySize   int `json:"key_size"`   // total key size, in bytes
	ValueSize int `json:"value_size"` // total value size, in bytes
	N         int `json:"n"`          // total count
}
