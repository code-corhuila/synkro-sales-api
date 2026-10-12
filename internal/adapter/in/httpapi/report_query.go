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

// reportQuery validates every report filter before any use case runs. Only
// page, limit, from and to exist: ownership comes from the verified token and
// can never be supplied by the caller.
func reportQuery(w http.ResponseWriter, r *http.Request) (in.ReportQuery, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		validationError(w, "query", "malformed query parameters")
		return in.ReportQuery{}, false
	}
	allowed := map[string]bool{"page": true, "limit": true, "from": true, "to": true}
	for name, items := range values {
		if !allowed[name] || len(items) != 1 || items[0] == "" {
			validationError(w, "query", "unknown, empty or repeated filter")
			return in.ReportQuery{}, false
		}
	}
	q := in.ReportQuery{Page: 1, Limit: 20}
	for _, field := range []string{"page", "limit"} {
		if value := values.Get(field); value != "" {
			if strings.Trim(value, "0123456789") != "" {
				validationError(w, field, "must be a positive integer")
				return q, false
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 || field == "limit" && n > 100 {
				validationError(w, field, "out of range")
				return q, false
			}
			if field == "page" {
				q.Page = n
			} else {
				q.Limit = n
			}
		}
	}
	if q.Page-1 > math.MaxInt64/q.Limit {
		validationError(w, "page", "pagination offset exceeds supported range")
		return q, false
	}
	var from, to *time.Time
	for _, field := range []string{"from", "to"} {
		if value := values.Get(field); value != "" {
			day, err := time.Parse("2006-01-02", value)
			if err != nil || day.Format("2006-01-02") != value {
				validationError(w, field, "must be a UTC date in YYYY-MM-DD format")
				return q, false
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
		return q, false
	}
	q.From = from
	if to != nil {
		// "to" names an inclusive UTC day: the exclusive bound is the next midnight.
		end := to.AddDate(0, 0, 1)
		q.Until = &end
	}
	return q, true
}
