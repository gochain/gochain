package types

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/rlp"
)

func TestReceiptsForStorage_EncodeRLP(t *testing.T) {
	receipt1 := &Receipt{
		Status:            ReceiptStatusFailed,
		CumulativeGasUsed: 1,
		Logs: []*Log{
			{Address: common.BytesToAddress([]byte{0x11})},
			{Address: common.BytesToAddress([]byte{0x01, 0x11})},
		},
		TxHash:          common.BytesToHash([]byte{0x11, 0x11}),
		ContractAddress: common.BytesToAddress([]byte{0x01, 0x11, 0x11}),
		GasUsed:         111111,
	}
	receipt2 := &Receipt{
		PostState:         common.Hash{2}.Bytes(),
		CumulativeGasUsed: 2,
		Logs: []*Log{
			{Address: common.BytesToAddress([]byte{0x22})},
			{Address: common.BytesToAddress([]byte{0x02, 0x22})},
		},
		TxHash:          common.BytesToHash([]byte{0x22, 0x22}),
		ContractAddress: common.BytesToAddress([]byte{0x02, 0x22, 0x22}),
		GasUsed:         222222,
	}

	normal := []*ReceiptForStorage{
		(*ReceiptForStorage)(receipt1),
		(*ReceiptForStorage)(receipt2),
	}
	normalBytes, err := rlp.EncodeToBytes(normal)
	if err != nil {
		t.Fatal(err)
	}

	custom := ReceiptsForStorage{receipt1, receipt2}
	customBytes, err := rlp.EncodeToBytes(custom)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(normalBytes, customBytes) {
		t.Errorf("expected %x but got %x", normalBytes, customBytes)
	}

	var normalDec []*ReceiptForStorage
	if err := rlp.DecodeBytes(normalBytes, &normalDec); err != nil {
		t.Fatal(err)
	}

	var customDec ReceiptsForStorage
	if err := rlp.DecodeBytes(normalBytes, &customDec); err != nil {
		t.Fatal(err)
	}
	if len(normalDec) != len(customDec) {
		t.Errorf("expected %v but got %v", normalDec, customDec)
	} else {
		for i := range normalDec {
			normal := (*Receipt)(normalDec[i])
			custom := customDec[i]
			if !reflect.DeepEqual(*normal, *custom) {
				t.Errorf("expected %v but got %v", normalDec, customDec)
				break
			}
		}
	}

}
