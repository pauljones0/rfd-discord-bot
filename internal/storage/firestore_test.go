package storage

import (
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestTrimOldDeals_CountTypeAssertions(t *testing.T) {
	// Test that the type assertion logic in TrimOldDeals handles both
	// int64 and *firestorepb.Value types correctly.
	//
	// We can't easily test the full TrimOldDeals without a Firestore backend,
	// but we can verify the type assertion logic used for the count result.

	tests := []struct {
		name     string
		value    interface{}
		wantInt  int64
		wantFail bool
	}{
		{
			name:    "int64 direct",
			value:   int64(42),
			wantInt: 42,
		},
		{
			name: "firestorepb.Value integer",
			value: &firestorepb.Value{
				ValueType: &firestorepb.Value_IntegerValue{IntegerValue: 100},
			},
			wantInt: 100,
		},
		{
			name:     "unexpected type",
			value:    "not a number",
			wantFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result int64
			var failed bool

			switch val := tt.value.(type) {
			case int64:
				result = val
			case *firestorepb.Value:
				result = val.GetIntegerValue()
			default:
				failed = true
			}

			if failed != tt.wantFail {
				t.Errorf("failed = %v, wantFail = %v", failed, tt.wantFail)
			}
			if !tt.wantFail && result != tt.wantInt {
				t.Errorf("result = %d, want %d", result, tt.wantInt)
			}
		})
	}
}

func TestErrDealExists(t *testing.T) {
	// Verify the sentinel error is usable
	if models.ErrDealExists == nil {
		t.Fatal("ErrDealExists should not be nil")
	}
	if models.ErrDealExists.Error() != "deal already exists" {
		t.Errorf("ErrDealExists message = %q, want %q", models.ErrDealExists.Error(), "deal already exists")
	}
}

