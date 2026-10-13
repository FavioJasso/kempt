package normalize

import "testing"

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
