package model

import "time"

// Report projections are derived on demand from active sales (ADR-005
// Decision 5, ADR-010 Decision 4). They are values with no identity or
// lifecycle: nothing here is persisted or a new business entity.

// DailyTotal summarizes active sales recorded on one UTC calendar day.
type DailyTotal struct {
	Day                   time.Time // midnight UTC
	TotalCents, SaleCount int64
}

// MonthlyTotal summarizes active sales recorded in one UTC calendar month.
type MonthlyTotal struct {
	Month                 time.Time // first day of the month, midnight UTC
	TotalCents, SaleCount int64
}

// ProductSales summarizes the active lines that sold one product.
type ProductSales struct {
	ProductID                        string
	TotalQuantity, TotalRevenueCents int64
}
