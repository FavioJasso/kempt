package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/FavioJasso/kempt/internal/detect"
	"github.com/FavioJasso/kempt/internal/infer"
	"github.com/FavioJasso/kempt/internal/plan"
	"github.com/FavioJasso/kempt/internal/read"
)

// maxUnzipRatio bounds how much an uploaded XLSX may expand when unzipped,
// which protects the server from zip bombs.
const maxUnzipRatio = 40

// upload is a parsed /api/inspect or /api/load request.
type upload struct {
	name  string
	data  []byte
	sheet string
	opts  plan.Options
}

// httpError carries an HTTP status with a user-facing message.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, a ...any) error {
	return &httpError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

// parseUpload reads a multipart form entirely in memory: the spreadsheet in
// field "file" (or a bundled sample named by field "sample") plus optional
// override fields. Nothing is written to disk.
func (s *Server) parseUpload(w http.ResponseWriter, r *http.Request) (*upload, error) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		return nil, badRequest("send the spreadsheet as multipart/form-data in a field named \"file\"")
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUpload+64<<10)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, badRequest("invalid multipart body: %v", err)
	}
	up := &upload{opts: plan.Options{RoleRows: gridRows}}
	fields := map[string]string{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, s.tooLarge()
			}
			return nil, badRequest("invalid multipart body: %v", err)
		}
		if part.FormName() == "file" && part.FileName() != "" {
			data, err := io.ReadAll(io.LimitReader(part, s.maxUpload+1))
			if err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					return nil, s.tooLarge()
				}
				return nil, badRequest("reading upload: %v", err)
			}
			if int64(len(data)) > s.maxUpload {
				return nil, s.tooLarge()
			}
			up.name, up.data = path.Base(strings.ReplaceAll(part.FileName(), `\`, "/")), data
			continue
		}
		v, err := io.ReadAll(io.LimitReader(part, 4<<10))
		if err != nil {
			return nil, badRequest("reading field %s: %v", part.FormName(), err)
		}
		fields[part.FormName()] = strings.TrimSpace(string(v))
	}

	if up.data == nil {
		name := fields["sample"]
		if name == "" {
			return nil, badRequest("no file uploaded (field \"file\") and no sample named (field \"sample\")")
		}
		if s.cfg.Samples == nil || !fs.ValidPath(name) || strings.Contains(name, "/") {
			return nil, &httpError{http.StatusNotFound, "unknown sample " + strconv.Quote(name)}
		}
		data, err := fs.ReadFile(s.cfg.Samples, name)
		if err != nil {
			return nil, &httpError{http.StatusNotFound, "unknown sample " + strconv.Quote(name)}
		}
		up.name, up.data = name, data
	}
	if err := up.applyFields(fields); err != nil {
		return nil, err
	}
	if err := checkZip(up.data, s.maxUpload); err != nil {
		return nil, err
	}
	return up, nil
}

func (s *Server) tooLarge() error {
	return &httpError{http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than %d MB", s.maxUpload>>20)}
}

// applyFields maps the optional form fields onto planning options.
func (up *upload) applyFields(f map[string]string) error {
	atoi := func(key string) (int, error) {
		if f[key] == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(f[key])
		if err != nil || n < 0 || n > 1000 {
			return 0, badRequest("%s must be a whole number between 0 and 1000", key)
		}
		return n, nil
	}
	var err error
	d := detect.Options{}
	if d.HeaderRows, err = atoi("header_rows"); err != nil {
		return err
	}
	if d.SkipRows, err = atoi("skip_rows"); err != nil {
		return err
	}
	d.SkipRowsSet = f["skip_rows"] != ""
	d.KeepTotals = f["keep_totals"] == "1" || f["keep_totals"] == "true" || f["keep_totals"] == "on"
	for _, c := range strings.Split(f["fill_down"], ",") {
		if c = strings.TrimSpace(c); c != "" {
			d.FillDown = append(d.FillDown, c)
		}
	}
	in := infer.Options{}
	if v := f["lenient"]; v != "" {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || x < 0 || x > 1 {
			return badRequest("lenient must be a number between 0 and 1")
		}
		in.Lenient = x
	}
	switch o := strings.ToLower(f["date_order"]); o {
	case "", "auto":
	case "mdy", "dmy":
		in.DateOrder = o
	default:
		return badRequest("date_order must be mdy or dmy")
	}
	up.opts.Detect, up.opts.Infer = d, in
	up.sheet = f["sheet"]
	return nil
}

// checkZip rejects XLSX uploads whose uncompressed size is unreasonable.
// Only real zip archives count: a CSV may start with "PK" ("PKG_ID,...").
func checkZip(data []byte, maxUpload int64) error {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return nil
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return badRequest("the file looks like an XLSX but is not a valid zip archive")
	}
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
	}
	if total > uint64(maxUpload)*maxUnzipRatio {
		return &httpError{http.StatusRequestEntityTooLarge, "the workbook expands to more data than this server accepts"}
	}
	return nil
}

// analyze reads and plans an upload, at most a few at a time: reading a
// large workbook takes hundreds of megabytes.
func (s *Server) analyze(ctx context.Context, up *upload) ([]*plan.Sheet, error) {
	release, err := acquire(ctx, s.analyses)
	if err != nil {
		return nil, err
	}
	defer release()
	grids, err := read.Bytes(up.name, up.data, read.Options{Sheet: up.sheet, MaxRows: s.maxRows, MaxCells: s.maxCells, CalcLimit: s.calcLimit})
	if err != nil {
		switch {
		case errors.Is(err, read.ErrTooManyRows):
			return nil, &httpError{http.StatusRequestEntityTooLarge, fmt.Sprintf("the sheet has more than %d rows", s.maxRows)}
		case errors.Is(err, read.ErrTooManyCells):
			return nil, &httpError{http.StatusRequestEntityTooLarge, fmt.Sprintf("the sheet spans more than %d cells", s.maxCells)}
		case errors.Is(err, read.ErrUnsupported), errors.Is(err, read.ErrSheetNotFound), errors.Is(err, read.ErrTooSparse):
			return nil, badRequest("%s", strings.TrimPrefix(err.Error(), "read: "))
		}
		return nil, badRequest("could not read %s: %v", up.name, err)
	}
	if s.maxRows > 0 {
		total := 0
		for _, g := range grids {
			total += g.Rows
		}
		if total > s.maxRows {
			return nil, &httpError{http.StatusRequestEntityTooLarge, fmt.Sprintf("the workbook has %d rows across its sheets; this server accepts at most %d", total, s.maxRows)}
		}
	}
	sheets := make([]*plan.Sheet, 0, len(grids))
	for _, g := range grids {
		sheets = append(sheets, plan.Build(g, up.opts))
	}
	plan.UniqueTables(sheets)
	return sheets, nil
}
