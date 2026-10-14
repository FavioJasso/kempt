package normalize

import (
	"strings"
	"testing"
)

func TestSnakeCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"FY2025 Budget", "fy2025_budget"},
		{"Order #", "order_num"},
		{"% Change", "pct_change"},
		{"OrderID", "order_id"},
		{"Café Sales", "cafe_sales"},
		{"  multiple   spaces ", "multiple_spaces"},
		{"2025 Revenue", "c_2025_revenue"},
		{"R&D", "r_and_d"},
		{"Unit Price ($)", "unit_price"},
		{"", ""},
		{"orderDate", "order_date"},
		{"HTTPServer", "http_server"},
		{"IDs", "ids"},
		{"OrderIDs", "order_ids"},
		{"Q1 Revenue", "q1_revenue"},
		{"Q1Revenue", "q1_revenue"},
		{"FY2025Q1", "fy2025q1"},
		{"Gross Margin %", "gross_margin_pct"},
		{"A&B #2", "a_and_b_num_2"},
		{"Straße", "strasse"},
		{"Ørsted Øre", "orsted_ore"},
		{"naïve_café", "naive_cafe"},
		{"Ünit-Price", "unit_price"},
		{"__x__", "x"},
		{"Revenue / Expense", "revenue_expense"},
		{"already_snake", "already_snake"},
		{"UPPER CASE", "upper_case"},
		{"e-mail", "e_mail"},
		{"123", "c_123"},
		{"日本", ""},
		{"$", ""},
		{"Department\nName", "department_name"},
		{"\ufb01le Size", "file_size"},
		{"Area (m\u00b2)", "area_m2"},
		{"\uff21\uff22\uff23 Total", "abc_total"},
		{"Growth \uff05", "growth_pct"},
	}
	for _, tc := range tests {
		if got := SnakeCase(tc.in); got != tc.want {
			t.Errorf("SnakeCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Budget Summary", "budget_summary"},
		{"Orders", "orders"},
		{"Sheet1", "sheet1"},
		{"My-Sheet (1)", "my_sheet_1"},
		{"2025 Q1", "c_2025_q1"},
		{"R&D Budget", "r_and_d_budget"},
		{"Café", "cafe"},
		{"ambiguous_dates", "ambiguous_dates"},
		{"", "sheet"},
		{"   ", "sheet"},
		{"日本", "sheet"},
		{"---", "sheet"},
	}
	for _, tc := range tests {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestColumnBase(t *testing.T) {
	tests := []struct {
		parts              []string
		letter             string
		name, unit, source string
	}{
		{[]string{"Revenue"}, "A", "revenue", "", ""},
		{[]string{"Sales ($000s)"}, "B", "sales", "$000s", "Sales ($000s)"},
		{[]string{"FY2025 ($000s)", "Budget"}, "C", "fy2025_budget", "$000s", "FY2025 ($000s)"},
		{[]string{"Q1", "Revenue"}, "D", "q1_revenue", "", ""},
		{[]string{"Change (%)"}, "E", "change", "%", "Change (%)"},
		{[]string{""}, "F", "col_f", "", ""},
		{nil, "AB", "col_ab", "", ""},
		{[]string{"***"}, "G", "col_g", "", ""},
		{[]string{"2025 Revenue"}, "H", "c_2025_revenue", "", ""},
		{[]string{"Energy [kWh]"}, "I", "energy", "kWh", "Energy [kWh]"},
		{[]string{"", "Name"}, "J", "name", "", ""},
	}
	for _, tt := range tests {
		name, unit, src := ColumnBase(tt.parts, tt.letter)
		if name != tt.name || unit != tt.unit || src != tt.source {
			t.Errorf("ColumnBase(%q, %s) = %q, %q, %q; want %q, %q, %q", tt.parts, tt.letter, name, unit, src, tt.name, tt.unit, tt.source)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly_ten", 11, "exactly_ten"},
		{"abcdef", 3, "abc"},
		{"ab_cdef", 3, "ab"}, // trailing underscore dropped
		{"ab__", 4, "ab__"},  // within the limit: unchanged
		{"ab日本", 4, "ab"},    // no split UTF-8 sequence
		{"ab日本", 5, "ab日"},
		{"日本", 1, ""},
		{"", 5, ""},
		{"a_b_c_d", 4, "a_b"},
	}
	for _, tt := range tests {
		if got := Truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestUnique(t *testing.T) {
	seen := map[string]bool{}
	long := strings.Repeat("a", 70)
	tests := []struct {
		in, want string
	}{
		{"name", "name"},
		{"name", "name_2"},
		{"name", "name_3"},
		{"name_2", "name_2_2"}, // name_2 was taken by the repeat above
		{"other", "other"},
		{long, strings.Repeat("a", MaxIdent)},
		{long, strings.Repeat("a", MaxIdent-2) + "_2"},
		{"x_", "x_"},
		{"x", "x"},
		{"x", "x_2"},
	}
	for _, tt := range tests {
		if got := Unique(tt.in, seen, MaxIdent); got != tt.want {
			t.Errorf("Unique(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
