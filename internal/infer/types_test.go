package infer

import "testing"

func TestSQLiteDecimalPrecision(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{Type{Base: Decimal, Precision: 15, Scale: 2}, "NUMERIC(15,2)"},
		{Type{Base: Decimal, Precision: 16, Scale: 2}, "TEXT"},
		{Type{Base: Decimal, Precision: 38, Scale: 0}, "TEXT"},
	}
	for _, tt := range tests {
		if got := tt.typ.SQL(SQLite); got != tt.want {
			t.Errorf("%v.SQL(sqlite) = %q, want %q", tt.typ, got, tt.want)
		}
		if got := tt.typ.SQL(Postgres); got == "TEXT" {
			t.Errorf("%v.SQL(postgres) must stay NUMERIC", tt.typ)
		}
	}
}
