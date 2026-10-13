package normalize

import "testing"

func TestClassifyNull(t *testing.T) {
	tests := []struct {
		in   string
		want NullKind
	}{
		{"", NullEmpty},
		{"   ", NullEmpty},
		{"\u00a0\t", NullEmpty},
		{"\ufeff", NullEmpty},
		{"-", NullToken},
		{"–", NullToken},
		{"—", NullToken},
		{" — ", NullToken},
		{"N/A", NullToken},
		{"n/a", NullToken},
		{"NA", NullToken},
		{"na", NullToken},
		{"n.a.", NullToken},
		{"N.A.", NullToken},
		{"NULL", NullToken},
		{"None", NullToken},
		{"#N/A", NullExcelError},
		{"#n/a", NullExcelError},
		{"#DIV/0!", NullExcelError},
		{" #div/0! ", NullExcelError},
		{"#VALUE!", NullExcelError},
		{"#REF!", NullExcelError},
		{"#NAME?", NullExcelError},
		{"#NUM!", NullExcelError},
		{"#NULL!", NullExcelError},
		{"#SPILL!", NullExcelError},
		{"#CALC!", NullExcelError},
		{"#GETTING_DATA", NullExcelError},
		{"0", NotNull},
		{"--", NotNull},
		{"n.a", NotNull},
		{"N/A.", NotNull},
		{"nan", NotNull},
		{"Nope", NotNull},
		{"#DIV/0", NotNull},
		{"Nashville", NotNull},
		{"see note", NotNull},
	}
	for _, tc := range tests {
		if got := ClassifyNull(tc.in); got != tc.want {
			t.Errorf("ClassifyNull(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNullKindString(t *testing.T) {
	tests := []struct {
		k    NullKind
		want string
	}{
		{NotNull, "not_null"},
		{NullEmpty, "empty"},
		{NullToken, "null_token"},
		{NullExcelError, "excel_error"},
		{NullKind(9), "null_kind(?)"},
	}
	for _, tc := range tests {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("NullKind(%d).String() = %q, want %q", int(tc.k), got, tc.want)
		}
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		in    string
		v, ok bool
	}{
		{"true", true, true},
		{"TRUE", true, true},
		{" Yes ", true, true},
		{"y", true, true},
		{"T", true, true},
		{"false", false, true},
		{"False", false, true},
		{"No", false, true},
		{"n", false, true},
		{"F", false, true},
		{"1", false, false},
		{"0", false, false},
		{"", false, false},
		{"yes!", false, false},
		{"maybe", false, false},
		{"on", false, false},
		{"off", false, false},
		{"tru", false, false},
		{"N/A", false, false},
	}
	for _, tc := range tests {
		v, ok := ParseBool(tc.in)
		if v != tc.v || ok != tc.ok {
			t.Errorf("ParseBool(%q) = (%v, %v), want (%v, %v)", tc.in, v, ok, tc.v, tc.ok)
		}
	}
}
