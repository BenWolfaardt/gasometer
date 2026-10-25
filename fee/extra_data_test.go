package fee_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/fee"
)

func TestDecodeOPStackExtraData(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		want  gasometer.FeeParams
	}{
		{"holocene", "00000000fa00000006", gasometer.FeeParams{Denominator: 250, Elasticity: 6}},
		{"jovian as served by base", "01000000640000000500000000004c4b40", baseParams},
		{"jovian zero minimum", "01000000fa000000020000000000000000", opParams},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fee.DecodeOPStackExtraData(mustHex(t, tt.extra))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !sameParams(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeOPStackExtraDataErrors(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		want  error
	}{
		{"empty", "", gasometer.ErrInvalidExtraData},
		{"unknown version", "02000000640000000500000000004c4b40", gasometer.ErrUnknownExtraDataVersion},
		{"holocene too short", "00000000fa000000", gasometer.ErrInvalidExtraData},
		{"holocene with jovian length", "00000000fa000000060000000000000000", gasometer.ErrInvalidExtraData},
		{"jovian with holocene length", "01000000fa00000006", gasometer.ErrInvalidExtraData},
		{"zero denominator", "000000000000000006", gasometer.ErrInvalidExtraData},
		{"zero elasticity", "01000000fa000000000000000000000000", gasometer.ErrInvalidExtraData},
		{"both zero", "000000000000000000", gasometer.ErrInvalidExtraData},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fee.DecodeOPStackExtraData(mustHex(t, tt.extra))
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameParams(a, b gasometer.FeeParams) bool {
	if a.Denominator != b.Denominator || a.Elasticity != b.Elasticity {
		return false
	}
	if a.MinBaseFee == nil || b.MinBaseFee == nil {
		return a.MinBaseFee == nil && b.MinBaseFee == nil
	}
	return a.MinBaseFee.Cmp(b.MinBaseFee) == 0
}

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
		params, err := fee.DecodeOPStackExtraData(extra)
		if err != nil {
			if !errors.Is(err, gasometer.ErrInvalidExtraData) && !errors.Is(err, gasometer.ErrUnknownExtraDataVersion) {
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

func encodeExtraData(p gasometer.FeeParams) []byte {
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
