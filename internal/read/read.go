// Package read turns .xlsx workbooks and CSV files into grid.Grids. It never
// interprets values beyond their stored kind: cleanup happens downstream.
package read

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/FavioJasso/kempt/internal/grid"
)

// Options controls what is read.
type Options struct {
	Sheet   string // "" = every visible worksheet (xlsx); a named sheet may be hidden. Ignored for CSV.
	MaxRows int    // 0 = unlimited; otherwise ErrTooManyRows when a sheet has more rows
	// MaxCells caps rows x columns of each grid (0 = unlimited); larger
	// sheets fail with ErrTooManyCells before the grid is allocated.
	MaxCells int64
	// CalcLimit skips evaluating formulas that have no cached value on
	// sheets with more than this many stored values (0 = always evaluate);
	// they read as #CALC!. Evaluating loads the whole sheet into memory.
	CalcLimit int
}

var (
	// ErrTooManyRows is returned when a sheet has more rows than Options.MaxRows.
	ErrTooManyRows = errors.New("read: too many rows")
	// ErrUnsupported is returned for files that are not .xlsx or CSV (legacy
	// .xls, PDF, ...). The wrapped message says how to convert the file.
	ErrUnsupported = errors.New("read: unsupported file format")
	// ErrSheetNotFound is returned when Options.Sheet names no sheet in the workbook.
	ErrSheetNotFound = errors.New("read: sheet not found")
	// ErrTooSparse is returned when a sheet's few values lie so far apart
	// that its grid would be almost entirely empty cells.
	ErrTooSparse = errors.New("read: sheet is mostly empty")
	// ErrTooManyCells is returned when a sheet spans more cells than Options.MaxCells.
	ErrTooManyCells = errors.New("read: sheet is too large")
)

// Grids are dense, so a sheet holding A1 and XFD50000 alone would need
// hundreds of millions of cells. A grid may always span sparseFloor cells;
// beyond that, at least one cell in sparseRatio must be stored in the file.
const (
	sparseFloor = 1 << 22
	sparseRatio = 16
)

var (
	zipMagic = []byte("PK\x03\x04")
	oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	pdfMagic = []byte("%PDF-")
)

// File reads the spreadsheet at path.
func File(path string, opts Options) ([]*grid.Grid, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return Bytes(filepath.Base(path), data, opts)
}

// Bytes reads an in-memory spreadsheet without touching disk. name supplies
// the extension and, for CSV, the sheet name. The content decides the format
// when it contradicts the extension: a zip archive is read as .xlsx, plain
// text as CSV.
func Bytes(name string, data []byte, opts Options) ([]*grid.Grid, error) {
	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case bytes.HasPrefix(data, zipMagic):
		switch ext {
		case ".xlsb", ".ods", ".numbers":
			return nil, fmt.Errorf("%w: %s is a %s file; open it in a spreadsheet app and save it as .xlsx", ErrUnsupported, base, ext)
		}
		return XLSX(bytes.NewReader(data), opts)
	case bytes.HasPrefix(data, oleMagic):
		return nil, fmt.Errorf("%w: %s is a legacy .xls or password-protected workbook; open it in Excel and save it as .xlsx without a password", ErrUnsupported, base)
	case bytes.HasPrefix(data, pdfMagic):
		return nil, fmt.Errorf("%w: %s is a PDF; kempt reads .xlsx and .csv files, so export the table to one of those first", ErrUnsupported, base)
	case isXLSXExt(ext) && len(bytes.TrimSpace(data)) == 0:
		return nil, fmt.Errorf("%w: %s is empty, not a valid .xlsx workbook", ErrUnsupported, base)
	case looksLikeMarkup(data):
		return nil, fmt.Errorf("%w: %s looks like an HTML or XML export; open it in Excel and save it as .xlsx or .csv", ErrUnsupported, base)
	case looksLikeText(data):
		g, err := readCSV(name, data, opts.MaxRows, opts.MaxCells)
		if err != nil {
			return nil, err
		}
		return []*grid.Grid{g}, nil
	}
	return nil, fmt.Errorf("%w: %s is neither an .xlsx workbook nor a CSV file; save it as .xlsx or .csv", ErrUnsupported, base)
}

func isXLSXExt(ext string) bool {
	switch ext {
	case ".xlsx", ".xlsm", ".xltx", ".xltm":
		return true
	}
	return false
}

// looksLikeText reports whether data is plausibly a text file: a UTF-16 BOM,
// or no NUL byte in the first 8 KiB.
func looksLikeText(data []byte) bool {
	if bytes.HasPrefix(data, bomUTF16LE) || bytes.HasPrefix(data, bomUTF16BE) {
		return true
	}
	return bytes.IndexByte(data[:min(len(data), 8<<10)], 0) < 0
}

// looksLikeMarkup reports whether UTF-8 data starts like an HTML or XML
// document, as "Excel" exports from web apps often do.
func looksLikeMarkup(data []byte) bool {
	head := bytes.TrimLeft(bytes.TrimPrefix(data, bomUTF8), " \t\r\n")
	head = bytes.ToLower(head[:min(len(head), 16)])
	for _, p := range []string{"<?xml", "<!doctype", "<!--", "<html", "<head", "<body", "<table", "<meta"} {
		if bytes.HasPrefix(head, []byte(p)) {
			return true
		}
	}
	return false
}

func tooManyRows(sheet string, limit int) error {
	return fmt.Errorf("%w: sheet %q has more than %d rows", ErrTooManyRows, sheet, limit)
}

// checkCells enforces Options.MaxCells.
func checkCells(sheet string, rows, cols int, maxCells int64) error {
	if cells := int64(rows) * int64(cols); maxCells > 0 && cells > maxCells {
		return fmt.Errorf("%w: sheet %q spans %d rows x %d columns (%d cells; the limit is %d)",
			ErrTooManyCells, sheet, rows, cols, cells, maxCells)
	}
	return nil
}

// checkDensity rejects a rows x cols grid that would hold far more cells than
// the stored values (fields, for CSV) it was read from.
func checkDensity(sheet string, rows, cols, stored int) error {
	cells := int64(rows) * int64(cols)
	if cells <= sparseFloor || cells <= sparseRatio*int64(stored) {
		return nil
	}
	return fmt.Errorf("%w: sheet %q spans %d rows x %d columns but stores only %d values; delete the stray cells far from the data",
		ErrTooSparse, sheet, rows, cols, stored)
}
