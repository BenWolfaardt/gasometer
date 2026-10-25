package services_test

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

func TestDecodeOPStackExtraData(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		want  entities.FeeParams
	}{
		{"holocene", "00000000fa00000006", entities.FeeParams{Denominator: 250, Elasticity: 6}},
		{"jovian as served by base", "01000000640000000500000000004c4b40", baseParams},
		{"jovian zero minimum", "01000000fa000000020000000000000000", opParams},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := services.DecodeOPStackExtraData(mustHex(t, tt.extra))
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
		{"empty", "", entities.ErrInvalidExtraData},
		{"unknown version", "02000000640000000500000000004c4b40", entities.ErrUnknownExtraDataVersion},
		{"holocene too short", "00000000fa000000", entities.ErrInvalidExtraData},
		{"holocene with jovian length", "00000000fa000000060000000000000000", entities.ErrInvalidExtraData},
		{"jovian with holocene length", "01000000fa00000006", entities.ErrInvalidExtraData},
		{"zero denominator", "000000000000000006", entities.ErrInvalidExtraData},
		{"zero elasticity", "01000000fa000000000000000000000000", entities.ErrInvalidExtraData},
		{"both zero", "000000000000000000", entities.ErrInvalidExtraData},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := services.DecodeOPStackExtraData(mustHex(t, tt.extra))
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

func sameParams(a, b entities.FeeParams) bool {
	if a.Denominator != b.Denominator || a.Elasticity != b.Elasticity {
		return false
	}
	if a.MinBaseFee == nil || b.MinBaseFee == nil {
		return a.MinBaseFee == nil && b.MinBaseFee == nil
	}
	return a.MinBaseFee.Cmp(b.MinBaseFee) == 0
}
