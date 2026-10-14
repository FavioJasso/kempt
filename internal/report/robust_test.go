package report

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

// TestJSONValues checks how every clean value type encodes, and that values
// encoding/json cannot represent do not make the whole document fail.
func TestJSONValues(t *testing.T) {
	day := normalize.NewDate(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	ts := normalize.NewTimestamp(time.Date(2026, 3, 15, 10, 30, 0, 500000000, time.UTC))
	tests := []struct {
		v    any
		want string
	}{
		{nil, `null`},
		{true, `true`},
		{int64(-9223372036854775808), `-9223372036854775808`},
		{normalize.Decimal("1234.50"), `1234.50`},
		{normalize.Decimal("-0.25"), `-0.25`},
		{normalize.Decimal("12345678901234567890123456789012345678"), `12345678901234567890123456789012345678`},
		{normalize.Decimal("007"), `"007"`},
		{normalize.Decimal("1."), `"1."`},
		{normalize.Decimal(""), `""`},
		{normalize.Decimal("1e5"), `"1e5"`},
		{normalize.Decimal(`{"a":1}`), `"{\"a\":1}"`},
		{day, `"2026-03-15"`},
		{ts, `"2026-03-15 10:30:00.5"`},
		{"a \"quoted\" <b>&\n", `"a \"quoted\" <b>&\n"`},
	}
	p := &plan.Plan{Table: "t", Columns: []plan.Column{{Name: "v", Type: infer.Type{Base: infer.Text}}}}
	for _, tc := range tests {
		p.Rows = append(p.Rows, []any{tc.v})
	}
	sh := &plan.Sheet{Name: "S", Tables: []*plan.Plan{p}, Decisions: []grid.Decision{{Kind: "k", Confidence: math.NaN()}}}
	p.Decisions = []grid.Decision{{Kind: "k", Confidence: math.NaN()}}
	for _, render := range []func(*bytes.Buffer) error{
		func(b *bytes.Buffer) error { return JSON(b, "x.csv", []*plan.Sheet{sh}) },
		func(b *bytes.Buffer) error { return Decisions(b, "x.csv", []*plan.Sheet{sh}) },
	} {
		var buf bytes.Buffer
		if err := render(&buf); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !json.Valid(buf.Bytes()) || strings.Contains(buf.String(), "NaN") {
			t.Errorf("invalid JSON:\n%s", buf.String())
		}
	}
	var doc struct {
		Sheets []struct {
			Tables []struct {
				Rows [][]json.RawMessage `json:"rows"`
			} `json:"tables"`
		} `json:"sheets"`
	}
	var buf bytes.Buffer
	if err := JSON(&buf, "x.csv", []*plan.Sheet{sh}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for i, tc := range tests {
		if got := string(doc.Sheets[0].Tables[0].Rows[i][0]); got != tc.want {
			t.Errorf("value %#v encodes as %s, want %s", tc.v, got, tc.want)
		}
	}
}

// TestJSONDeterministic checks that repeated encodings are byte-identical
// even though cellRoles is a map.
func TestJSONDeterministic(t *testing.T) {
	render := func() string {
		sh := sampleSheet()
		for r := range 50 {
			for c := range 10 {
				sh.CellRoles[grid.CellRef(r, c)] = "data"
			}
		}
		var buf bytes.Buffer
		if err := JSON(&buf, "x.xlsx", []*plan.Sheet{sh}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	first := render()
	for range 20 {
		if render() != first {
			t.Fatal("JSON output differs between runs")
		}
	}
}

// TestRawRagged checks that a hand-built grid whose rows are shorter than
// its declared size is dumped without panicking.
func TestRawRagged(t *testing.T) {
	g := &grid.Grid{Sheet: "R", Rows: 2, Cols: 3, Cells: [][]grid.Cell{
		{{Raw: "a", Kind: grid.String}, {Raw: "=x", Kind: grid.Formula, Formula: "1+1"}},
		{{Raw: "b", Kind: grid.String}},
	}}
	var buf bytes.Buffer
	if err := Raw(&buf, g, 0, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "B1  =1+1") {
		t.Errorf("formula missing:\n%s", buf.String())
	}
}
