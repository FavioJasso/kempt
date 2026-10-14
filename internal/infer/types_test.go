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

func TestMySQLLimits(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{Type{Base: Decimal, Precision: 32, Scale: 30}, "DECIMAL(32,30)"},
		{Type{Base: Decimal, Precision: 32, Scale: 31}, "TEXT"},
		{Type{Base: Text, MaxLen: 0}, "VARCHAR(1)"},
		{Type{Base: Text, MaxLen: 1024}, "VARCHAR(1024)"},
		{Type{Base: Text, MaxLen: 1025}, "TEXT"},
		{Type{Base: Text, MaxLen: 16383}, "TEXT"},
		{Type{Base: Text, MaxLen: 16384}, "MEDIUMTEXT"},
		{Type{Base: Text, MaxLen: 5 << 20}, "LONGTEXT"},
	}
	for _, tt := range tests {
		if got := tt.typ.SQL(MySQL); got != tt.want {
			t.Errorf("%+v.SQL(mysql) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}
