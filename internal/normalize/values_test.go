package normalize

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFormatAndJSON(t *testing.T) {
	ts := time.Date(2026, 3, 15, 10, 30, 5, 0, time.FixedZone("x", 3600))
	tests := []struct {
		v          any
		text, json string
	}{
		{nil, "", "null"},
		{true, "true", "true"},
		{false, "false", "false"},
		{int64(-42), "-42", "-42"},
		{Decimal("-1234.50"), "-1234.50", "-1234.50"},
		{NewDate(ts), "2026-03-15", `"2026-03-15"`},
		{NewTimestamp(ts), "2026-03-15 10:30:05", `"2026-03-15 10:30:05"`},
		{NewTimestamp(ts.Add(250 * time.Millisecond)), "2026-03-15 10:30:05.25", `"2026-03-15 10:30:05.25"`},
		{"O'Brien", "O'Brien", `"O'Brien"`},
		{3.5, "", "3.5"},
	}
	for _, tc := range tests {
		if got := Format(tc.v); got != tc.text {
			t.Errorf("Format(%#v) = %q, want %q", tc.v, got, tc.text)
		}
		b, err := json.Marshal(tc.v)
		if err != nil || string(b) != tc.json {
			t.Errorf("json.Marshal(%#v) = %s, %v; want %s", tc.v, b, err, tc.json)
		}
	}
	if f := Decimal("-12.5").Float(); f != -12.5 {
		t.Errorf("Decimal.Float = %v", f)
	}
}
