package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
)

// Issue 285: a reused key answers with the original only for the same business input.
func TestRegisterSale_ConflictingRetryIsRejectedWithoutReturningTheStoredSale(t *testing.T) {
	changes := map[string]func(*in.RegisterSaleCommand){
		"customer": func(c *in.RegisterSaleCommand) { c.CustomerID = "customer-2" },
		"author":   func(c *in.RegisterSaleCommand) { c.CreatedBy = "user-2" },
		"quantity": func(c *in.RegisterSaleCommand) { c.Lines[0].Quantity = 3 },
		"price":    func(c *in.RegisterSaleCommand) { c.Lines[0].UnitPriceCents = 9999 },
		"product":  func(c *in.RegisterSaleCommand) { c.Lines[0].ProductID = "product-9" },
		"extra line": func(c *in.RegisterSaleCommand) {
			c.Lines = append(c.Lines, in.RegisterSaleLine{ProductID: "product-8", Quantity: 1, UnitPriceCents: 1})
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			uc, repo, _, _ := registrationFixture()
			first, err := uc.Register(context.Background(), registrationCommand(1))
			if err != nil || !first.Created {
				t.Fatalf("first registration: %+v, %v", first, err)
			}
			retry := registrationCommand(1)
			change(&retry)
			got, err := uc.Register(context.Background(), retry)
			if !errors.Is(err, in.ErrIdempotencyConflict) || errors.Is(err, in.ErrPersistenceFailure) {
				t.Fatalf("err = %v, want the dedicated conflict and not a persistence failure", err)
			}
			if got.Sale != nil || got.Created {
				t.Fatalf("conflict returned a sale: %+v", got)
			}
			if len(repo.stored) != 1 || repo.stored[retry.IdempotencyKey] != first.Sale {
				t.Fatal("conflicting retry altered the stored sale")
			}
		})
	}
}

func TestRegisterSale_EquivalentRetryReturnsTheOriginalWhateverTheLineOrder(t *testing.T) {
	uc, repo, _, _ := registrationFixture()
	cmd := registrationCommand(3)
	first, err := uc.Register(context.Background(), cmd)
	if err != nil || !first.Created {
		t.Fatalf("first registration: %+v, %v", first, err)
	}
	reordered := cmd
	reordered.Lines = []in.RegisterSaleLine{cmd.Lines[2], cmd.Lines[0], cmd.Lines[1]}
	again, err := uc.Register(context.Background(), reordered)
	if err != nil || again.Created || again.Sale != first.Sale || len(repo.stored) != 1 {
		t.Fatalf("equivalent retry: %+v, %v", again, err)
	}
}
