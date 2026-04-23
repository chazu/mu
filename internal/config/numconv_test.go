package config

import (
	"encoding/json"
	"testing"
)

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    float64
		wantErr bool
	}{
		{"float64", float64(3.14), 3.14, false},
		{"float64 integral", float64(42), 42, false},
		{"int64", int64(7), 7, false},
		{"int", int(9), 9, false},
		{"int32", int32(-3), -3, false},
		{"json.Number int", json.Number("12"), 12, false},
		{"json.Number float", json.Number("1.5"), 1.5, false},
		{"json.Number bad", json.Number("not-a-number"), 0, true},
		{"string rejected", "42", 0, true},
		{"bool rejected", true, 0, true},
		{"nil rejected", nil, 0, true},
		{"slice rejected", []any{1}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToFloat64(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ToFloat64(%v) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ToFloat64(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    int64
		wantErr bool
	}{
		{"int64", int64(5), 5, false},
		{"int", int(11), 11, false},
		{"int32", int32(-2), -2, false},
		{"float64 integral", float64(42), 42, false},
		{"float64 non-integral", float64(3.14), 0, true},
		{"json.Number int", json.Number("99"), 99, false},
		{"json.Number float", json.Number("1.5"), 0, true},
		{"string rejected", "5", 0, true},
		{"bool rejected", false, 0, true},
		{"nil rejected", nil, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToInt64(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ToInt64(%v) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ToInt64(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
