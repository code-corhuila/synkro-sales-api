package in

import (
	"context"
	"errors"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"time"
)

var ErrSaleNotFound = errors.New("sale not found")
var ErrInvalidQuery = errors.New("invalid sales query")

type ListSalesQuery struct {
	Page, Limit           int64
	CustomerID, CreatedBy string
	From, Until           *time.Time
}
type ListSalesResult struct {
	Sales                          []*model.Sale
	Page, Limit, Total, TotalPages int64
}
type SalesQueryUseCase interface {
	Get(context.Context, string) (*model.Sale, error)
	List(context.Context, ListSalesQuery) (ListSalesResult, error)
}
