package rawdb

import (
	"bytes"
	"testing"

	"github.com/gochain/gochain/v4/common"
)

func TestNumHashKey(t *testing.T) {
	prefix := []byte{headerPrefix}
	hash := common.Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	for i, test := range []struct {
		orig, opt []byte
	}{
		{
			orig: append(append(prefix, encodeBlockNumber(123456789)...), hash.Bytes()...),
			opt:  numHashKey(headerPrefix, 123456789, hash),
		},
		{
			orig: append(append(prefix, encodeBlockNumber(123456789)...), []byte{numSuffix}...),
			opt:  numKey(123456789),
		},
		{
			orig: append(append(append(prefix, encodeBlockNumber(123456789)...), hash[:]...), []byte{tdSuffix}...),
			opt:  tdKey(123456789, hash),
		},
	} {
		if !bytes.Equal(test.orig, test.opt) {
			t.Errorf("%d expected:\n\t%X\nbut got:\n\t%X", i, test.orig, test.opt)
		}
	}
}

func BenchmarkNumHashKey(b *testing.B) {
	prefix := []byte("h")
	b.Run("unoptimized", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = append(append(prefix, encodeBlockNumber(123456789)...), common.Hash{}.Bytes()...)
		}
	})
	b.Run("optimized", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = numHashKey(headerPrefix, 123456789, common.Hash{})
		}
	})
}
