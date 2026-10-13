package dsn

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestResolve(t *testing.T) {
	const (
		pg     = "postgres://kempt:s3cret@db.example.com:5432/app"
		pgPub  = "postgresql://postgres:pub@roundhouse.proxy.rlwy.net:41234/railway"
		pgPriv = "postgresql://postgres:priv@postgres.railway.internal:5432/railway"
	)
	tests := []struct {
		name       string
		flag       string
		env        map[string]string
		wantSource string
		wantDial   string
		wantDSN    string
		wantErr    string // substring; "ErrNoDSN" checks errors.Is
	}{
		{"flag wins over everything", pg,
			map[string]string{"KEMPT_DSN": "x.db", "DATABASE_PUBLIC_URL": pgPub, "DATABASE_URL": pgPriv},
			"--dsn", "postgres", pg, ""},
		{"KEMPT_DSN before Railway vars", "",
			map[string]string{"KEMPT_DSN": "sqlite://local.db", "DATABASE_PUBLIC_URL": pgPub, "DATABASE_URL": pgPriv},
			"$KEMPT_DSN", "sqlite", "local.db", ""},
		{"public URL before private URL", "",
			map[string]string{"DATABASE_PUBLIC_URL": pgPub, "DATABASE_URL": pgPriv},
			"$DATABASE_PUBLIC_URL", "postgres", pgPub, ""},
		{"private URL last", "",
			map[string]string{"DATABASE_URL": pgPriv},
			"$DATABASE_URL", "postgres", pgPriv, ""},
		{"blank flag skipped", "   ",
			map[string]string{"DATABASE_URL": pgPriv},
			"$DATABASE_URL", "postgres", pgPriv, ""},
		{"whitespace env values skipped", "",
			map[string]string{"KEMPT_DSN": " \t", "DATABASE_PUBLIC_URL": "\n", "DATABASE_URL": pgPriv},
			"$DATABASE_URL", "postgres", pgPriv, ""},
		{"trailing newline trimmed", "",
			map[string]string{"KEMPT_DSN": pg + "\n"},
			"$KEMPT_DSN", "postgres", pg, ""},
		{"mysql flag converted", "mysql://root:pw@mysql.example.com/shop", nil,
			"--dsn", "mysql", "root:pw@tcp(mysql.example.com:3306)/shop?parseTime=true", ""},
		{"nothing set", "", nil, "", "", "", "ErrNoDSN"},
		{"only blanks", "  ", map[string]string{"KEMPT_DSN": " ", "DATABASE_PUBLIC_URL": "", "DATABASE_URL": "\t"},
			"", "", "", "ErrNoDSN"},
		{"bad flag names its source", "redis://u:hunter2@cache:6379", nil,
			"", "", "", "--dsn: unsupported DSN"},
		{"bad higher source does not fall through", "",
			map[string]string{"KEMPT_DSN": "mongodb://u:hunter2@h/db", "DATABASE_URL": pgPriv},
			"", "", "", "$KEMPT_DSN: unsupported DSN"},
		{"bad public URL reported", "",
			map[string]string{"DATABASE_PUBLIC_URL": "mysql://u:hunter2@h/db?parseTime=maybe"},
			"", "", "", "$DATABASE_PUBLIC_URL: mysql DSN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.flag, func(k string) string { return tt.env[k] })
			switch {
			case tt.wantErr == "ErrNoDSN":
				if !errors.Is(err, ErrNoDSN) {
					t.Fatalf("err = %v, want ErrNoDSN", err)
				}
				return
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("error leaks the password: %v", err)
				}
				return
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Source != tt.wantSource || got.Dialect != tt.wantDial || got.DSN != tt.wantDSN {
				t.Errorf("got {%s %s %q}, want {%s %s %q}", got.Source, got.Dialect, got.DSN, tt.wantSource, tt.wantDial, tt.wantDSN)
			}
			if strings.Contains(got.Redacted, "s3cret") || strings.Contains(got.Describe(), "pub@") {
				t.Errorf("password not redacted: %q", got.Describe())
			}
		})
	}
}

func TestResolveProcessEnv(t *testing.T) {
	t.Setenv("KEMPT_DSN", "")
	t.Setenv("DATABASE_PUBLIC_URL", "")
	t.Setenv("DATABASE_URL", "postgres://u:p@h/db")
	got, err := Resolve("", nil)
	if err != nil || got.Source != "$DATABASE_URL" {
		t.Fatalf("Resolve(nil getenv) = %+v, %v", got, err)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		raw      string
		dialect  string
		dsn      string
		redacted string
		wantErr  string
	}{
		// Postgres URLs pass through.
		{raw: "postgres://u:p@localhost:5432/db?sslmode=disable", dialect: "postgres",
			dsn: "postgres://u:p@localhost:5432/db?sslmode=disable", redacted: "postgres://u:****@localhost:5432/db?sslmode=disable"},
		{raw: "postgresql://postgres:abc@roundhouse.proxy.rlwy.net:41234/railway", dialect: "postgres",
			dsn: "postgresql://postgres:abc@roundhouse.proxy.rlwy.net:41234/railway", redacted: "postgresql://postgres:****@roundhouse.proxy.rlwy.net:41234/railway"},
		{raw: "  postgres:///kempt  ", dialect: "postgres", dsn: "postgres:///kempt", redacted: "postgres:///kempt"},
		{raw: "POSTGRES://u@h/db", dialect: "postgres", dsn: "postgres://u@h/db", redacted: "POSTGRES://u@h/db"},

		// MySQL URLs become go-sql-driver DSNs.
		{raw: "mysql://user:pass@host:3307/db", dialect: "mysql",
			dsn: "user:pass@tcp(host:3307)/db?parseTime=true", redacted: "mysql://user:****@host:3307/db"},
		{raw: "mysql://root:pw@mysql.railway.internal/railway", dialect: "mysql",
			dsn: "root:pw@tcp(mysql.railway.internal:3306)/railway?parseTime=true"},
		{raw: "mysql://us%40er:p%40ss%2Fw%3Ard@h/db", dialect: "mysql",
			dsn: "us@er:p@ss/w:rd@tcp(h:3306)/db?parseTime=true", redacted: "mysql://us%40er:****@h/db"},
		{raw: "mysql://u:p@h/db?x=y&charset=utf8mb4,utf8&loc=Europe%2FParis", dialect: "mysql",
			dsn: "u:p@tcp(h:3306)/db?charset=utf8mb4,utf8&loc=Europe%2FParis&parseTime=true&x=y"},
		{raw: "mysql://u:p@h/db?loc=Europe/Paris", dialect: "mysql",
			dsn: "u:p@tcp(h:3306)/db?loc=Europe%2FParis&parseTime=true"},
		{raw: "mysql://u:p@h/db?parseTime=false", dialect: "mysql", dsn: "u:p@tcp(h:3306)/db"},
		{raw: "mysql://u:p@h:25060/defaultdb?ssl-mode=REQUIRED", dialect: "mysql",
			dsn: "u:p@tcp(h:25060)/defaultdb?parseTime=true&tls=skip-verify"},
		{raw: "mysql://u:p@h/db?tls=true&sslmode=disabled", dialect: "mysql",
			dsn: "u:p@tcp(h:3306)/db?parseTime=true&tls=true"},
		{raw: "mysql://u@[::1]/db", dialect: "mysql", dsn: "u@tcp([::1]:3306)/db?parseTime=true"},
		{raw: "mysql://u:p@h", dialect: "mysql", dsn: "u:p@tcp(h:3306)/?parseTime=true"},
		{raw: "mysql:///db", dialect: "mysql", dsn: "tcp(127.0.0.1:3306)/db?parseTime=true"},
		{raw: "MySQL://u:p@h/my%20db", dialect: "mysql", dsn: "u:p@tcp(h:3306)/my%20db?parseTime=true"},
		{raw: "mysql://u:hunter2@h/db?parseTime=maybe", wantErr: "invalid bool value"},
		{raw: "mysql://u:hunter2@h/db?strict=true", wantErr: "strict"},
		{raw: "mysql://u:hunter2@h/db?ssl-mode=sometimes", wantErr: "unknown ssl-mode"},
		{raw: "mysql://u:hunter2@h/db?tls=custom", wantErr: "unknown config name"},
		{raw: "mysql://u:hun#ter2@h/db", wantErr: "percent-encode"},
		{raw: "mysql://u:hunter/2@h/db", wantErr: "percent-encode"},
		{raw: "mysql://u%3Ax:hunter2@h/db", wantErr: "':'"},
		{raw: "mysql://u:hunter2@h/a/b", wantErr: "contains '/'"},

		// SQLite.
		{raw: "sqlite://data/kempt.db", dialect: "sqlite", dsn: "data/kempt.db"},
		{raw: "sqlite:///abs/x.db", dialect: "sqlite", dsn: "/abs/x.db"},
		{raw: "sqlite:x.db", dialect: "sqlite", dsn: "x.db"},
		{raw: "sqlite::memory:", dialect: "sqlite", dsn: ":memory:"},
		{raw: "file:x.db?mode=ro", dialect: "sqlite", dsn: "file:x.db?mode=ro"},
		{raw: "./out/budget.db", dialect: "sqlite", dsn: "./out/budget.db"},
		{raw: "/var/lib/kempt.sqlite", dialect: "sqlite", dsn: "/var/lib/kempt.sqlite"},
		{raw: "Budget.SQLITE3", dialect: "sqlite", dsn: "Budget.SQLITE3"},
		{raw: `C:\data\kempt.db`, dialect: "sqlite", dsn: `C:\data\kempt.db`},
		{raw: "sqlite://", wantErr: "no file path"},

		// Everything else.
		{raw: "", wantErr: "empty DSN"},
		{raw: "   ", wantErr: "empty DSN"},
		{raw: "redis://u:hunter2@cache:6379", wantErr: `unsupported DSN "redis://u:****@cache:6379"`},
		{raw: "http://example.com/data.db", wantErr: "unsupported DSN"},
		{raw: "kempt.db.bak", wantErr: "unsupported DSN"},
		{raw: "host=h user=u password=hunter2 dbname=d", wantErr: "password=****"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("error leaks the password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Dialect != tt.dialect || got.DSN != tt.dsn || got.Source != "" {
				t.Errorf("got {%s %q %q}, want {%s %q}", got.Dialect, got.DSN, got.Source, tt.dialect, tt.dsn)
			}
			if tt.redacted != "" && got.Redacted != tt.redacted {
				t.Errorf("Redacted = %q, want %q", got.Redacted, tt.redacted)
			}
		})
	}
}

// TestParseMySQLRoundTrip checks that the driver reads the converted DSN
// back with the same credentials, even when they hold DSN delimiters.
func TestParseMySQLRoundTrip(t *testing.T) {
	tests := []struct{ raw, user, pass, addr, db string }{
		{"mysql://root:pw@h:3307/shop", "root", "pw", "h:3307", "shop"},
		{"mysql://u:p%40ss%2F%3F%28x%29@h/db", "u", "p@ss/?(x)", "h:3306", "db"},
		{"mysql://u:a%2Fb%40c@h/d?loc=UTC&x=1%2F2", "u", "a/b@c", "h:3306", "d"},
		{"mysql://u%40corp:%3A%3A@[2001:db8::1]:3306/x", "u@corp", "::", "[2001:db8::1]:3306", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := mysql.ParseDSN(got.DSN)
			if err != nil {
				t.Fatalf("driver rejects %q: %v", got.DSN, err)
			}
			if cfg.User != tt.user || cfg.Passwd != tt.pass || cfg.Addr != tt.addr || cfg.DBName != tt.db || !cfg.ParseTime {
				t.Errorf("driver read %q as user=%q pass=%q addr=%q db=%q parseTime=%v", got.DSN, cfg.User, cfg.Passwd, cfg.Addr, cfg.DBName, cfg.ParseTime)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"postgres://kempt:s3cret@db:5432/app", "postgres://kempt:****@db:5432/app"},
		{"postgresql://postgres:abc@roundhouse.proxy.rlwy.net:41234/railway?sslmode=require",
			"postgresql://postgres:****@roundhouse.proxy.rlwy.net:41234/railway?sslmode=require"},
		{"postgres://kempt@db/app", "postgres://kempt@db/app"},
		{"postgres://kempt:@db/app", "postgres://kempt:@db/app"},
		{"postgres://db:5432/app", "postgres://db:5432/app"},
		{"postgres://db/app?options=a@b", "postgres://db/app?options=a@b"},
		{"mysql://root:p%40ss@h:3306/db", "mysql://root:****@h:3306/db"},
		{"mysql://root:p@ss@h/db", "mysql://root:****@h/db"},
		{"mysql://root:pa/ss?#x@h/db", "mysql://root:****@h/db"},
		{"postgres://admin@corp:pw@h/db", "postgres://admin@corp:****@h/db"},
		{"postgres://u:pw@h1:5432,h2:5432/db", "postgres://u:****@h1:5432,h2:5432/db"},
		{"postgres://h/db?user=u&password=hunter2&sslmode=require", "postgres://h/db?user=u&password=****&sslmode=require"},
		{"user:pass@tcp(host:3306)/db?parseTime=true", "user:****@tcp(host:3306)/db?parseTime=true"},
		{"user:p@ss/w:rd@tcp(h:3306)/db?parseTime=true", "user:****@tcp(h:3306)/db?parseTime=true"},
		{"user:pass@/db", "user:****@/db"},
		{"user@tcp(host:3306)/db", "user@tcp(host:3306)/db"},
		{"host=db user=kempt password=hunter2 dbname=app", "host=db user=kempt password=**** dbname=app"},
		{"host=db password = 'two words\\' here' dbname=app", "host=db password = **** dbname=app"},
		{"Server=db;Uid=kempt;Pwd=hunter2;Database=app", "Server=db;Uid=kempt;Pwd=****;Database=app"},
		{`Password="a;b";Host=db`, `Password=****;Host=db`},
		{"sslpassword=k3y host=db", "sslpassword=**** host=db"},
		{"sqlite:///home/u@x/data.db", "sqlite:///home/u@x/data.db"},
		{"C:/Users/a@b/data.db", "C:/Users/a@b/data.db"},
		{"./kempt.db", "./kempt.db"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := Redact(tt.raw); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		t    Target
		want string
	}{
		{Target{Dialect: "postgres", Source: "$DATABASE_PUBLIC_URL", Redacted: "postgres://user:****@host:5432/db"},
			"postgres DSN from $DATABASE_PUBLIC_URL: postgres://user:****@host:5432/db"},
		{Target{Dialect: "sqlite", Redacted: "x.db"}, "sqlite DSN: x.db"},
	}
	for _, tt := range tests {
		if got := tt.t.Describe(); got != tt.want {
			t.Errorf("Describe() = %q, want %q", got, tt.want)
		}
	}
}
