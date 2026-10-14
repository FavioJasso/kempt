// Command kempt turns messy spreadsheets into clean, typed SQL tables and
// explains every decision it made along the way.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/FavioJasso/kempt/internal/detect"
	"github.com/FavioJasso/kempt/internal/dsn"
	"github.com/FavioJasso/kempt/internal/emit"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/plan"
	"github.com/FavioJasso/kempt/internal/read"
	"github.com/FavioJasso/kempt/internal/report"
)

// version is set by goreleaser via -ldflags.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "kempt:", err)
		os.Exit(1)
	}
}

// flags shared by every command that analyzes a file.
type flags struct {
	sheet      string
	headerRows int
	skipRows   int
	keepTotals bool
	fillDown   []string
	lenient    float64
	dateOrder  string
	reportPath string
	table      string
}

func (f *flags) planOptions() (plan.Options, error) {
	if f.lenient < 0 || f.lenient > 1 {
		return plan.Options{}, fmt.Errorf("--lenient must be between 0 and 1, got %v", f.lenient)
	}
	if f.headerRows < 0 || f.skipRows < 0 {
		return plan.Options{}, errors.New("--header-rows and --skip-rows must not be negative")
	}
	order := strings.ToLower(f.dateOrder)
	switch order {
	case "", "auto":
		order = ""
	case "mdy", "dmy":
	default:
		return plan.Options{}, fmt.Errorf("--date-order must be mdy or dmy, got %q", f.dateOrder)
	}
	return plan.Options{
		Detect: detect.Options{
			SkipRows:   f.skipRows,
			HeaderRows: f.headerRows,
			KeepTotals: f.keepTotals,
			FillDown:   f.fillDown,
		},
		Infer: infer.Options{Lenient: f.lenient, DateOrder: order},
		Table: f.table,
	}, nil
}

// analyze reads a file and plans every sheet.
func (f *flags) analyze(path string, stderr io.Writer) ([]*plan.Sheet, error) {
	opts, err := f.planOptions()
	if err != nil {
		return nil, err
	}
	grids, err := read.File(path, read.Options{Sheet: f.sheet})
	if err != nil {
		return nil, err
	}
	sheets := make([]*plan.Sheet, 0, len(grids))
	for _, g := range grids {
		sh := plan.Build(g, opts)
		for _, w := range sh.Warnings {
			fmt.Fprintf(stderr, "kempt: warning: sheet %q: %s\n", sh.Name, w)
		}
		sheets = append(sheets, sh)
	}
	plan.UniqueTables(sheets)
	if f.reportPath != "" {
		if err := writeFile(f.reportPath, func(w io.Writer) error {
			return report.Decisions(w, filepath.Base(path), sheets)
		}); err != nil {
			return nil, err
		}
	}
	return sheets, nil
}

func tables(sheets []*plan.Sheet) []*plan.Plan {
	var out []*plan.Plan
	for _, sh := range sheets {
		out = append(out, sh.Tables...)
	}
	return out
}

func writeFile(path string, fn func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func newRoot() *cobra.Command {
	f := &flags{}
	root := &cobra.Command{
		Use:           "kempt",
		Short:         "Unkempt spreadsheets in, clean typed SQL tables out",
		Long:          "kempt finds the real table inside a messy XLSX or CSV sheet (titles, multi-row headers,\nmerged cells, totals, footnotes), types every column, and explains each decision it made.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&f.sheet, "sheet", "", "only process this sheet (default: every visible sheet)")
	pf.IntVar(&f.headerRows, "header-rows", 0, "force the header height in rows (0 = detect)")
	pf.IntVar(&f.skipRows, "skip-rows", 0, "force the number of leading rows to skip before the header (0 = detect)")
	pf.BoolVar(&f.keepTotals, "keep-totals", false, "keep totals and subtotals rows as data")
	pf.StringSliceVar(&f.fillDown, "fill-down", nil, "fill blank cells from above in these columns (letters, header text, or names)")
	pf.Float64Var(&f.lenient, "lenient", 0, "keep a column's type if at least this fraction of values parse (failures become NULL)")
	pf.StringVar(&f.dateOrder, "date-order", "", "order for ambiguous numeric dates: mdy or dmy (default: mdy, logged)")
	pf.StringVar(&f.reportPath, "report", "", "write the decision report as JSON to this file")
	pf.StringVar(&f.table, "table", "", "table name (default: the slugified sheet name, or the file name for CSV; several tables get _1, _2)")

	root.AddCommand(inspectCmd(f), ddlCmd(f), loadCmd(f), serveCmd())
	return root
}

func inspectCmd(f *flags) *cobra.Command {
	var asJSON, raw bool
	var rows int
	cmd := &cobra.Command{
		Use:   "inspect <file>",
		Short: "Show detected tables, headers, column types, and every decision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if raw {
				grids, err := read.File(args[0], read.Options{Sheet: f.sheet})
				if err != nil {
					return err
				}
				for i, g := range grids {
					if i > 0 {
						fmt.Fprintln(out)
					}
					if err := report.Raw(out, g, 0, 0); err != nil {
						return err
					}
				}
				return nil
			}
			sheets, err := f.analyze(args[0], cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if asJSON {
				return report.JSON(out, filepath.Base(args[0]), sheets)
			}
			return report.Text(out, filepath.Base(args[0]), sheets, report.TextOptions{PreviewRows: rows})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the full plan as JSON")
	cmd.Flags().BoolVar(&raw, "raw", false, "dump the raw grid (cells, kinds, merges) without detection")
	cmd.Flags().IntVar(&rows, "rows", 10, "preview rows per table in text output")
	return cmd
}

func ddlCmd(f *flags) *cobra.Command {
	var dialect, schema string
	cmd := &cobra.Command{
		Use:   "ddl <file>",
		Short: "Print CREATE TABLE statements for every detected table",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := checkDialect(dialect)
			if err != nil {
				return err
			}
			sheets, err := f.analyze(args[0], cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			ts := tables(sheets)
			if len(ts) == 0 {
				return errors.New("no table found (try kempt inspect to see why)")
			}
			for i, t := range ts {
				if i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				fmt.Fprint(cmd.OutOrStdout(), emit.DDL(t, d, emit.DDLOptions{Schema: schema}))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dialect, "dialect", "postgres", "SQL dialect: postgres, mysql, or sqlite")
	cmd.Flags().StringVar(&schema, "schema", "", "schema/database to qualify the table with")
	return cmd
}

func loadCmd(f *flags) *cobra.Command {
	var dsnFlag, ifExists, schema, dialect string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "load <file>",
		Short: "Create the tables and load the clean rows into a database",
		Long: "Create the tables and load the clean rows. The DSN comes from --dsn, then $KEMPT_DSN,\n" +
			"$DATABASE_PUBLIC_URL, and $DATABASE_URL. Accepted: postgres://, postgresql://,\n" +
			"mysql://user:pass@host:port/db, sqlite://path, or a path ending in .db/.sqlite.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
			switch ifExists {
			case "fail", "replace", "append":
			default:
				return fmt.Errorf("--if-exists must be fail, replace, or append, got %q", ifExists)
			}
			target, terr := dsn.Resolve(dsnFlag, os.Getenv)
			if terr != nil && !(dryRun && errors.Is(terr, dsn.ErrNoDSN)) {
				return terr
			}
			if terr == nil {
				fmt.Fprintln(stderr, "kempt: using", target.Describe())
			}
			d := target.Dialect
			if dialect != "" {
				if d != "" && dialect != d {
					return fmt.Errorf("--dialect %s does not match the %s DSN", dialect, d)
				}
				d = dialect
			}
			if d == "" {
				d = infer.Postgres
			}
			if _, err := checkDialect(d); err != nil {
				return err
			}
			sheets, err := f.analyze(args[0], stderr)
			if err != nil {
				return err
			}
			ts := tables(sheets)
			if len(ts) == 0 {
				return errors.New("no table found (try kempt inspect to see why)")
			}
			if dryRun {
				for _, t := range ts {
					fmt.Fprint(stdout, emit.DDL(t, d, emit.DDLOptions{Schema: schema}))
					fmt.Fprintln(stdout)
				}
				return report.Text(stdout, filepath.Base(args[0]), sheets, report.TextOptions{PreviewRows: 10})
			}
			results, err := emit.Load(cmd.Context(), target.Dialect, target.DSN, ts,
				emit.LoadOptions{IfExists: ifExists, Schema: schema})
			if err != nil {
				return err
			}
			for _, r := range results {
				fmt.Fprintf(stdout, "loaded %d rows into %s\n", r.Rows, r.Table)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&dsnFlag, "dsn", "", "database DSN (overrides $KEMPT_DSN, $DATABASE_PUBLIC_URL, $DATABASE_URL)")
	fl.StringVar(&ifExists, "if-exists", "fail", "when the table exists: fail, replace, or append")
	fl.BoolVar(&dryRun, "dry-run", false, "print the DDL, the first 10 rows, and the report without touching the database")
	fl.StringVar(&schema, "schema", "", "schema (Postgres) or database (MySQL) to load into")
	fl.StringVar(&dialect, "dialect", "", "SQL dialect for --dry-run without a DSN (default postgres)")
	return cmd
}

func checkDialect(d string) (string, error) {
	d = strings.ToLower(d)
	for _, ok := range infer.Dialects {
		if d == ok {
			return d, nil
		}
	}
	return "", fmt.Errorf("unknown dialect %q (want postgres, mysql, or sqlite)", d)
}
