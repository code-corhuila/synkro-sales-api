package httpapi

import (
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"time"
)

type SaleResponse struct {
	SaleID     string               `json:"saleId"`
	CustomerID string               `json:"customerId"`
	CreatedBy  string               `json:"createdBy"`
	Date       string               `json:"date"`
	TotalCents int64                `json:"totalCents"`
	Active     bool                 `json:"active"`
	Details    []SaleDetailResponse `json:"details"`
}
type SaleDetailResponse struct {
	DetailID       string `json:"detailId"`
	ProductID      string `json:"productId"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	SubtotalCents  int64  `json:"subtotalCents"`
	Active         bool   `json:"active"`
}
type PageMeta struct {
	Page       int64 `json:"page"`
	Limit      int64 `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"totalPages"`
}
type ListSalesResponse struct {
	Data []SaleResponse `json:"data"`
	Meta PageMeta       `json:"meta"`
}

func saleResponse(s *model.Sale) SaleResponse {
	response := SaleResponse{SaleID: s.ID(), CustomerID: s.CustomerID(), CreatedBy: s.CreatedBy(), Date: s.Date().UTC().Format(time.RFC3339Nano), TotalCents: s.TotalCents(), Active: s.Active(), Details: []SaleDetailResponse{}}
	// The existing decoder rejects inactive details: every representable domain
	// line is active. Never fabricate a restored historical lifecycle state.
	for _, d := range s.Details() {
		response.Details = append(response.Details, SaleDetailResponse{DetailID: d.ID(), ProductID: d.ProductID(), Quantity: d.Quantity(), UnitPriceCents: d.UnitPriceCents(), SubtotalCents: d.SubtotalCents(), Active: true})
	}
	return response
}

type DailyTotalResponse struct {
	Day             string `json:"day"`
	DailyTotalCents int64  `json:"dailyTotalCents"`
	SaleCount       int64  `json:"saleCount"`
}
type MonthlyTotalResponse struct {
	Month             string `json:"month"`
	MonthlyTotalCents int64  `json:"monthlyTotalCents"`
	SaleCount         int64  `json:"saleCount"`
}
type TopProductResponse struct {
	ProductID         string `json:"productId"`
	TotalQuantity     int64  `json:"totalQuantity"`
	TotalRevenueCents int64  `json:"totalRevenueCents"`
}
type ReportResponse[R any] struct {
	Data []R      `json:"data"`
	Meta PageMeta `json:"meta"`
}
