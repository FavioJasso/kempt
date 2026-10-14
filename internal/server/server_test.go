package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

	query := func(sql string) (int, map[string]any) {
		b, _ := json.Marshal(queryRequest{Schema: schema, SQL: sql})
		return post(t, ts.URL+"/api/query", "application/json", bytes.NewReader(b))
	}
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
}
