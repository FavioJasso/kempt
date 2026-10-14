# kempt

**Unkempt spreadsheets in, clean typed SQL tables out.**

kempt is a single static binary that finds the real table inside a messy XLSX or CSV sheet. It handles title rows, multi-row headers, merged cells, subtotals and footnotes. It types every column, loads the result into Postgres, MySQL, or SQLite, and **explains every decision it made** along the way.

<!-- DEPLOY_BUTTON -->

![kempt demo](demo.gif)
<!-- Render the GIF with `vhs demo.tape` (https://github.com/charmbracelet/vhs). -->

## Install

```bash
go install github.com/FavioJasso/kempt/cmd/kempt@latest
```

You can also download a binary for Linux, macOS, or Windows (amd64/arm64) from [Releases](https://github.com/FavioJasso/kempt/releases), or run the web demo with Docker:

```bash
docker build -t kempt . && docker run --rm -p 8080:8080 kempt
```

kempt is pure Go with no CGO, so every build is a static binary.

## Three commands

```bash
kempt inspect budget.xlsx                      # what kempt sees: tables, header, types, every decision
kempt ddl budget.xlsx --dialect postgres       # CREATE TABLE with column comments
kempt load budget.xlsx --dsn postgres://...    # create the table and load the clean rows
```

Here is `kempt inspect` on the bundled [`budget_report.xlsx`](testdata/fixtures/budget_report.xlsx). That sheet has a merged title, a two-row "FY2025 / FY2026" header over "Budget / Actual", `($000s)` units, accounting negatives, subtotals, a grand total and footnotes. The decision list is abridged; `…` marks what was left out:

```text
$ kempt inspect budget_report.xlsx --rows 4
budget_report.xlsx: 1 sheet, 1 table

== Sheet "Budget Summary" (A1:H19, 1 table) ==

-- Table budget_summary (region A1:H19, 7 rows, 8 columns) --

Columns (8):
  #  NAME           TYPE          UNIT   SOURCE  HEADER
  1  department     text                 A       Department
  2  program        text                 B       Program
  3  fy2025_budget  int           $000s  C       FY2025 ($000s) / Budget
  4  fy2025_actual  int           $000s  D       FY2025 ($000s) / Actual
  5  fy2026_budget  int           $000s  E       FY2026 ($000s) / Budget
  6  fy2026_actual  int           $000s  F       FY2026 ($000s) / Actual
  7  change_pct     decimal(2,1)  %      G       Change (%)
  8  notes          text                 H       Notes

Decisions (26):
  RANGE    KIND                 CONF  REASON
  A1:H1    skip_title_row       0.95  Row 1 has a single value merged across A:H above the header
                                      (row 4); skipped as a title.
  A2:H2    skip_title_row       0.90  Row 2 has a single value above the header (row 4); skipped as
                                      a title.
  …  5 column_name decisions
  A4:H5    header_merge         0.90  Rows 4–5 form a two-row header: merged group labels in row 4
                                      ("FY2025 ($000s)" over C:D, "FY2026 ($000s)" over E:F) are
                                      combined with the labels below as group_sub.
  …  5 strip_unit decisions
  A5:H5    header_row           0.95  Row 5 scored highest as the header (3.45: 100% filled, 100%
                                      labels, 75% unique, next rows 70% more numeric); the next best
                                      row (row 6) scored 2.45; row 4 above holds its group labels.
  A8:H8    drop_total_row       0.97  Row 8 is labeled "Total Public Works"; C8 equals the sum of
                                      its group (3570). Dropped as a totals row (use --keep-totals
                                      to keep it).
  C11      accounting_negative  0.95  Read 1 value in column fy2025_budget as negative because it is
                                      written in accounting style (parentheses or a trailing minus):
                                      C11 ("(1,234)" → -1234).
  …  3 accounting_negative decisions
  H11      null_token           0.95  Read 1 value in column notes as NULL because it is a
                                      missing-value placeholder: H11 ("—").
  …  2 drop_total_row and 1 null_token decisions
  A16:H16  drop_total_row       0.97  Row 16 is labeled "Grand Total"; C16 equals the sum of every
                                      data row above (14396). Dropped as a totals row (use
                                      --keep-totals to keep it).
  A18:H18  skip_footer          0.90  Row 18 follows the last data row and holds a single note
                                      ("Source: Riverton Finance Department, FY…"); dropped as a
                                      footer.
  …  1 skip_footer decision

Preview (first 4 of 7 rows; ∅ = NULL):
  ROW  department          program              fy2025_budget  fy2025_actual  fy2026_budget
    6  Public Works        Streets                       2450           2391           2600
    7  Public Works        Fleet                         1120           1165           1180
    9  Parks & Recreation  Parks Maintenance             1800           1742           1850
   10  Parks & Recreation  Recreation Programs            960           1010           1020

  ROW  fy2026_actual  change_pct  notes
    6           1212         6.1  Paving backlog
    7            604         5.4  ∅
    9            930         2.8  ∅
   10            488         6.3  Summer camp expansion
```

## What kempt does

**Detection** finds the table inside the sheet:

- **Regions.** Two or more blank rows split the sheet into separate tables (`sheet_1`, `sheet_2`, ...). So does one blank row followed by a header-like row. Stray notes too small to be a table are ignored, and the decision says so.
- **Title rows.** Single-value or sparse rows above the header are skipped, including titles merged across the width.
- **Header scoring.** Each candidate row is scored on how filled, textual and unique it is, and on how much more numeric the rows below it are.
- **Multi-row headers.** Group labels such as `FY2025` merged over `Budget | Actual` become `fy2025_budget` and `fy2025_actual`.
- **Units.** `($000s)`, `(USD)`, `(%)`, `[kWh]`, and `in thousands` move from the name into a column comment. Values are never rescaled.
- **Totals.** A row labeled `Total`, `Subtotal`, `Grand total`, `Sum` or `All ...` is dropped when one of its numbers equals the sum of the rows above it (confidence 0.97), or when the label is the row's only text. A row like `Total Wine & More | Catering | 340` is kept and logged.
- **Footers.** The trailing run of `Source:`, `Note:`, `*`, `†`, `¹` and `Prepared by` rows is dropped.
- **Merged cells.** Vertically merged categories are filled down into every row they cover.
- **No header?** When no row reads as a header (its values have the same types as the rows below it), every row is kept and the columns are named `col_a`, `col_b`, …

**Normalization** turns display formatting into types:

| In the sheet | In the table |
|---|---|
| `(1,234)`, `500-` | `-1234`, `-500` |
| `$1,200.50`, `€15`, `£3` | `1200.50`, `15`, `3` (symbol logged, kept in the comment) |
| `12%`, or `0.12` formatted as a percentage in Excel | `12` in a column suffixed `_pct` (not divided by 100) |
| `1.234,56` and `12,50` on a sheet that only uses decimal commas | `1234.56`, `12.50` (logged once per sheet) |
| `N/A`, `NA`, `-`, `—`, `null`, `none`, empty | `NULL` |
| `#DIV/0!`, `#N/A`, `#VALUE!`, `#REF!` | `NULL` (logged as Excel errors) |
| Excel serial dates, `2026-03-15`, `03/15/2026`, `3/15/26`, `Mar 15, 2026`, `15-Mar-2026` | `DATE` / `TIMESTAMP` (two-digit years follow Excel: `00`–`29` are 2000s, `30`–`99` are 1900s) |
| `09:30`, `1:15 PM` | kept as text, never turned into a date |
| Excel cells formatted as times or durations (`h:mm`, `[h]:mm:ss`) | `HH:MM:SS` text such as `09:30:00` (logged) instead of a fraction of a day |
| `02134`, `007`, and numbers formatted with leading zeros (`00000`) | kept as text, so leading zeros survive |

**Column names** are lowercase snake_case. Accents are transliterated, and `%`, `#` and `&` become `pct`, `num` and `and`. camelCase is split (`OrderID` → `order_id`), and a leading digit gets a `c_` prefix (`2025 Revenue` → `c_2025_revenue`). Blank headers become `col_<letter>`, and duplicates get `_2`, `_3`. Names Postgres reserves for system columns (`ctid`, `xmin`, `xmax`, `cmin`, `cmax`, `tableoid`) also get the `c_` prefix. Table and column names are cut to 63 bytes, the Postgres limit. The original header text is kept as the column comment.

Each column gets the narrowest type that parses every non-null value, in the order `bool → int → bigint → decimal(p,s) → date → timestamp → text`. Decimal precision and scale are measured from the data. In MySQL, text becomes `VARCHAR(n)` sized to the next power of two, or `TEXT` beyond 1024.

**Explainability.** Every rule logs a decision with a cell range, a reason, and a confidence score. kempt is conservative: when a heuristic is unsure it keeps the data and logs a low-confidence decision rather than dropping rows.

### A decision report

`--report report.json` writes every decision for a file. Here is an excerpt for the budget sheet (3 of its 26 decisions):

```json
{
  "file": "budget_report.xlsx",
  "tables": [
    {
      "sheet": "Budget Summary",
      "table": "budget_summary",
      "region": "A1:H19",
      "decisions": [
        {
          "kind": "header_merge",
          "range": "'Budget Summary'!A4:H5",
          "reason": "Rows 4–5 form a two-row header: merged group labels in row 4 (\"FY2025 ($000s)\" over C:D, \"FY2026 ($000s)\" over E:F) are combined with the labels below as group_sub.",
          "confidence": 0.9
        },
        {
          "kind": "strip_unit",
          "range": "'Budget Summary'!C4:C5",
          "reason": "Unit \"$000s\" moved from the header \"FY2025 ($000s)\" into the column comment of fy2025_budget; values are not rescaled.",
          "confidence": 0.9
        },
        {
          "kind": "drop_total_row",
          "range": "'Budget Summary'!A8:H8",
          "reason": "Row 8 is labeled \"Total Public Works\"; C8 equals the sum of its group (3570). Dropped as a totals row (use --keep-totals to keep it).",
          "confidence": 0.97
        }
      ]
    }
  ]
}
```

## Overrides

When a heuristic gets a sheet wrong, these flags correct it. All of them work with `inspect`, `ddl`, and `load`.

| Flag | Effect |
|---|---|
| `--sheet NAME` | only process one sheet (default: every visible sheet) |
| `--skip-rows N` | skip the first N rows; the header starts right after |
| `--header-rows N` | force the header height |
| `--keep-totals` | keep totals and subtotals rows as data |
| `--fill-down COL[,COL]` | fill blank cells from above; name columns as kempt prints them (`region`, `name_2`), by letter (`C`), or by header text |
| `--lenient 0.98` | keep a column's type if at least this fraction of values parse; failures become NULL and are logged |
| `--date-order dmy` | read ambiguous numeric dates as DD/MM (default MM/DD, logged with confidence 0.5) |
| `--report report.json` | write the decision report |
| `--table NAME` | table name (default: the slugified sheet name, or the file name for CSV); with several tables in the workbook they become `NAME_1`, `NAME_2`, ... in order |

## Loading

`kempt load` reads the DSN from the first of these that is set:

1. `--dsn`
2. `$KEMPT_DSN`
3. `$DATABASE_PUBLIC_URL`
4. `$DATABASE_URL`

It prints which source it used, with the password redacted.

| DSN | Driver |
|---|---|
| `postgres://user:pass@host:5432/db`, `postgresql://...` | pgx, bulk loaded with `COPY` |
| `mysql://user:pass@host:3306/db` | go-sql-driver/mysql, batched multi-row `INSERT` |
| `sqlite://path/to/file.db`, `file.db`, `file.sqlite` | modernc.org/sqlite (pure Go) |

`--if-exists fail|replace|append` (default `fail`) decides what happens when the table already exists. `append` first checks the existing columns and refuses, without writing anything, when a column is missing or would change the new values (decimals into an integer or a narrower `NUMERIC`, timestamps into a `DATE`). `--dry-run` prints the DDL, the first 10 rows, and the report without connecting.

Postgres and SQLite load every table in one transaction, so a failed load leaves the database unchanged. MySQL commits DDL as it goes: with `fail`, kempt drops the tables it had created before the error; with `replace` and `append`, it prints the tables that were already committed. MySQL loads run in strict mode, so the server rejects a bad value instead of storing a zero or truncating it.

## Why not csvsql / DuckDB?

They are excellent, and if your file is already a tidy table you should use them. `csvsql`, `sq` and DuckDB's `read_xlsx` load clean rectangular data quickly and well. kempt is for the step before that: business and government spreadsheets that aren't tables yet. Those sheets have a title in row 1 and a header split across two merged rows. Their numbers are formatted as `(1,234)`, with a subtotal under every department and a footnote at the bottom. kempt finds the table, cleans the values, and shows its work, so you can trust the result or override the parts it got wrong.

## Web demo and API

`kempt serve` runs a one-page demo at `http://localhost:8080`, embedded in the binary. Drop a file or pick a sample. The original grid appears on the left, colored by what kempt detected. On the right are the clean table, the DDL for each dialect, and the decision list; clicking a decision highlights its cells. When a Postgres DSN is configured, **Load into Postgres** copies the tables into a throwaway `demo_<random>` schema and opens a read-only SQL console.

| Endpoint | |
|---|---|
| `GET /healthz` | liveness |
| `GET /api/samples` | the bundled sample files |
| `POST /api/inspect` | multipart `file` (or `sample`) and optional `keep_totals`, `lenient`, `date_order`, `header_rows`, `skip_rows`, `fill_down`, `sheet` → grid (first 200×30), cell roles, decisions, columns, a 50-row preview, and DDL for all three dialects |
| `POST /api/load` | same input; loads into a fresh `demo_<random8>` schema → `{schema, token, table, tables, expiresAt}` (Postgres only) |
| `POST /api/query` | `{"schema", "token", "sql"}` → at most 100 rows; values are cut to 2,000 characters, or fewer when a row has more than 50 columns (100,000 characters per row); one read-only statement, 2 s timeout |

Uploads are processed in memory and never written to disk. Each loaded schema gets its own LOGIN role, which can only read that schema, and queries run as that role inside a `READ ONLY` transaction. The role's password comes from `KEMPT_SECRET` and a random token that only the uploader receives, so knowing a schema's name is not enough to query it. The server's own database privileges are never exposed to the SQL console. Loaded schemas, and their roles, are dropped after an hour.

Other demo users cannot read your rows, but the Postgres catalog is shared: table and column names of loaded schemas are visible to every role while the schema exists. Demo loads therefore leave out the comments that hold the original header text.

| Variable | |
|---|---|
| `PORT` | listen port (default 8080; `--addr` overrides) |
| `DATABASE_URL` (or `KEMPT_DSN`, `DATABASE_PUBLIC_URL`) | Postgres for **Load** and **Query** |
| `KEMPT_DEMO=1` | public-demo limits (below) |
| `KEMPT_SECRET` | derives the per-schema query passwords; set it so loaded schemas stay queryable across restarts |
| `KEMPT_TRUST_PROXY=1` | rate-limit by `X-Forwarded-For` (on automatically on Railway); leave it off when clients connect directly |

Set `KEMPT_DEMO=1` for a public deployment. It caps uploads at 5 MB (100 MB unzipped), 50,000 rows and 1,000,000 cells, reads at most two spreadsheets at a time, and allows 10 requests per minute per IP. Formulas saved without a computed value are evaluated only in workbooks with up to 8 MB of sheet XML (about 150,000 values); in larger ones they read as `#CALC!`, which kempt logs.

## Railway

The demo is designed for [Railway](https://railway.com): one service for kempt and one for Postgres.

```bash
railway login
railway init -n kempt
railway add --database postgres
railway add --service kempt
railway variables -s kempt --set 'DATABASE_URL=${{Postgres.DATABASE_URL}}' --set 'KEMPT_DEMO=1'
railway up -s kempt
railway domain -s kempt
```

Before the `variables` step, check the database service's name with `railway status` and use it in the reference variable (`${{Postgres.DATABASE_URL}}`). The Dockerfile builds a static binary on a distroless image and listens on `$PORT`.

**Loading from your laptop.** `railway run` injects the Postgres service's variables into a local command, so you don't have to paste a DSN:

```bash
railway run -s Postgres kempt load budget.xlsx
```

This works because kempt checks `DATABASE_PUBLIC_URL` before `DATABASE_URL`: the private URL only resolves inside Railway's network. Confirm the variable names in the Postgres service's Variables tab the first time.

## Development

```bash
go vet ./... && go test ./...
go run ./testdata/gen                          # regenerate the fixture workbooks (deterministic)
go test ./cmd/kempt -run TestGolden -update    # rewrite testdata/golden/*.json after an intended change
KEMPT_TEST_PG='postgres://user:pass@localhost:5432/db?sslmode=disable' go test ./...   # include Postgres integration tests
goreleaser release --snapshot --clean          # local cross-platform build
```

Each fixture in `testdata/fixtures` targets specific heuristics, and `testdata/golden` holds the expected `kempt inspect --json` output for each one. `tidy.csv` is the regression guard: kempt must leave an already-clean file unchanged except for the column names.

## Roadmap

Out of scope for v1:

- PDF tables
- Optional LLM assistance for low-confidence regions
- Legacy `.xls` files (save as `.xlsx` for now)
- Google Sheets input
- Schema evolution and append-with-migration
- Rescaling values by their units (`($000s)` is recorded, not applied)

## License

MIT
