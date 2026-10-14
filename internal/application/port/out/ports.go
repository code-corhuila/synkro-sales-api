package out

import (
	"context"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

type SaleRepository interface {
	// CreateOnce atomically stores the complete sale together with its key.
	// An unused key returns the stored sale and created=true. An existing key
	// returns the original sale and created=false, without replacing it.
	// Concurrent attempts with the same key must create at most one sale.
	// Implementations must honor ctx cancellation and return persistence errors.
	CreateOnce(ctx context.Context, idempotencyKey string, sale *model.Sale) (originalOrCreated *model.Sale, created bool, err error)
}

// IDGenerator supplies a new unique identifier for each sale and detail.
// Implementations must support concurrent calls when the use case is shared.
type IDGenerator interface {
	NewID() string
}

// Clock provides the creation instant. Implementations must support concurrent
// calls when the use case is shared; the use case normalizes the instant to UTC.
type Clock interface {
	Now() time.Time
}
