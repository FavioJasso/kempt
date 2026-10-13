package normalize

import "testing"

func TestExtractUnit(t *testing.T) {
	tests := []struct{ in, rest, unit string }{
		{"Revenue ($000s)", "Revenue", "$000s"},
		{"FY2025 ($000s)", "FY2025", "$000s"},
		{"Spend ($000's)", "Spend", "$000's"},
		{"Amount (USD)", "Amount", "USD"},
		{"Change (%)", "Change", "%"},
		{"Energy (kWh)", "Energy", "kWh"},
		{"Energy (KWH)", "Energy", "KWH"},
		{"Revenue in thousands", "Revenue", "thousands"},
		{"Revenue, in millions", "Revenue", "millions"},
		{"Debt In Billions", "Debt", "billions"},
		{"Sales (in thousands)", "Sales", "thousands"},
		{"Mass [kg]", "Mass", "kg"},
		{"Population (2020)", "Population (2020)", ""},
		{"Notes (see below)", "Notes (see below)", ""},
		{"Cost (estimated)", "Cost (estimated)", ""},
		{"Unit Price ($)", "Unit Price", "$"},
		{"Price ($/unit)", "Price", "$/unit"},
		{"Usage (kWh per day)", "Usage", "kWh per day"},
		{"Budget (€000)", "Budget", "€000"},
		{"Rent (£m)", "Rent", "£m"},
		{"Sales ($K)", "Sales", "$K"},
		{"Budget (USD 000s)", "Budget", "USD 000s"},
		{"Revenue (USD M)", "Revenue", "USD M"},
		{"Capex [EUR mn]", "Capex", "EUR mn"},
		{"Rate ($/unit-of-measure)", "Rate ($/unit-of-measure)", ""},
		{"Cost (usd)", "Cost (usd)", ""},
		{"Revenue ( USD )", "Revenue", "USD"},
		{"Distance (km)", "Distance", "km"},
		{"Temp (°F)", "Temp", "°F"},
		{"Area (sq ft)", "Area", "sq ft"},
		{"Volume (m³)", "Volume", "m³"},
		{"Duration (hrs)", "Duration", "hrs"},
		{"Load (MWh) (2020)", "Load (2020)", "MWh"},
		{"Revenue - (USD)", "Revenue", "USD"},
		{"(%)", "", "%"},
		{"Product(s)", "Product(s)", ""},
		{"Status (Y/N)", "Status (Y/N)", ""},
		{"Date (mm/dd/yyyy)", "Date (mm/dd/yyyy)", ""},
		{"Share (% of total)", "Share (% of total)", ""},
		{"Sales (PA)", "Sales (PA)", ""},
		{"Within thousands", "Within thousands", ""},
		{"Margin in thousands of units", "Margin in thousands of units", ""},
		{"  Revenue   Total  ", "Revenue Total", ""},
		{"", "", ""},
	}
	for _, tc := range tests {
		rest, unit := ExtractUnit(tc.in)
		if rest != tc.rest || unit != tc.unit {
			t.Errorf("ExtractUnit(%q) = (%q, %q), want (%q, %q)", tc.in, rest, unit, tc.rest, tc.unit)
		}
	}
}
