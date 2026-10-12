package httpapi

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
)

// window is the validated pagination and UTC date range shared by every
// paginated read. until is exclusive: the midnight after the inclusive "to" day.
type window struct {
	page, limit int64
	from, until *time.Time
}

// parseWindow validates page, limit, from and to plus any extra allowed
// filter names, before any use case runs. It returns the parsed values so
// callers can read their extra filters. Unknown, empty and repeated
// parameters are rejected.
func parseWindow(w http.ResponseWriter, r *http.Request, extra ...string) (url.Values, window, bool) {
	win := window{page: 1, limit: 20}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		validationError(w, "query", "malformed query parameters")
		return nil, win, false
	}
	allowed := map[string]bool{"page": true, "limit": true, "from": true, "to": true}
	for _, name := range extra {
		allowed[name] = true
	}
	for name, items := range values {
		if !allowed[name] || len(items) != 1 || items[0] == "" {
			validationError(w, "query", "unknown, empty or repeated filter")
			return nil, win, false
		}
	}
	for _, field := range []string{"page", "limit"} {
		if value := values.Get(field); value != "" {
			if strings.Trim(value, "0123456789") != "" {
				validationError(w, field, "must be a positive integer")
				return nil, win, false
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 || field == "limit" && n > 100 {
				validationError(w, field, "out of range")
				return nil, win, false
			}
			if field == "page" {
				win.page = n
			} else {
				win.limit = n
			}
		}
	}
	if win.page-1 > math.MaxInt64/win.limit {
		validationError(w, "page", "pagination offset exceeds supported range")
		return nil, win, false
	}
	var from, to *time.Time
	for _, field := range []string{"from", "to"} {
		if value := values.Get(field); value != "" {
			day, err := time.Parse("2006-01-02", value)
			if err != nil || day.Format("2006-01-02") != value {
				validationError(w, field, "must be a UTC date in YYYY-MM-DD format")
				return nil, win, false
			}
			if field == "from" {
				from = &day
			} else {
				to = &day
			}
		}
	}
	if from != nil && to != nil && from.After(*to) {
		validationError(w, "from", "must be on or before to")
		return nil, win, false
	}
	win.from = from
	if to != nil {
		end := to.AddDate(0, 0, 1)
		win.until = &end
	}
	return values, win, true
}

// reportQuery accepts only page, limit, from and to: ownership comes from
// the verified token and can never be supplied by the caller.
func reportQuery(w http.ResponseWriter, r *http.Request) (in.ReportQuery, bool) {
	_, win, ok := parseWindow(w, r)
	if !ok {
		return in.ReportQuery{}, false
	}
	return in.ReportQuery{Page: win.page, Limit: win.limit, From: win.from, Until: win.until}, true
}
