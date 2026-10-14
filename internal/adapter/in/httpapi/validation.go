package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
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
	Quantity       int    `json:"quantity" validate:"gte=1"`
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
	values, win, ok := parseWindow(w, r, "customerId")
	if !ok {
		return in.ListSalesQuery{}, false
	}
	q := in.ListSalesQuery{Page: win.page, Limit: win.limit, From: win.from, Until: win.until}
	if value := values.Get("customerId"); value != "" {
		if !canonicalID(value) {
			validationError(w, "customerId", "must be a canonical UUID")
			return q, false
		}
		q.CustomerID = value
	}
	return q, true
}
