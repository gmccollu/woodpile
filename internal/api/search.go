package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- types (docs/api.md "Severity filters", "Search", "Exports") ----

// Unknown is the severity of a message with no parseable PRI.
const Unknown = -1

// Severities is a severity filter. The zero value means all severities; None
// means nothing (severities=none); otherwise only Values (0-7, Unknown) pass.
type Severities struct {
	None   bool
	Values []int
}

// All reports whether the filter lets every severity through.
func (s Severities) All() bool { return !s.None && len(s.Values) == 0 }

// UnmarshalJSON reads the export body's form: [0, 1, "unknown"], ["none"] or
// [] (all).
func (s *Severities) UnmarshalJSON(b []byte) error {
	*s = Severities{}
	if string(b) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		return errors.New("severities must be a list of 0 to 7 and \"unknown\" (or [\"none\"])")
	}
	var parts []string
	for _, it := range items {
		var n int
		var str string
		switch {
		case json.Unmarshal(it, &n) == nil:
			parts = append(parts, strconv.Itoa(n))
		case json.Unmarshal(it, &str) == nil && str != "":
			parts = append(parts, str)
		default:
			return fmt.Errorf("severities: %s is not a severity (0 to 7, \"unknown\" or \"none\")", it)
		}
	}
	v, err := parseSeverityList(strings.Join(parts, ","))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// MarshalJSON writes the echo form: [] for all, ["none"], or the list.
func (s Severities) MarshalJSON() ([]byte, error) {
	out := []any{}
	switch {
	case s.None:
		out = append(out, "none")
	default:
		for _, v := range s.Values {
			if v == Unknown {
				out = append(out, "unknown")
			} else {
				out = append(out, v)
			}
		}
	}
	return json.Marshal(out)
}

// parseSeverityList reads "0,3,unknown" or "none" ("" is all).
func parseSeverityList(v string) (Severities, error) {
	var s Severities
	seen := map[int]bool{}
	none := false
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		switch {
		case item == "":
		case strings.EqualFold(item, "none"):
			none = true
		case strings.EqualFold(item, "unknown"):
			if !seen[Unknown] {
				seen[Unknown] = true
				s.Values = append(s.Values, Unknown)
			}
		case len(item) == 1 && item[0] >= '0' && item[0] <= '7':
			n := int(item[0] - '0')
			if !seen[n] {
				seen[n] = true
				s.Values = append(s.Values, n)
			}
		default:
			return Severities{}, fmt.Errorf("severities: %q is not a severity (0 to 7, unknown, or none on its own)", item)
		}
	}
	if none {
		if len(s.Values) > 0 {
			return Severities{}, errors.New("severities: none can't be combined with other values")
		}
		return Severities{None: true}, nil
	}
	return s, nil
}

// SearchRequest is GET /api/search, parsed and checked.
type SearchRequest struct {
	Q          string
	Regex      bool
	Case       bool
	Devices    []string
	Group      string
	Ungrouped  bool
	Severities Severities
	From, To   time.Time // zero: the default (to = now, from = to - 24 h)
	Asc        bool      // order=asc
	Limit      int       // 1-5,000
	Cursor     string
}

// SearchLine is one search result or context line.
type SearchLine struct {
	Received time.Time
	DeviceID string
	Severity int    // 0-7, or Unknown
	Raw      []byte // as received
	Ref      string
}

// MarshalJSON gives the docs/api.md shape: received with millisecond
// precision (like the log files), severity null when unknown, raw as a string
// (bytes that aren't valid UTF-8 become U+FFFD).
func (l SearchLine) MarshalJSON() ([]byte, error) {
	var sev any
	if l.Severity >= 0 && l.Severity <= 7 {
		sev = l.Severity
	}
	return json.Marshal(struct {
		Received string `json:"received"`
		DeviceID string `json:"device_id"`
		Severity any    `json:"severity"`
		Raw      string `json:"raw"`
		Ref      string `json:"ref"`
	}{l.Received.UTC().Format("2006-01-02T15:04:05.000Z07:00"), l.DeviceID, sev, string(bytes.ToValidUTF8(l.Raw, []byte("�"))), l.Ref})
}

// SearchPage is the body of GET /api/search.
type SearchPage struct {
	Results      []SearchLine `json:"results"`
	NextCursor   string       `json:"next_cursor"`
	ScannedBytes int64        `json:"scanned_bytes"`
	ScannedFiles int          `json:"scanned_files"`
	ElapsedMS    int64        `json:"elapsed_ms"`
	Partial      bool         `json:"partial"`
}

// ContextPage is the body of GET /api/context.
type ContextPage struct {
	Lines    []SearchLine `json:"lines"`
	HitIndex int          `json:"hit_index"`
	AtStart  bool         `json:"at_start"`
	AtEnd    bool         `json:"at_end"`
}

// ExportRequest is the body of POST /api/exports and /api/exports/estimate.
type ExportRequest struct {
	AllDevices bool // every non-hidden device (instead of Devices, Group or Ungrouped)
	Devices    []string
	Group      string
	Ungrouped  bool
	From, To   time.Time
	Severities Severities
	Q          string
	Regex      bool
	Case       bool
}

// ExportEstimate is the body of POST /api/exports/estimate.
type ExportEstimate struct {
	Lines int64 `json:"lines"`
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

// ExportFilters echoes an export's filters.
type ExportFilters struct {
	Group      string     `json:"group"`
	Ungrouped  bool       `json:"ungrouped"`
	Severities Severities `json:"severities"`
	Q          string     `json:"q"`
	Regex      bool       `json:"regex"`
	Case       bool       `json:"case"`
}

// ExportInfo is one export in GET /api/exports.
type ExportInfo struct {
	ID        string        `json:"id"`
	Created   time.Time     `json:"created"`
	Devices   []string      `json:"devices"`
	From      time.Time     `json:"from"`
	To        time.Time     `json:"to"`
	State     string        `json:"state"` // queued, building, ready, failed, expired
	Progress  float64       `json:"progress"`
	Lines     int64         `json:"lines"`
	SizeBytes int64         `json:"size_bytes"`
	Error     string        `json:"error"`
	ReadyAt   *time.Time    `json:"ready_at"`
	ExpiresAt *time.Time    `json:"expires_at"`
	Filters   ExportFilters `json:"filters"`
}

func (e *ExportInfo) normalize() {
	e.Devices = nonNil(e.Devices)
	e.Created, e.From, e.To = e.Created.UTC(), e.From.UTC(), e.To.UTC()
	for _, p := range []**time.Time{&e.ReadyAt, &e.ExpiresAt} {
		if *p != nil {
			t := (*p).UTC()
			*p = &t
		}
	}
}

// ExportDownload is an open, ready export.
type ExportDownload struct {
	File    io.ReadSeekCloser
	ModTime time.Time
}

// RequestError is returned by a Backend method for a request that's wrong in a
// way only the backend can tell (an invalid regular expression, a cursor from
// another search, a group with no devices): a 400 with Msg.
type RequestError struct{ Msg string }

func (e *RequestError) Error() string { return e.Msg }

// ErrNotReady is returned by OpenExport for an export that isn't ready (409).
var ErrNotReady = errors.New("not ready")

// ---- handlers ----

func parseBoolParam(v url.Values, name string) (bool, error) {
	switch s := v.Get(name); s {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false, not %q", name, s)
	}
}

func parseTimeParam(v url.Values, name string) (time.Time, error) {
	s := v.Get(name)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q must be an RFC 3339 time, not %q", name, s)
	}
	return t.UTC(), nil
}

func splitList(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// parseSeverityParams reads severities= (which wins) or min_severity=.
func parseSeverityParams(v url.Values) (Severities, error) {
	var ceiling *int
	if s := v.Get("min_severity"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 7 {
			return Severities{}, fmt.Errorf("min_severity must be an integer from 0 to 7, not %q", s)
		}
		ceiling = &n
	}
	set, err := parseSeverityList(v.Get("severities"))
	if err != nil {
		return Severities{}, err
	}
	if !set.All() || ceiling == nil {
		return set, nil
	}
	for i := 0; i <= *ceiling; i++ {
		set.Values = append(set.Values, i)
	}
	set.Values = append(set.Values, Unknown)
	return set, nil
}

// ParseSearch reads GET /api/search's query string (docs/api.md).
func ParseSearch(v url.Values) (SearchRequest, error) {
	var r SearchRequest
	var err error
	r.Q = v.Get("q")
	if r.Regex, err = parseBoolParam(v, "regex"); err != nil {
		return r, err
	}
	if r.Case, err = parseBoolParam(v, "case"); err != nil {
		return r, err
	}
	if r.Ungrouped, err = parseBoolParam(v, "ungrouped"); err != nil {
		return r, err
	}
	r.Devices, r.Group = splitList(v.Get("devices")), v.Get("group")
	if r.Group != "" && r.Ungrouped {
		return r, errors.New("group and ungrouped=true are alternatives; send one")
	}
	if r.Severities, err = parseSeverityParams(v); err != nil {
		return r, err
	}
	if r.From, err = parseTimeParam(v, "from"); err != nil {
		return r, err
	}
	if r.To, err = parseTimeParam(v, "to"); err != nil {
		return r, err
	}
	if !r.From.IsZero() && !r.To.IsZero() && !r.From.Before(r.To) {
		return r, errors.New(`"from" must be before "to"`)
	}
	switch o := v.Get("order"); o {
	case "", "desc":
	case "asc":
		r.Asc = true
	default:
		return r, fmt.Errorf("order must be desc or asc, not %q", o)
	}
	r.Limit = 500
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return r, fmt.Errorf("limit must be a positive integer, not %q", s)
		}
		r.Limit = min(n, 5000)
	}
	r.Cursor = v.Get("cursor")
	return r, nil
}

// requestOrBackendError answers a Backend error: 400 for a RequestError, 404
// for ErrNotFound, 409 for ErrNotReady, 500 otherwise.
func (s *Server) requestOrBackendError(w http.ResponseWriter, r *http.Request, err error, notFound string) {
	var re *RequestError
	switch {
	case errors.As(err, &re):
		writeError(w, http.StatusBadRequest, re.Msg)
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, notFound)
	case errors.Is(err, ErrNotReady):
		writeError(w, http.StatusConflict, "The export isn't ready yet.")
	case errors.Is(err, r.Context().Err()) && r.Context().Err() != nil:
		// The client went away; nobody reads this.
		writeError(w, http.StatusServiceUnavailable, "The request was cancelled.")
	default:
		s.backendError(w, r, err)
	}
}

func (s *Server) getSearch(w http.ResponseWriter, r *http.Request) {
	req, err := ParseSearch(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.backend.Search(r.Context(), req)
	if err != nil {
		s.requestOrBackendError(w, r, err, "Not found.")
		return
	}
	page.Results = nonNil(page.Results)
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	ref := v.Get("ref")
	if ref == "" {
		writeError(w, http.StatusBadRequest, "ref is required.")
		return
	}
	n := map[string]int{"before": 50, "after": 50}
	for _, k := range []string{"before", "after"} {
		if str := v.Get(k); str != "" {
			x, err := strconv.Atoi(str)
			if err != nil || x < 0 {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be an integer from 0 to 500, not %q", k, str))
				return
			}
			n[k] = min(x, 500)
		}
	}
	page, err := s.backend.Context(r.Context(), ref, n["before"], n["after"])
	if err != nil {
		s.requestOrBackendError(w, r, err, "That line's file is gone (deleted by retention).")
		return
	}
	page.Lines = nonNil(page.Lines)
	writeJSON(w, http.StatusOK, page)
}

// exportBody is the JSON body of POST /api/exports.
type exportBody struct {
	AllDevices bool       `json:"all_devices"`
	Devices    []string   `json:"devices"`
	Group      string     `json:"group"`
	Ungrouped  bool       `json:"ungrouped"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	Severities Severities `json:"severities"`
	Q          string     `json:"q"`
	Regex      bool       `json:"regex"`
	Case       bool       `json:"case"`
}

func (s *Server) readExportRequest(w http.ResponseWriter, r *http.Request) (ExportRequest, bool) {
	var b exportBody
	if !decodeJSON(w, r, &b) {
		return ExportRequest{}, false
	}
	req := ExportRequest{AllDevices: b.AllDevices, Group: b.Group, Ungrouped: b.Ungrouped, Severities: b.Severities, Q: b.Q, Regex: b.Regex, Case: b.Case}
	for _, d := range b.Devices {
		if d = strings.TrimSpace(d); d != "" {
			req.Devices = append(req.Devices, d)
		}
	}
	bad := func(msg string) (ExportRequest, bool) {
		writeError(w, http.StatusBadRequest, msg)
		return ExportRequest{}, false
	}
	var err error
	for _, f := range []struct {
		name, v string
		t       *time.Time
	}{{"from", b.From, &req.From}, {"to", b.To, &req.To}} {
		if f.v == "" {
			return bad(fmt.Sprintf("%q is required.", f.name))
		}
		if *f.t, err = time.Parse(time.RFC3339Nano, f.v); err != nil {
			return bad(fmt.Sprintf("%q must be an RFC 3339 time, not %q.", f.name, f.v))
		}
	}
	if !req.From.Before(req.To) {
		return bad(`"from" must be before "to".`)
	}
	if req.Group != "" && req.Ungrouped {
		return bad("group and ungrouped=true are alternatives; send one.")
	}
	if req.AllDevices && (len(req.Devices) > 0 || req.Group != "" || req.Ungrouped) {
		return bad("all_devices picks every device; don't send devices, group or ungrouped with it.")
	}
	if !req.AllDevices && len(req.Devices) == 0 && req.Group == "" && !req.Ungrouped {
		return bad("Pick at least one device (devices, group, ungrouped or all_devices).")
	}
	return req, true
}

func (s *Server) postExport(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readExportRequest(w, r)
	if !ok {
		return
	}
	info, err := s.backend.CreateExport(r.Context(), req)
	if err != nil {
		s.requestOrBackendError(w, r, err, "Not found.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": info.ID})
}

func (s *Server) postEstimate(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readExportRequest(w, r)
	if !ok {
		return
	}
	est, err := s.backend.EstimateExport(r.Context(), req)
	if err != nil {
		s.requestOrBackendError(w, r, err, "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, est)
}

func (s *Server) listExports(w http.ResponseWriter, r *http.Request) {
	es, err := s.backend.Exports(r.Context())
	if err != nil {
		s.backendError(w, r, err)
		return
	}
	es = nonNil(es)
	for i := range es {
		es[i].normalize()
	}
	writeJSON(w, http.StatusOK, es)
}

var exportIDRE = regexp.MustCompile(`^exp_[0-9a-f]{16}$`)

// exportID reads {id}, answering 404 itself for anything that isn't an export
// ID (routes take IDs, never paths).
func exportID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !exportIDRE.MatchString(id) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("No export with id %q.", id))
		return "", false
	}
	return id, true
}

func (s *Server) getExport(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	e, err := s.backend.Export(r.Context(), id)
	if err != nil {
		s.requestOrBackendError(w, r, err, fmt.Sprintf("No export with id %q.", id))
		return
	}
	e.normalize()
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) deleteExport(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	if err := s.backend.DeleteExport(r.Context(), id); err != nil {
		s.requestOrBackendError(w, r, err, fmt.Sprintf("No export with id %q.", id))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) downloadExport(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	info, err := s.backend.Export(r.Context(), id)
	if err != nil {
		s.requestOrBackendError(w, r, err, fmt.Sprintf("No export with id %q.", id))
		return
	}
	if info.State == "expired" {
		writeError(w, http.StatusNotFound, "That export has expired (exports are deleted 24 hours after they're ready).")
		return
	}
	dl, err := s.backend.OpenExport(r.Context(), id)
	if err != nil {
		s.requestOrBackendError(w, r, err, fmt.Sprintf("No export with id %q.", id))
		return
	}
	defer dl.File.Close()
	name := fmt.Sprintf("woodpile-%s-%s.zip", info.From.UTC().Format("20060102T150405Z"), info.To.UTC().Format("20060102T150405Z"))
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, dl.ModTime, dl.File)
}
