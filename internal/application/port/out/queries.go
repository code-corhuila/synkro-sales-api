package out

import (
	"context"
	"errors"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"time"
)

var ErrSaleNotFound = errors.New("active sale not found")

type SaleFilter struct {
	CustomerID, CreatedBy string
	From, Until           *time.Time
	Page, Limit           int64
}

// All reads exclude inactive sales. From is inclusive; Until is exclusive.
// List and count apply the same scalar ownership filter in the database.
type SaleReadRepository interface {
	FindActiveByID(context.Context, string) (*model.Sale, error)
	ListActive(context.Context, SaleFilter) ([]*model.Sale, error)
	CountActive(context.Context, SaleFilter) (int64, error)
}
