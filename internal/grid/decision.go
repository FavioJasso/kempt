package grid

import (
	"cmp"
	"math"
	"slices"
)

// Decision kinds. Every heuristic in kempt that changes, drops, or
// reinterprets data logs one of these.
const (
	KSkipRowsForced  = "skip_rows_forced"  // --skip-rows removed leading rows
	KSkipTitleRow    = "skip_title_row"    // leading title row above the header
	KSkipLeadingRow  = "skip_leading_row"  // non-title row above the header (low confidence)
	KLeadingData     = "leading_data_row"  // data-like row above the header, kept as data
	KSplitRegion     = "split_region"      // blank rows started a new table
	KSkipRegion      = "skip_region"       // region too small to be a table
	KHeaderRow       = "header_row"        // which row was chosen as the header
	KHeaderMerge     = "header_merge"      // multi-row header combined as group_sub
	KNoHeader        = "no_header"         // no row looks like a header; columns get generated names
	KPossibleNote    = "possible_note"     // a single-value row under the header, kept as data
	KColumnName      = "column_name"       // header text was renamed/deduped
	KStripUnit       = "strip_unit"        // unit moved from header to Column.Unit
	KDropEmptyColumn = "drop_empty_column" // column with no header and no data
	KBlankRow        = "skip_blank_row"    // blank row inside a region
	KDropTotalRow    = "drop_total_row"    // totals/subtotals row removed
	KKeepTotalRow    = "keep_total_row"    // totals row kept because of --keep-totals
	KSkipFooter      = "skip_footer"       // footnote/source row removed
	KPossibleFooter  = "possible_footer"   // trailing sparse row kept as data
	KFillDown        = "fill_down"         // merged/blank cells filled from above
	KNullToken       = "null_token"        // placeholder like N/A or — read as NULL
	KExcelError      = "excel_error"       // #DIV/0! etc. read as NULL
	KCastFailNull    = "cast_fail_null"    // value failed the column type under --lenient
	KTypeWidened     = "type_widened"      // column fell back to a wider type because of a few values
	KStripCurrency   = "strip_currency"    // currency symbols removed
	KPercent         = "percent"           // "12%" stored as 12 in a _pct column
	KAccountingNeg   = "accounting_negative"
	KDateOrder       = "date_order"      // DD/MM vs MM/DD resolution
	KExcelSerialDate = "excel_serial"    // numeric serials read as dates
	KDateParse       = "date_parse"      // text dates parsed
	KTrimWhitespace  = "trim_whitespace" // leading/trailing whitespace removed from text
)

// Decision is one logged transformation.
type Decision struct {
	Kind       string  `json:"kind"`
	Range      string  `json:"range"` // A1 notation, qualified with the sheet: "Sheet1!A1:F2"
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`

	// Row and Col anchor the decision for ordering (0-based); -1 sorts first.
	Row int `json:"-"`
	Col int `json:"-"`
}

// NewDecision builds a decision anchored at the top-left of rng.
func NewDecision(kind, sheet string, rng Range, conf float64, reason string) Decision {
	return Decision{
		Kind:       kind,
		Range:      QualifiedRange(sheet, rng),
		Reason:     reason,
		Confidence: RoundConf(conf),
		Row:        rng.R1,
		Col:        rng.C1,
	}
}

// RoundConf clamps a confidence to [0,1] and rounds it to two decimals so
// output is stable across platforms.
func RoundConf(c float64) float64 {
	if c < 0 {
		c = 0
	}
	if c > 1 {
		c = 1
	}
	return math.Round(c*100) / 100
}

// SortDecisions orders decisions by row, then kind, then column, then range
// and reason, which makes output fully deterministic.
func SortDecisions(ds []Decision) { slices.SortStableFunc(ds, CompareDecisions) }

// CompareDecisions is the canonical decision order used by SortDecisions.
func CompareDecisions(a, b Decision) int {
	return cmp.Or(
		cmp.Compare(a.Row, b.Row),
		cmp.Compare(a.Kind, b.Kind),
		cmp.Compare(a.Col, b.Col),
		cmp.Compare(a.Range, b.Range),
		cmp.Compare(a.Reason, b.Reason),
	)
}

// Cell roles drive the UI coloring. Every non-empty cell in a sheet gets
// exactly one role; empty cells inside a table get RoleBlank.
const (
	RoleTitle   = "title"
	RoleHeader  = "header"
	RoleData    = "data"
	RoleTotal   = "total"
	RoleFooter  = "footer"
	RoleNull    = "null"
	RoleBlank   = "blank"
	RoleIgnored = "ignored" // content outside any detected table (e.g. a region too small to load)
)
