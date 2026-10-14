package normalize

import (
	"math"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

const tsLayout = "2006-01-02 15:04:05.000"

func TestParseDate(t *testing.T) {
	tests := []struct {
		in      string
		order   DateOrder
		want    string // tsLayout; "" = rejected
		hasTime bool
		numeric bool
		p1, p2  int
		layout  string
	}{
		// ISO.
		{"2026-03-15", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "YYYY-MM-DD"},
		{"2026-03-15", DMY, "2026-03-15 00:00:00.000", false, false, 0, 0, "YYYY-MM-DD"},
		{"2026/03/15", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "YYYY/MM/DD"},
		{"2026-3-5", MDY, "2026-03-05 00:00:00.000", false, false, 0, 0, "YYYY-MM-DD"},
		{"  2026-03-15  ", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "YYYY-MM-DD"},
		{"2026-03-15T10:30:00", MDY, "2026-03-15 10:30:00.000", true, false, 0, 0, "YYYY-MM-DDTHH:MM:SS"},
		{"2026-03-15 10:30", MDY, "2026-03-15 10:30:00.000", true, false, 0, 0, "YYYY-MM-DD HH:MM"},
		{"2026-03-15 10:30:00", MDY, "2026-03-15 10:30:00.000", true, false, 0, 0, "YYYY-MM-DD HH:MM:SS"},
		{"2026-03-15 10:30:00.250Z", MDY, "2026-03-15 10:30:00.250", true, false, 0, 0, "YYYY-MM-DD HH:MM:SS.FFFZ"},
		{"2026-03-15T23:59:59Z", MDY, "2026-03-15 23:59:59.000", true, false, 0, 0, "YYYY-MM-DDTHH:MM:SSZ"},
		{"2026-03-15T00:00:00Z", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "YYYY-MM-DDTHH:MM:SSZ"},
		{"2026-03-15 00:00:00", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "YYYY-MM-DD HH:MM:SS"},
		// Numeric day/month forms.
		{"03/15/2026", MDY, "2026-03-15 00:00:00.000", false, true, 3, 15, "MM/DD/YYYY"},
		{"3/15/26", MDY, "2026-03-15 00:00:00.000", false, true, 3, 15, "M/D/YY"},
		{"3/15/2026", MDY, "2026-03-15 00:00:00.000", false, true, 3, 15, "M/D/YYYY"},
		{"3-15-2026", MDY, "2026-03-15 00:00:00.000", false, true, 3, 15, "M-D-YYYY"},
		{"3-4-26", MDY, "2026-03-04 00:00:00.000", false, true, 3, 4, "M-D-YY"},
		{"03/04/2026", MDY, "2026-03-04 00:00:00.000", false, true, 3, 4, "MM/DD/YYYY"},
		{"03/04/2026", DMY, "2026-04-03 00:00:00.000", false, true, 3, 4, "DD/MM/YYYY"},
		{"15.03.2026", DMY, "2026-03-15 00:00:00.000", false, true, 15, 3, "DD.MM.YYYY"},
		{"04.03.2026", MDY, "2026-04-03 00:00:00.000", false, true, 4, 3, "MM.DD.YYYY"},
		{"02/29/2024", MDY, "2024-02-29 00:00:00.000", false, true, 2, 29, "MM/DD/YYYY"},
		{"1/1/68", MDY, "2068-01-01 00:00:00.000", false, true, 1, 1, "M/D/YY"},
		{"1/1/69", MDY, "1969-01-01 00:00:00.000", false, true, 1, 1, "M/D/YY"},
		{"1/1/00", MDY, "2000-01-01 00:00:00.000", false, true, 1, 1, "M/D/YY"},
		{"12/31/99", MDY, "1999-12-31 00:00:00.000", false, true, 12, 31, "MM/DD/YY"},
		{"31/12/68", DMY, "2068-12-31 00:00:00.000", false, true, 31, 12, "DD/MM/YY"},
		{"15-Mar-68", MDY, "2068-03-15 00:00:00.000", false, false, 0, 0, "D-Mon-YY"},
		{"\u00a03/15/2026\u00a0", MDY, "2026-03-15 00:00:00.000", false, true, 3, 15, "M/D/YYYY"},
		{"2026-03-15 10:30:00.5", MDY, "2026-03-15 10:30:00.500", true, false, 0, 0, "YYYY-MM-DD HH:MM:SS.F"},
		{"2026-03-15T10:30:00.123Z", MDY, "2026-03-15 10:30:00.123", true, false, 0, 0, "YYYY-MM-DDTHH:MM:SS.FFFZ"},
		{"3/15/2026 12:00 AM", MDY, "2026-03-15 00:00:00.000", false, true, 3, 15, "M/D/YYYY H:MM AM/PM"},
		{"3/15/2026 14:30", MDY, "2026-03-15 14:30:00.000", true, true, 3, 15, "M/D/YYYY HH:MM"},
		{"3/15/2026 2:30 PM", MDY, "2026-03-15 14:30:00.000", true, true, 3, 15, "M/D/YYYY H:MM AM/PM"},
		{"3/15/2026 2:30pm", MDY, "2026-03-15 14:30:00.000", true, true, 3, 15, "M/D/YYYY H:MM AM/PM"},
		{"3/15/2026 12:05 am", MDY, "2026-03-15 00:05:00.000", true, true, 3, 15, "M/D/YYYY H:MM AM/PM"},
		{"3/15/2026 12:00 PM", MDY, "2026-03-15 12:00:00.000", true, true, 3, 15, "M/D/YYYY H:MM AM/PM"},
		{"3/15/2026 9:15:30 p.m.", MDY, "2026-03-15 21:15:30.000", true, true, 3, 15, "M/D/YYYY H:MM:SS AM/PM"},
		// Month names.
		{"Mar 15, 2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "Mon D, YYYY"},
		{"Mar 15, 2026", DMY, "2026-03-15 00:00:00.000", false, false, 0, 0, "Mon D, YYYY"},
		{"March 15 2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "Month D YYYY"},
		{"MAR 15 2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "Mon D YYYY"},
		{"Mar. 15,2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "Mon D, YYYY"},
		{"Sept 5, 2026", MDY, "2026-09-05 00:00:00.000", false, false, 0, 0, "Mon D, YYYY"},
		{"December 31, 1999", MDY, "1999-12-31 00:00:00.000", false, false, 0, 0, "Month D, YYYY"},
		{"Mar 15, 2026 10:30 AM", MDY, "2026-03-15 10:30:00.000", true, false, 0, 0, "Mon D, YYYY H:MM AM/PM"},
		{"15-Mar-2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "D-Mon-YYYY"},
		{"15 Mar 2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "D Mon YYYY"},
		{"15-Mar-26", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "D-Mon-YY"},
		{"15-mar-69", MDY, "1969-03-15 00:00:00.000", false, false, 0, 0, "D-Mon-YY"},
		{"15 March 2026", DMY, "2026-03-15 00:00:00.000", false, false, 0, 0, "D Month YYYY"},
		{"1 May 2026", MDY, "2026-05-01 00:00:00.000", false, false, 0, 0, "D Mon YYYY"},
		{"15th March 2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "D Month YYYY"},
		{"15  Mar   2026", MDY, "2026-03-15 00:00:00.000", false, false, 0, 0, "D Mon YYYY"},

		// Rejected.
		{"", MDY, "", false, false, 0, 0, ""},
		{"   ", MDY, "", false, false, 0, 0, ""},
		{"hello", MDY, "", false, false, 0, 0, ""},
		{"2026", MDY, "", false, false, 0, 0, ""},
		{"46096", MDY, "", false, false, 0, 0, ""},
		{"3/15", MDY, "", false, false, 0, 0, ""},
		{"March 2026", MDY, "", false, false, 0, 0, ""},
		{"2026-02-30", MDY, "", false, false, 0, 0, ""},
		{"2026-13-01", MDY, "", false, false, 0, 0, ""},
		{"2026-00-10", MDY, "", false, false, 0, 0, ""},
		{"0999-01-01", MDY, "", false, false, 0, 0, ""},
		{"2026-03/15", MDY, "", false, false, 0, 0, ""},
		{"02/30/2026", MDY, "", false, false, 0, 0, ""},
		{"02/29/2025", MDY, "", false, false, 0, 0, ""},
		{"13/01/2026", MDY, "", false, false, 0, 0, ""},
		{"15/03/2026", MDY, "", false, false, 0, 0, ""},
		{"01/13/2026", DMY, "", false, false, 0, 0, ""},
		{"32/01/2026", DMY, "", false, false, 0, 0, ""},
		{"3/15-2026", MDY, "", false, false, 0, 0, ""},
		{"15.03.26", DMY, "", false, false, 0, 0, ""},
		{"1.2.10", MDY, "", false, false, 0, 0, ""},
		{"Foo 15, 2026", MDY, "", false, false, 0, 0, ""},
		{"15-Foo-2026", MDY, "", false, false, 0, 0, ""},
		{"Mar 32, 2026", MDY, "", false, false, 0, 0, ""},
		{"Mar 15 26", MDY, "", false, false, 0, 0, ""},
		{"2026-03-15 25:00", MDY, "", false, false, 0, 0, ""},
		{"02/29/2025", DMY, "", false, false, 0, 0, ""},
		{"29/02/2025", DMY, "", false, false, 0, 0, ""},
		{"13/13/2026", MDY, "", false, false, 0, 0, ""},
		{"13/13/2026", DMY, "", false, false, 0, 0, ""},
		{"3/15/2026 0:30 AM", MDY, "", false, false, 0, 0, ""},
		{"3/15/2026T10:30", MDY, "", false, false, 0, 0, ""},
		{"2026-03-15Z", MDY, "", false, false, 0, 0, ""},
		{"2026-03-15T10:30:00+02:00", MDY, "", false, false, 0, 0, ""},
		{"2026-03-15 10:30:00.1234567891", MDY, "", false, false, 0, 0, ""},
		{"2026-03-15 10:60", MDY, "", false, false, 0, 0, ""},
		{"2026-03-15 10:30 +02:00", MDY, "", false, false, 0, 0, ""},
		{"3/15/2026 13:30 PM", MDY, "", false, false, 0, 0, ""},
		{"3/15/2026 0:30 AM", MDY, "", false, false, 0, 0, ""},
		{"3/15/2026T10:30", MDY, "", false, false, 0, 0, ""},
		{"3/15/2026 10:30Z", MDY, "", false, false, 0, 0, ""},
		{"$1,200.50", MDY, "", false, false, 0, 0, ""},
	}
	for _, tc := range tests {
		got, ok := ParseDate(tc.in, tc.order)
		if ok != (tc.want != "") {
			t.Errorf("ParseDate(%q, %v) ok = %v, want %v (got %+v)", tc.in, tc.order, ok, !ok, got)
			continue
		}
		if !ok {
			continue
		}
		if s := got.T.Format(tsLayout); s != tc.want || got.T.Location() != time.UTC {
			t.Errorf("ParseDate(%q, %v).T = %s %v, want %s UTC", tc.in, tc.order, s, got.T.Location(), tc.want)
		}
		if got.HasTime != tc.hasTime || got.Numeric != tc.numeric || got.P1 != tc.p1 || got.P2 != tc.p2 || got.Layout != tc.layout {
			t.Errorf("ParseDate(%q, %v) = {HasTime:%v Numeric:%v P1:%d P2:%d Layout:%q}, want {HasTime:%v Numeric:%v P1:%d P2:%d Layout:%q}",
				tc.in, tc.order, got.HasTime, got.Numeric, got.P1, got.P2, got.Layout,
				tc.hasTime, tc.numeric, tc.p1, tc.p2, tc.layout)
		}
	}
}

func TestExcelSerial(t *testing.T) {
	tests := []struct {
		v        float64
		date1904 bool
		want     string // tsLayout; "" = rejected
		hasTime  bool
	}{
		{46096, false, "2026-03-15 00:00:00.000", false},
		{46096.5, false, "2026-03-15 12:00:00.000", true},
		{46096.75, false, "2026-03-15 18:00:00.000", true},
		{46096.999999999, false, "2026-03-16 00:00:00.000", false},
		{45000.000005, false, "2023-03-15 00:00:00.000", false},
		{45000.0000058, false, "2023-03-15 00:00:01.000", true},
		{1, false, "1900-01-01 00:00:00.000", false},
		{59, false, "1900-02-28 00:00:00.000", false},
		{61, false, "1900-03-01 00:00:00.000", false},
		{0.5, false, "1899-12-31 12:00:00.000", true},
		{2958465, false, "9999-12-31 00:00:00.000", false},
		{44634, true, "2026-03-15 00:00:00.000", false},
		{1, true, "1904-01-02 00:00:00.000", false},
		{60, false, "", false},
		{60.5, false, "", false},
		{60, true, "1904-03-01 00:00:00.000", false},
		{59.75, false, "1900-02-28 18:00:00.000", true},
		{0, false, "", false},
		{-1, false, "", false},
		{2958466, false, "", false},
		{2958465, true, "", false},
		{math.NaN(), false, "", false},
		{math.Inf(1), false, "", false},
	}
	for _, tc := range tests {
		got, hasTime, ok := ExcelSerial(tc.v, tc.date1904)
		if ok != (tc.want != "") {
			t.Errorf("ExcelSerial(%v, %v) ok = %v, want %v", tc.v, tc.date1904, ok, !ok)
			continue
		}
		if !ok {
			continue
		}
		if s := got.Format(tsLayout); s != tc.want || hasTime != tc.hasTime || got.Location() != time.UTC {
			t.Errorf("ExcelSerial(%v, %v) = (%s, %v), want (%s, %v)", tc.v, tc.date1904, s, hasTime, tc.want, tc.hasTime)
		}
	}
}

// TestExcelSerialMatchesExcelize cross-checks the conversion against
// excelize for serials past Excel's 1900 leap-year bug.
func TestExcelSerialMatchesExcelize(t *testing.T) {
	for _, date1904 := range []bool{false, true} {
		for v := 61.0; v < 80000; v += 997.25 {
			got, _, ok := ExcelSerial(v, date1904)
			want, err := excelize.ExcelDateToTime(v, date1904)
			if !ok || err != nil || !got.Equal(want.Round(time.Second)) {
				t.Errorf("ExcelSerial(%v, %v) = %v (ok=%v), excelize says %v", v, date1904, got, ok, want)
			}
		}
	}
}

func TestPlausibleSerial(t *testing.T) {
	tests := []struct {
		v    float64
		want bool
	}{
		{20000, true},
		{46096, true},
		{46096.5, true},
		{80000, true},
		{19999.99, false},
		{80000.5, false},
		{0, false},
		{1, false},
		{-46096, false},
		{2958465, false},
		{math.NaN(), false},
	}
	for _, tc := range tests {
		if got := PlausibleSerial(tc.v); got != tc.want {
			t.Errorf("PlausibleSerial(%v) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestDateHint(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"Order Date", true},
		{"DATE", true},
		{"Invoice Date", true},
		{"Day of week", true},
		{"Month", true},
		{"Period", true},
		{"Fiscal period end", true},
		{"Birthday", false}, {"Monthly Revenue", false}, {"Weekday Visitors", false}, {"Holiday Bonus", false}, {"InvoiceDate", true}, {"order_dt", true},
		{"Amount", false},
		{"Year", false},
		{"Time", false},
		{"Dt", true},
		{"Order ID", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := DateHint(tc.header); got != tc.want {
			t.Errorf("DateHint(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}
