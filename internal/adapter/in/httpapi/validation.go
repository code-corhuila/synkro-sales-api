package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/go-playground/validator/v10"
)

type registerRequest struct {
	CustomerID string           `json:"customerId"`
	CreatedBy  string           `json:"createdBy"`
	Details    []registerDetail `json:"details" validate:"required,min=1,max=100,dive"`
}
type registerDetail struct {
	ProductID      string `json:"productId"`
	Quantity       int    `json:"quantity" validate:"gte=1,lte=2147483647"`
	UnitPriceCents int64  `json:"unitPriceCents" validate:"gte=1"`
}

var requestValidator = newRequestValidator()

func newRequestValidator() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(func(field reflect.StructField) string { return strings.SplitN(field.Tag.Get("json"), ",", 2)[0] })
	return v
}

func validationError(w http.ResponseWriter, field, message string) {
	writeError(w, 400, "VALIDATION_ERROR", "Invalid request", []ErrorDetail{{Field: field, Message: message}})
}
func registrationCommand(w http.ResponseWriter, r *http.Request) (in.RegisterSaleCommand, bool) {
	keys := r.Header.Values("Idempotency-Key")
	key := r.Header.Get("Idempotency-Key")
	n := utf8.RuneCountInString(key)
	if len(keys) != 1 || !utf8.ValidString(key) || n < 8 || n > 128 {
		validationError(w, "Idempotency-Key", "must contain 8 to 128 characters")
		return in.RegisterSaleCommand{}, false
	}
	var body registerRequest
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil || uniqueJSON(raw) != nil || requestFields(raw) != nil {
		validationError(w, "body", "must contain one unambiguous JSON object with documented fields")
		return in.RegisterSaleCommand{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil {
		validationError(w, "body", "must be a valid JSON object with only documented fields")
		return in.RegisterSaleCommand{}, false
	}
	if err := requestValidator.Struct(body); err != nil {
		field := "details"
		if violations, ok := err.(validator.ValidationErrors); ok && len(violations) > 0 {
			field = strings.TrimPrefix(violations[0].Namespace(), "registerRequest.")
		}
		validationError(w, field, "must contain 1 to 100 lines with positive integer quantities and prices")
		return in.RegisterSaleCommand{}, false
	}
	if !canonicalID(body.CustomerID) {
		validationError(w, "customerId", "must be a canonical UUID")
		return in.RegisterSaleCommand{}, false
	}
	if !canonicalID(body.CreatedBy) {
		validationError(w, "createdBy", "must be a canonical UUID")
		return in.RegisterSaleCommand{}, false
	}
	cmd := in.RegisterSaleCommand{IdempotencyKey: key, CustomerID: body.CustomerID, CreatedBy: body.CreatedBy, Lines: make([]in.RegisterSaleLine, len(body.Details))}
	for i, d := range body.Details {
		field := fmt.Sprintf("details[%d]", i)
		if !canonicalID(d.ProductID) {
			validationError(w, field+".productId", "must be a canonical UUID")
			return in.RegisterSaleCommand{}, false
		}
		cmd.Lines[i] = in.RegisterSaleLine{ProductID: d.ProductID, Quantity: d.Quantity, UnitPriceCents: d.UnitPriceCents}
	}
	return cmd, true
}

func listQuery(w http.ResponseWriter, r *http.Request) (in.ListSalesQuery, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		validationError(w, "query", "malformed query parameters")
		return in.ListSalesQuery{}, false
	}
	allowed := map[string]bool{"page": true, "limit": true, "from": true, "to": true, "customerId": true}
	for name, items := range values {
		if !allowed[name] || len(items) != 1 || items[0] == "" {
			validationError(w, "query", "unknown, empty or repeated filter")
			return in.ListSalesQuery{}, false
		}
	}
	q := in.ListSalesQuery{Page: 1, Limit: 20}
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
		end := to.AddDate(0, 0, 1)
		q.Until = &end
	}
	if value := values.Get("customerId"); value != "" {
		if !canonicalID(value) {
			validationError(w, "customerId", "must be a canonical UUID")
			return q, false
		}
		q.CustomerID = value
	}
	return q, true
}
