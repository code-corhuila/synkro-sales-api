package persistence

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// ErrInvalidSaleDocument means persisted data cannot be restored faithfully.
var ErrInvalidSaleDocument = errors.New("sale document cannot be represented by the domain")

type saleDocument struct {
	ID             string           `bson:"_id"`
	CustomerID     string           `bson:"customerId"`
	CreatedBy      string           `bson:"createdBy"`
	Date           bson.DateTime    `bson:"date"`
	TotalCents     int64            `bson:"totalCents"`
	Active         bool             `bson:"active"`
	IdempotencyKey string           `bson:"idempotencyKey"`
	Details        []detailDocument `bson:"details"`
}

type detailDocument struct {
	ID             string `bson:"detailId"`
	ProductID      string `bson:"productId"`
	Quantity       int32  `bson:"quantity"`
	UnitPriceCents int64  `bson:"unitPriceCents"`
	SubtotalCents  int64  `bson:"subtotalCents"`
	Active         bool   `bson:"active"`
}

func invalidDocument(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidSaleDocument, reason)
}

func canonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func validKey(key string) bool {
	n := utf8.RuneCountInString(key)
	return utf8.ValidString(key) && n >= 8 && n <= 128
}

func encodeSale(key string, sale *model.Sale) (bson.Raw, error) {
	if sale == nil {
		return nil, invalidDocument("nil sale")
	}
	doc := saleDocument{ID: sale.ID(), CustomerID: sale.CustomerID(), CreatedBy: sale.CreatedBy(), Date: bson.NewDateTimeFromTime(sale.Date()), TotalCents: sale.TotalCents(), Active: sale.Active(), IdempotencyKey: key}
	for _, line := range sale.Details() {
		// RegisterSale/AddDetail guarantee int32 quantities and freeze prices.
		// Every detail created by the current domain is active; inactive stored
		// details are rejected on restoration instead of inventing lifecycle state.
		doc.Details = append(doc.Details, detailDocument{ID: line.ID(), ProductID: line.ProductID(), Quantity: int32(line.Quantity()), UnitPriceCents: line.UnitPriceCents(), SubtotalCents: line.SubtotalCents(), Active: true})
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode sale: %w", err)
	}
	// Apply the same representability checks at both boundaries. Amounts in
	// the write document always come from the domain, never from the mapper.
	if _, err := decodeSale(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// exactFields rejects unknown, missing, duplicate and coerced BSON fields.
// It is a read boundary, not a replacement for the migrated MongoDB validator.
func exactFields(raw bson.Raw, expected map[string]bson.Type) error {
	elements, err := raw.Elements()
	if err != nil {
		return invalidDocument("malformed BSON")
	}
	if len(elements) != len(expected) {
		return invalidDocument("unexpected field count")
	}
	seen := make(map[string]bool, len(elements))
	for _, element := range elements {
		name := element.Key()
		kind, ok := expected[name]
		if !ok || seen[name] || kind != element.Value().Type {
			return invalidDocument("unexpected field or BSON type: " + name)
		}
		seen[name] = true
	}
	return nil
}

func decodeSale(raw bson.Raw) (*model.Sale, error) {
	if err := raw.Validate(); err != nil {
		return nil, invalidDocument("malformed BSON")
	}
	if err := exactFields(raw, map[string]bson.Type{"_id": bson.TypeString, "customerId": bson.TypeString, "createdBy": bson.TypeString, "date": bson.TypeDateTime, "totalCents": bson.TypeInt64, "active": bson.TypeBoolean, "idempotencyKey": bson.TypeString, "details": bson.TypeArray}); err != nil {
		return nil, err
	}
	values, err := raw.Lookup("details").Array().Values()
	if err != nil {
		return nil, invalidDocument("malformed details array")
	}
	for _, value := range values {
		if value.Type != bson.TypeEmbeddedDocument {
			return nil, invalidDocument("detail is not an object")
		}
		if err := exactFields(value.Document(), map[string]bson.Type{"detailId": bson.TypeString, "productId": bson.TypeString, "quantity": bson.TypeInt32, "unitPriceCents": bson.TypeInt64, "subtotalCents": bson.TypeInt64, "active": bson.TypeBoolean}); err != nil {
			return nil, err
		}
	}
	var doc saleDocument
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSaleDocument, err)
	}
	if !canonicalUUID(doc.ID) || !canonicalUUID(doc.CustomerID) || !canonicalUUID(doc.CreatedBy) {
		return nil, invalidDocument("noncanonical root UUID")
	}
	if !validKey(doc.IdempotencyKey) {
		return nil, invalidDocument("invalid idempotency key")
	}
	if !doc.Active {
		return nil, invalidDocument("inactive sale restoration is unsupported")
	}
	lines := make([]model.LineInput, len(doc.Details))
	for i, detail := range doc.Details {
		if !canonicalUUID(detail.ID) || !canonicalUUID(detail.ProductID) {
			return nil, invalidDocument("noncanonical detail UUID")
		}
		if !detail.Active {
			return nil, invalidDocument("inactive detail restoration is unsupported")
		}
		lines[i] = model.LineInput{DetailID: detail.ID, ProductID: detail.ProductID, Quantity: int(detail.Quantity), UnitPriceCents: detail.UnitPriceCents}
	}
	// BSON datetime floors to an epoch millisecond, including before 1970.
	// Both new and replayed results use that same UTC instant.
	sale, err := model.RegisterSale(doc.ID, doc.CustomerID, doc.CreatedBy, time.UnixMilli(int64(doc.Date)).UTC(), lines)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSaleDocument, err)
	}
	if sale.TotalCents() != doc.TotalCents {
		return nil, invalidDocument("total differs from domain calculation")
	}
	for i, detail := range sale.Details() {
		if detail.SubtotalCents() != doc.Details[i].SubtotalCents {
			return nil, invalidDocument("subtotal differs from domain calculation")
		}
	}
	return sale, nil
}
