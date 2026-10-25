package services_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

func FuzzDecodeOPStackExtraData(f *testing.F) {
	for _, seed := range []string{
		"00000000fa00000006",
		"01000000640000000500000000004c4b40",
		"01000000fa000000020000000000000000",
		"000000000000000000",
		"02",
		"",
	} {
		f.Add(mustHexF(f, seed))
	}
	f.Fuzz(func(t *testing.T, extra []byte) {
		params, err := services.DecodeOPStackExtraData(extra)
		if err != nil {
			if !errors.Is(err, entities.ErrInvalidExtraData) && !errors.Is(err, entities.ErrUnknownExtraDataVersion) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		if params.Denominator == 0 || params.Elasticity == 0 {
			t.Fatalf("accepted zero params from %x", extra)
		}
		if got := encodeExtraData(params); !bytes.Equal(got, extra) {
			t.Fatalf("round trip: %x decoded to %+v, re-encoded %x", extra, params, got)
		}
	})
}

func encodeExtraData(p entities.FeeParams) []byte {
	out := make([]byte, 9, 17)
	binary.BigEndian.PutUint32(out[1:5], uint32(p.Denominator))
	binary.BigEndian.PutUint32(out[5:9], uint32(p.Elasticity))
	if p.MinBaseFee != nil {
		out[0] = 1
		out = binary.BigEndian.AppendUint64(out, p.MinBaseFee.Uint64())
	}
	return out
}

func mustHexF(f *testing.F, s string) []byte {
	f.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		f.Fatal(err)
	}
	return b
}
