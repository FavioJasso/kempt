package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckQuery(t *testing.T) {
	tests := []struct {
		sql     string
		ok      bool
		stmt    string
		keyword string
	}{
		{"SELECT 1", true, "SELECT 1", "select"},
		{"select * from t;", true, "select * from t", "select"},
		{"  select 1 ;  -- trailing comment\n", true, "select 1", "select"},
		{"select 1; /* c */", true, "select 1", "select"},
		{"WITH x AS (SELECT 1) SELECT * FROM x", true, "WITH x AS (SELECT 1) SELECT * FROM x", "with"},
		{"(select 1)", true, "(select 1)", "select"},
		{"-- note\nselect 2", true, "-- note\nselect 2", "select"},
		{"/* a /* nested */ b */ values (1)", true, "/* a /* nested */ b */ values (1)", "values"},
		{"table budget", true, "table budget", "table"},
		{"explain select 1", true, "explain select 1", "explain"},
		{"select ';' as semi", true, "select ';' as semi", "select"},
		{`select "a;b" from t`, true, `select "a;b" from t`, "select"},
		{"select 'it''s; fine'", true, "select 'it''s; fine'", "select"},
		{`select E'\'; drop table x; --'`, true, `select E'\'; drop table x; --'`, "select"},
		{"select $$;$$", true, "select $$;$$", "select"},
		{"select $tag$ ; $$ ; $tag$", true, "select $tag$ ; $$ ; $tag$", "select"},
		{"select 1 -- ; drop table t", true, "select 1 -- ; drop table t", "select"},
		{"select 1; select 2", false, "", ""},
		{"select 1;drop table t", false, "", ""},
		{"select 1; ;", false, "", ""},
		{"drop table t", false, "", ""},
		{"insert into t values (1)", false, "", ""},
		{"update t set a = 1", false, "", ""},
		{"set role postgres", false, "", ""},
		{"copy t to program 'id'", false, "", ""},
		{"", false, "", ""},
		{";", false, "", ""},
		{"   -- only a comment", false, "", ""},
		{"select 'unterminated", false, "", ""},
		{"select /* unterminated", false, "", ""},
		{"select $x$ unterminated", false, "", ""},
		{"select a$b from t; delete from t", false, "", ""},
	}
	for _, tt := range tests {
		stmt, kw, err := checkQuery(tt.sql)
		if (err == nil) != tt.ok {
			t.Errorf("checkQuery(%q) err = %v, want ok=%v", tt.sql, err, tt.ok)
			continue
		}
		if tt.ok && (stmt != tt.stmt || kw != tt.keyword) {
			t.Errorf("checkQuery(%q) = %q, %q; want %q, %q", tt.sql, stmt, kw, tt.stmt, tt.keyword)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newLimiter(10, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d denied", i+1)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait <= 0 || wait > 6*time.Second {
		t.Fatalf("11th request: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("other clients have their own bucket")
	}
	now = now.Add(6 * time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("a token refills after 6s")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("only one token refilled")
	}
	now = now.Add(time.Hour)
	l.allow("c")
	if _, ok := l.buckets["a"]; ok {
		t.Fatal("idle buckets are collected")
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		remote, xff, want string
	}{
		{"10.0.0.1:5000", "", "10.0.0.1"},
		{"10.0.0.1:5000", "203.0.113.9", "203.0.113.9"},
		{"10.0.0.1:5000", "1.2.3.4, 203.0.113.9", "203.0.113.9"},
		{"[::1]:80", "", "::1"},
		{"bogus", "", "bogus"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := clientIP(r); got != tt.want {
			t.Errorf("clientIP(%q, %q) = %q, want %q", tt.remote, tt.xff, got, tt.want)
		}
	}
}
