package persistence

import (
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"go.mongodb.org/mongo-driver/v2/bson"
	"reflect"
	"testing"
	"time"
)

func TestActiveQueryUsesTypedScalarFilters(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(0, 0, 1)
	filter := activeFilter(out.SaleFilter{CustomerID: "customer", CreatedBy: "owner", From: &from, Until: &until})
	want := bson.D{{Key: "active", Value: true}, {Key: "customerId", Value: "customer"}, {Key: "createdBy", Value: "owner"}, {Key: "date", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lt", Value: until}}}}
	if !reflect.DeepEqual(filter, want) {
		t.Fatalf("filter=%v want=%v", filter, want)
	}
	if !reflect.DeepEqual(activeFilter(out.SaleFilter{}), bson.D{{Key: "active", Value: true}}) {
		t.Fatal("active filter missing")
	}
}
