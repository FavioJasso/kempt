package report

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/normalize"
	"github.com/FavioJasso/kempt/internal/plan"
)

func textOf(t *testing.T, sheets []*plan.Sheet, opts TextOptions) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Text(&buf, "/data/budget_report.xlsx", sheets, opts); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// checkLines fails when a line is wider than 100 runes or ends in a space.
func checkLines(t *testing.T, out string) {
	t.Helper()
	for i, l := range strings.Split(out, "\n") {
		if n := runeLen(l); n > lineWidth {
			t.Errorf("line %d is %d runes wide: %q", i+1, n, l)
		}
		if strings.HasSuffix(l, " ") {
			t.Errorf("line %d has trailing spaces: %q", i+1, l)
		}
	}
}

func TestText(t *testing.T) {
	out := textOf(t, []*plan.Sheet{sampleSheet(), {Name: "Empty", Grid: grid.New("Empty", 0, 0)}}, TextOptions{})
	checkLines(t, out)
	for _, want := range []string{
		"budget_report.xlsx: 2 sheets, 1 table\n",
		"\n== Sheet \"Budget Summary\" (A1:C6, 1 table) ==\n",
		"\nSheet decisions (1):\n  RANGE  KIND         CONF  REASON\n  A6:C6  skip_footer  0.90  Row 6 starts with \"Source\".\n",
		"\n-- Table budget_summary (region A1:C6, 2 rows, 3 columns) --\n",
		"\nColumns (3):\n  #  NAME        TYPE          UNIT   SOURCE  HEADER\n",
		"  1  department  text                 A       Department\n",
		"  2  fy2025      decimal(6,2)  $000s  B       FY2025 ($000s)\n",
		"\nDecisions (3):\n  RANGE  KIND            CONF  REASON\n  A1:C1  skip_title_row  0.95  Row 1 is a single merged cell spanning A:C.\n" +
			"  A3:A4  fill_down       0.90  Vertical merge A3:A4 copied down.\n" +
			"  A5:C5  drop_total_row  0.95  Row 5 starts with \"Total\" and its numbers equal the sum of the rows\n" +
			"                               above it in the group, so it is a totals row; dropped (use\n" +
			"                               --keep-totals to keep it).\n",
		"\nPreview (all 2 rows; ∅ = NULL):\n  ROW  department            fy2025  date\n" +
			"    3  Parks & Recreation   2450.00  2026-03-15\n    4  Parks & Recreation  -1234.00  ∅\n",
		"\n== Sheet \"Empty\" (empty, 0 tables) ==\n\nNo tables found on this sheet.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n--- output ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "'Budget Summary'!") {
		t.Error("decision ranges should be relative to the sheet")
	}
}

func TestTextPreviewRows(t *testing.T) {
	tests := []struct {
		rows        int
		want, avoid string
	}{
		{0, "Preview (all 2 rows; ∅ = NULL):", ""},
		{10, "Preview (all 2 rows; ∅ = NULL):", ""},
		{1, "Preview (first 1 of 2 rows; ∅ = NULL):", "-1234.00"},
		{2, "    4  Parks & Recreation  -1234.00  ∅", ""},
		{-1, "Decisions (3):", "Preview"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.rows), func(t *testing.T) {
			out := textOf(t, []*plan.Sheet{sampleSheet()}, TextOptions{PreviewRows: tc.rows})
			if !strings.Contains(out, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}
			if tc.avoid != "" && strings.Contains(out, tc.avoid) {
				t.Errorf("output contains %q:\n%s", tc.avoid, out)
			}
		})
	}
}

func TestTextWideTable(t *testing.T) {
	p := &plan.Plan{Table: "wide", Region: "A1:L3"}
	row := []any{}
	for j := range 12 {
		base := infer.Text
		var v any = strings.Repeat("x", 30) + fmt.Sprint(j)
		if j%2 == 1 {
			base, v = infer.BigInt, int64(j)
		}
		p.Columns = append(p.Columns, plan.Column{Name: fmt.Sprintf("a_rather_long_column_name_%02d", j), Type: infer.Type{Base: base}})
		row = append(row, v)
	}
	row[11] = nil
	p.Rows = [][]any{row, {normalize.NewTimestamp(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), true}}
	out := textOf(t, []*plan.Sheet{{Name: "W", Tables: []*plan.Plan{p}}}, TextOptions{})
	checkLines(t, out)
	for _, want := range []string{
		"\n== Sheet \"W\" (1 table) ==\n",
		"\n  #  a_rather_long_column_na…  a_rather_long_column_na…  a_rather_long_column_na…\n" +
			"  1  xxxxxxxxxxxxxxxxxxxxxxx…                         1  xxxxxxxxxxxxxxxxxxxxxxx…\n" +
			"  2  2026-01-02 03:04:05                           true  ∅\n",
		"\n  #  a_rather_long_column_na…  a_rather_long_column_na…  a_rather_long_column_na…\n" +
			"  1                         9  xxxxxxxxxxxxxxxxxxxxxxx…                         ∅\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if blocks := strings.Count(out, "\n  #  a_rather"); blocks != 4 {
		t.Errorf("want the 12 columns split into 4 blocks, got %d:\n%s", blocks, out)
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  []string
	}{
		{"", 10, []string{""}},
		{"short", 10, []string{"short"}},
		{"exactly ten", 11, []string{"exactly ten"}},
		{"one two three", 7, []string{"one two", "three"}},
		{"  spaced   out  ", 20, []string{"spaced out"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"a abcdefgh b", 4, []string{"a", "abcd", "efgh", "b"}},
		{"ünïcödé wörds hère", 7, []string{"ünïcödé", "wörds", "hère"}},
		{"a b c d", 3, []string{"a b", "c d"}},
		{"abcd", 4, []string{"abcd"}},
	}
	for _, tc := range tests {
		if got := wrap(tc.in, tc.width); strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrap(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 5, "abc"},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcd…"},
		{"ab def", 4, "ab…"},
		{"ünïcödé", 4, "ünï…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
		{"⟵", 1, "⟵"},
		{"City of Riverton — Budget", 18, "City of Riverton…"},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestValueText(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, "∅"},
		{"", ""},
		{"a\tb", "a b"},
		{"line1\r\nline2\nline3", "line1↵line2↵line3"},
		{true, "true"},
		{int64(-7), "-7"},
		{normalize.Decimal("12.50"), "12.50"},
		{normalize.NewDate(time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)), "2026-03-15"},
		{normalize.NewTimestamp(time.Date(2026, 3, 15, 9, 30, 0, 0, time.UTC)), "2026-03-15 09:30:00"},
		{3.25, "3.25"},
	}
	for _, tc := range tests {
		if got := valueText(tc.in); got != tc.want {
			t.Errorf("valueText(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
