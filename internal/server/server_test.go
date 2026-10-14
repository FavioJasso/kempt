package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/FavioJasso/kempt/internal/dsn"
)

var testSamples = os.DirFS("../../testdata/fixtures")

func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.Samples == nil {
		cfg.Samples = testSamples
	}
	if cfg.Web == nil {
		cfg.Web = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>kempt</title>")}}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	return ts
}

// form builds a multipart body from fields; a field named "file" is sent as
// a file part named by fileName.
func form(t *testing.T, fileName string, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if k == "file" {
			fw, err := mw.CreateFormFile("file", fileName)
			if err != nil {
				t.Fatal(err)
			}
			fw.Write([]byte(v))
			continue
		}
		mw.WriteField(k, v)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func post(t *testing.T, url, ctype string, body io.Reader) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(url, ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res.StatusCode, out
}

func TestStaticAndConfig(t *testing.T) {
	ts := newTestServer(t, Config{Demo: true})
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "kempt") {
		t.Fatalf("GET / = %d %q", res.StatusCode, body)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP = %q", csp)
	}
	res, err = http.Get(ts.URL + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, res.StatusCode)
	}
	res, _ = http.Get(ts.URL + "/api/config")
	var cfg map[string]any
	json.NewDecoder(res.Body).Decode(&cfg)
	res.Body.Close()
	if cfg["loadEnabled"] != false || cfg["demo"] != true || cfg["maxUploadBytes"].(float64) != demoMaxUpload || cfg["maxRows"].(float64) != demoMaxRows {
		t.Fatalf("config %v", cfg)
	}
}

func TestSamples(t *testing.T) {
	ts := newTestServer(t, Config{})
	res, err := http.Get(ts.URL + "/api/samples")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Samples []sampleInfo `json:"samples"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if len(out.Samples) != 6 {
		t.Fatalf("samples %+v", out.Samples)
	}
	for _, s := range out.Samples {
		if s.Description == "" || s.Size == 0 {
			t.Errorf("sample %+v lacks description or size", s)
		}
	}
}

func TestInspectSample(t *testing.T) {
	ts := newTestServer(t, Config{})
	body, ct := form(t, "", map[string]string{"sample": "budget_report.xlsx"})
	status, out := post(t, ts.URL+"/api/inspect", ct, body)
	if status != 200 {
		t.Fatalf("status %d: %v", status, out)
	}
	sheets := out["sheets"].([]any)
	sh := sheets[0].(map[string]any)
	g := sh["grid"].(map[string]any)
	if g["rows"].(float64) != 19 || g["cols"].(float64) != 8 {
		t.Fatalf("grid %vx%v", g["rows"], g["cols"])
	}
	if merges := g["merges"].([]any); len(merges) != 7 {
		t.Fatalf("merges %v", merges)
	}
	roles := sh["cellRoles"].(map[string]any)
	if roles["A1"] != "title" || roles["C5"] != "header" || roles["A8"] != "total" || roles["A18"] != "footer" || roles["F14"] != "null" {
		t.Fatalf("roles A1=%v C5=%v A8=%v A18=%v F14=%v", roles["A1"], roles["C5"], roles["A8"], roles["A18"], roles["F14"])
	}
	ddl := sh["ddl"].(map[string]any)
	for _, d := range []string{"postgres", "mysql", "sqlite"} {
		if !strings.Contains(ddl[d].(string), "budget_summary") {
			t.Errorf("%s DDL: %v", d, ddl[d])
		}
	}
	preview := sh["preview"].([]any)
	if len(preview) != 7 {
		t.Fatalf("preview rows %d", len(preview))
	}
	decisions := sh["decisions"].([]any)
	if len(decisions) == 0 || strings.Contains(decisions[0].(map[string]any)["range"].(string), "!") {
		t.Fatalf("decisions %v", decisions)
	}
}

func TestInspectUploadAndOptions(t *testing.T) {
	ts := newTestServer(t, Config{})
	csv := "Name;Joined\nAnn;03/04/2026\nBob;05/06/2026\n"
	body, ct := form(t, "people.csv", map[string]string{"file": csv, "date_order": "dmy"})
	status, out := post(t, ts.URL+"/api/inspect", ct, body)
	if status != 200 {
		t.Fatalf("status %d: %v", status, out)
	}
	tbl := out["sheets"].([]any)[0].(map[string]any)["tables"].([]any)[0].(map[string]any)
	if tbl["table"] != "people" {
		t.Fatalf("table %v", tbl["table"])
	}
	row := tbl["preview"].([]any)[0].([]any)
	if row[1] != "2026-04-03" {
		t.Fatalf("dmy date = %v", row[1])
	}

	body, ct = form(t, "x.csv", map[string]string{"file": csv, "lenient": "7"})
	if status, _ := post(t, ts.URL+"/api/inspect", ct, body); status != 400 {
		t.Fatalf("bad lenient status %d", status)
	}
}

func TestInspectErrors(t *testing.T) {
	ts := newTestServer(t, Config{Demo: true})
	cases := []struct {
		name   string
		fields map[string]string
		file   string
		ctype  string
		want   int
	}{
		{"no file", map[string]string{"keep_totals": "1"}, "", "", 400},
		{"unknown sample", map[string]string{"sample": "../go.mod"}, "", "", 404},
		{"missing sample", map[string]string{"sample": "nope.xlsx"}, "", "", 404},
		{"legacy xls", map[string]string{"file": "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1rest"}, "old.xls", "", 400},
		{"too large", map[string]string{"file": strings.Repeat("a,b\n", demoMaxUpload/4+10)}, "big.csv", "", 413},
		{"too sparse", map[string]string{"file": "a\n" + strings.Repeat("\n", 3000) + strings.Repeat(",", 2000) + "z\n"}, "sparse.csv", "", 400},
		{"too many cells", map[string]string{"file": strings.Repeat(strings.Repeat("1,", 999)+"1\n", 2100)}, "wide.csv", "", 413},
		{"not multipart", nil, "", "application/json", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body io.Reader = strings.NewReader("{}")
			ct := c.ctype
			if c.fields != nil {
				b, mct := form(t, c.file, c.fields)
				body, ct = b, mct
			}
			status, out := post(t, ts.URL+"/api/inspect", ct, body)
			if status != c.want || out["error"] == "" {
				t.Fatalf("status %d (want %d): %v", status, c.want, out)
			}
		})
	}
}

func TestDemoRowLimit(t *testing.T) {
	ts := newTestServer(t, Config{Demo: true})
	var sb strings.Builder
	sb.WriteString("n\n")
	for i := 0; i < demoMaxRows+5; i++ {
		sb.WriteString("1\n")
	}
	body, ct := form(t, "rows.csv", map[string]string{"file": sb.String()})
	if status, out := post(t, ts.URL+"/api/inspect", ct, body); status != 413 {
		t.Fatalf("status %d: %v", status, out)
	}
}

func TestRateLimit(t *testing.T) {
	ts := newTestServer(t, Config{Demo: true})
	var last int
	for i := 0; i < demoRate+1; i++ {
		body, ct := form(t, "", map[string]string{"sample": "tidy.csv"})
		last, _ = post(t, ts.URL+"/api/inspect", ct, body)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("request %d status %d, want 429", demoRate+1, last)
	}
	// Reads that the page makes on load are not rate limited.
	for i := 0; i < 3; i++ {
		res, err := http.Get(ts.URL + "/api/samples")
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("samples after limit: %v %v", err, res.StatusCode)
		}
		res.Body.Close()
	}
}

func TestLoadDisabledWithoutDSN(t *testing.T) {
	ts := newTestServer(t, Config{})
	body, ct := form(t, "", map[string]string{"sample": "tidy.csv"})
	if status, _ := post(t, ts.URL+"/api/load", ct, body); status != http.StatusServiceUnavailable {
		t.Fatalf("load status %d", status)
	}
	status, _ := post(t, ts.URL+"/api/query", "application/json", strings.NewReader(`{"schema":"demo_aaaaaaaa","sql":"select 1"}`))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("query status %d", status)
	}
}

// TestPostgresLoadQuery exercises /api/load, /api/query and the janitor
// against a real database when KEMPT_TEST_PG is set.
func TestPostgresLoadQuery(t *testing.T) {
	url := os.Getenv("KEMPT_TEST_PG")
	if url == "" {
		t.Skip("KEMPT_TEST_PG not set")
	}
	target, err := dsn.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DSN: &target, Samples: testSamples})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, ct := form(t, "", map[string]string{"sample": "budget_report.xlsx"})
	status, out := post(t, ts.URL+"/api/load", ct, body)
	if status != 200 {
		t.Fatalf("load %d: %v", status, out)
	}
	schema := out["schema"].(string)
	if !schemaRe.MatchString(schema) || out["table"] != "budget_summary" || out["queryEnabled"] != true {
		t.Fatalf("load response %v", out)
	}
	defer func() {
		conn, err := s.pool.Acquire(context.Background())
		if err == nil {
			s.dropSchema(context.Background(), conn.Conn(), schema)
			conn.Release()
		}
	}()

	token, _ := out["token"].(string)
	if !tokenRe.MatchString(token) {
		t.Fatalf("load returned no token: %v", out)
	}
	queryAs := func(schema, token, sql string) (int, map[string]any) {
		b, _ := json.Marshal(queryRequest{Schema: schema, Token: token, SQL: sql})
		return post(t, ts.URL+"/api/query", "application/json", bytes.NewReader(b))
	}
	query := func(sql string) (int, map[string]any) { return queryAs(schema, token, sql) }
	status, out = query("SELECT department, sum(fy2025_budget) AS total FROM budget_summary GROUP BY 1 ORDER BY 1")
	if status != 200 {
		t.Fatalf("query %d: %v", status, out)
	}
	rows := out["rows"].([]any)
	if len(rows) != 3 || rows[0].([]any)[0] != "Parks & Recreation" || rows[0].([]any)[1] != "1526" {
		t.Fatalf("rows %v", rows)
	}

	for _, bad := range []string{
		"DELETE FROM budget_summary",
		"SELECT 1; DROP TABLE budget_summary",
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT set_config('role', 'postgres', true)",
		"WITH d AS (DELETE FROM budget_summary RETURNING *) SELECT * FROM d",
		"SELECT * FROM pg_sleep(5)",
		"SELECT * FROM public.kempt_should_not_exist",
	} {
		if status, out := query(bad); status == 200 {
			t.Errorf("%q succeeded: %v", bad, out)
		}
	}
	status, out = query("SELECT count(*) FROM budget_summary")
	if status != 200 || out["rows"].([]any)[0].([]any)[0] != "7" {
		t.Fatalf("table changed? %d %v", status, out)
	}
	status, out = query("SELECT g FROM generate_series(1, 500) g")
	if status != 200 || out["truncated"] != true || len(out["rows"].([]any)) != queryRows {
		t.Fatalf("truncation %d %v", status, out["truncated"])
	}

	// The schema name alone grants nothing: every role can list schema
	// names, so the token from /api/load is the credential.
	if status, _ := queryAs(schema, "", "SELECT 1"); status != http.StatusBadRequest {
		t.Errorf("no token: status %d", status)
	}
	if status, _ := queryAs(schema, strings.Repeat("0", 32), "SELECT 1"); status != http.StatusNotFound {
		t.Errorf("wrong token: status %d", status)
	}
	if status, _ := queryAs(schema, "not-a-token", "SELECT 1"); status != http.StatusNotFound {
		t.Errorf("malformed token: status %d", status)
	}
	s.cfg.Demo = true // demo loads leave out comments; the limiter stays off
	body, ct = form(t, "", map[string]string{"sample": "tidy.csv"})
	status, other := post(t, ts.URL+"/api/load", ct, body)
	s.cfg.Demo = false
	if status != 200 {
		t.Fatalf("second load %d: %v", status, other)
	}
	otherSchema := other["schema"].(string)
	defer func() {
		conn, err := s.pool.Acquire(context.Background())
		if err == nil {
			s.dropSchema(context.Background(), conn.Conn(), otherSchema)
			conn.Release()
		}
	}()
	if status, _ := queryAs(otherSchema, token, "SELECT 1"); status != http.StatusNotFound {
		t.Errorf("another load's token opened %s: status %d", otherSchema, status)
	}
	if status, _ := queryAs(otherSchema, other["token"].(string), "SELECT count(*) FROM tidy"); status != 200 {
		t.Errorf("own token: status %d", status)
	}
	var comments int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_description d
		JOIN pg_class c ON c.oid = d.objoid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1`, otherSchema).Scan(&comments); err != nil || comments != 0 {
		t.Errorf("demo load left %d comments (err %v)", comments, err)
	}
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_description d
		JOIN pg_class c ON c.oid = d.objoid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1`, schema).Scan(&comments); err != nil || comments == 0 {
		t.Errorf("non-demo load has no comments (err %v)", err)
	}

	// The database cuts values and handles any column shape.
	status, out = query("SELECT g, repeat('z', 10000000) AS big FROM generate_series(1, 3) g")
	if status != 200 {
		t.Fatalf("big values %d: %v", status, out)
	}
	if v := out["rows"].([]any)[0].([]any)[1].(string); len([]rune(v)) != maxCell+1 || !strings.HasSuffix(v, "…") {
		t.Errorf("big value has %d characters", len([]rune(v)))
	}
	status, out = query("SELECT 1, 'a', true, NULL::int, '{1,2}'::int[]")
	if status != 200 || fmt.Sprint(out["columns"]) != "[?column? ?column? ?column? int4 int4]" ||
		fmt.Sprint(out["rows"]) != "[[1 a true <nil> {1,2}]]" {
		t.Errorf("shapes %d: %v %v", status, out["columns"], out["rows"])
	}
	if status, out = query("SELECT FROM budget_summary"); status != 200 || len(out["rows"].([]any)) != 7 {
		t.Errorf("zero columns %d: %v", status, out)
	}
	if status, out = query("EXPLAIN SELECT * FROM budget_summary"); status != 200 || len(out["rows"].([]any)) == 0 {
		t.Errorf("explain %d: %v", status, out)
	}
	if status, out = query("SELECT $1::int"); status != http.StatusBadRequest {
		t.Errorf("parameter placeholder %d: %v", status, out)
	}

	// The janitor only drops roles kempt marked as its own.
	for _, st := range []string{
		"DROP ROLE IF EXISTS demo_readonly", "CREATE ROLE demo_readonly NOLOGIN",
		"DROP ROLE IF EXISTS demo_zzzzzzzz", "CREATE ROLE demo_zzzzzzzz NOLOGIN",
		"COMMENT ON ROLE demo_zzzzzzzz IS '" + roleComment + "2020-01-01T00:00:00Z'",
	} {
		if _, err := s.pool.Exec(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	}
	defer s.pool.Exec(context.Background(), "DROP ROLE IF EXISTS demo_readonly")
	defer s.pool.Exec(context.Background(), "DROP ROLE IF EXISTS demo_zzzzzzzz")

	// The janitor leaves fresh schemas alone and drops expired ones.
	if n, err := s.sweep(context.Background(), time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("sweep fresh: %d %v", n, err)
	}
	if n, err := s.sweep(context.Background(), time.Now().Add(time.Minute)); err != nil || n < 1 {
		t.Fatalf("sweep expired: %d %v", n, err)
	}
	if status, _ := query("SELECT 1"); status != http.StatusNotFound {
		t.Fatalf("query after sweep status %d", status)
	}
	roles := func(name string) bool {
		var ok bool
		s.pool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", name).Scan(&ok)
		return ok
	}
	if !roles("demo_readonly") {
		t.Error("the janitor dropped a role kempt did not create")
	}
	if roles("demo_zzzzzzzz") || roles(schema) {
		t.Error("the janitor kept an orphaned kempt role")
	}
}

func TestDemoRowLimitAcrossSheets(t *testing.T) {
	ts := newTestServer(t, Config{Demo: true})
	f := excelize.NewFile()
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("S%d", i)
		if i == 0 {
			f.SetSheetName("Sheet1", name)
		} else {
			f.NewSheet(name)
		}
		sw, err := f.NewStreamWriter(name)
		if err != nil {
			t.Fatal(err)
		}
		sw.SetRow("A1", []any{"id", "v"})
		for r := 2; r <= 20_000; r++ {
			cell, _ := excelize.CoordinatesToCellName(1, r)
			sw.SetRow(cell, []any{r, r * 2})
		}
		sw.Flush()
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	body, ct := form(t, "multi.xlsx", map[string]string{"file": buf.String()})
	if status, out := post(t, ts.URL+"/api/inspect", ct, body); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d: %v", status, out)
	}
}

func TestRateLimitIgnoresSpoofedXFF(t *testing.T) {
	ts := newTestServer(t, Config{Demo: true})
	var last int
	for i := 0; i < demoRate+1; i++ {
		body, ct := form(t, "", map[string]string{"sample": "tidy.csv"})
		req, _ := http.NewRequest("POST", ts.URL+"/api/inspect", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		last = res.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For bypassed the limit: last status %d", last)
	}
}

func TestCSVStartingWithPK(t *testing.T) {
	ts := newTestServer(t, Config{})
	body, ct := form(t, "pk.csv", map[string]string{"file": "PKG_ID,Name\n1,a\n2,b\n3,c\n"})
	if status, out := post(t, ts.URL+"/api/inspect", ct, body); status != 200 {
		t.Fatalf("status %d: %v", status, out)
	}
}
