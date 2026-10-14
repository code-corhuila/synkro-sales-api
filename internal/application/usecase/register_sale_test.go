package usecase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

var (
	_ in.RegisterSaleUseCase = (*RegisterSale)(nil)
	_ out.SaleRepository     = (*fakeSales)(nil)
	_ out.IDGenerator        = (*sequentialIDs)(nil)
	_ out.Clock              = (*fixedClock)(nil)
)

type createRequest struct {
	ctx  context.Context
	key  string
	sale *model.Sale
}

// fakeSales models the atomic port contract only; it does not test MongoDB.
type fakeSales struct {
	mu       sync.Mutex
	stored   map[string]*model.Sale
	requests []createRequest
	err      error
}

func (f *fakeSales) CreateOnce(ctx context.Context, key string, sale *model.Sale) (*model.Sale, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, createRequest{ctx: ctx, key: key, sale: sale})
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if f.err != nil {
		// A repository may return partial data alongside an error. The caller
		// must still return no successful registration result.
		return sale, true, f.err
	}
	if original, exists := f.stored[key]; exists {
		return original, false, nil
	}
	if f.stored == nil {
		f.stored = make(map[string]*model.Sale)
	}
	f.stored[key] = sale
	return sale, true, nil
}

type sequentialIDs struct {
	mu   sync.Mutex
	next int
}

func (f *sequentialIDs) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return fmt.Sprintf("id-%d", f.next)
}

type fixedClock struct{ date time.Time }

func (f *fixedClock) Now() time.Time { return f.date }

func registrationCommand(count int) in.RegisterSaleCommand {
	lines := make([]in.RegisterSaleLine, count)
	for i := range lines {
		lines[i] = in.RegisterSaleLine{
			ProductID: fmt.Sprintf("product-%d", i), Quantity: 2, UnitPriceCents: 1250,
		}
	}
	return in.RegisterSaleCommand{
		IdempotencyKey: "saga-123:register-sale", CustomerID: "customer-1", CreatedBy: "user-1", Lines: lines,
	}
}

func registrationFixture() (*RegisterSale, *fakeSales, *sequentialIDs, *fixedClock) {
	repo := &fakeSales{}
	ids := &sequentialIDs{}
	clock := &fixedClock{date: time.Date(2026, 10, 11, 9, 30, 15, 123000000, time.FixedZone("Bogota", -5*60*60))}
	return NewRegisterSale(repo, ids, clock), repo, ids, clock
}

func assertNoRegistration(t *testing.T, result in.RegisterSaleResult) {
	t.Helper()
	if result.Sale != nil || result.Created {
		t.Fatalf("failed registration returned success: %+v", result)
	}
}

func TestRegisterSale_Succeeds(t *testing.T) {
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprintf("%d lines", count), func(t *testing.T) {
			uc, repo, ids, clock := registrationFixture()
			cmd := registrationCommand(count)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, err := uc.Register(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Created || result.Sale == nil {
				t.Fatalf("expected a newly created sale: %+v", result)
			}
			sale := result.Sale
			if sale.ID() != "id-1" || sale.CustomerID() != cmd.CustomerID || sale.CreatedBy() != cmd.CreatedBy || !sale.Active() {
				t.Errorf("sale header not preserved: %+v", sale)
			}
			if !sale.Date().Equal(clock.date) || sale.Date().Location() != time.UTC {
				t.Errorf("date = %v, want %v in UTC", sale.Date(), clock.date.UTC())
			}
			details := sale.Details()
			if len(details) != count {
				t.Fatalf("details = %d, want %d", len(details), count)
			}
			for i, detail := range details {
				if detail.ID() != fmt.Sprintf("id-%d", i+2) || detail.ProductID() != cmd.Lines[i].ProductID || detail.Quantity() != 2 || detail.UnitPriceCents() != 1250 || detail.SubtotalCents() != 2500 {
					t.Errorf("unexpected detail %d: %+v", i, detail)
				}
			}
			if sale.TotalCents() != int64(count)*2500 || ids.next != count+1 {
				t.Errorf("total = %d, generated IDs = %d", sale.TotalCents(), ids.next)
			}
			if len(repo.requests) != 1 {
				t.Fatalf("CreateOnce calls = %d, want 1", len(repo.requests))
			}
			request := repo.requests[0]
			if request.key != cmd.IdempotencyKey || request.ctx != ctx || request.sale != sale || len(repo.stored) != 1 {
				t.Errorf("repository did not receive the key, context and complete aggregate: %+v", request)
			}
		})
	}
}

func TestRegisterSale_DomainValidation(t *testing.T) {
	quantityTooLarge := int64(math.MaxInt32) + 1
	tests := []struct {
		name  string
		lines []in.RegisterSaleLine
		want  error
	}{
		{"zero lines", nil, model.ErrNoLines},
		{"101 lines", registrationCommand(101).Lines, model.ErrTooManyLines},
		{"zero quantity", []in.RegisterSaleLine{{Quantity: 0, UnitPriceCents: 1}}, model.ErrInvalidQuantity},
		{"negative quantity", []in.RegisterSaleLine{{Quantity: -1, UnitPriceCents: 1}}, model.ErrInvalidQuantity},
		{"quantity above int32", []in.RegisterSaleLine{{Quantity: int(quantityTooLarge), UnitPriceCents: 1}}, model.ErrQuantityTooLarge},
		{"zero price", []in.RegisterSaleLine{{Quantity: 1, UnitPriceCents: 0}}, model.ErrInvalidUnitPrice},
		{"negative price", []in.RegisterSaleLine{{Quantity: 1, UnitPriceCents: -1}}, model.ErrInvalidUnitPrice},
		{"multiplication overflow", []in.RegisterSaleLine{{Quantity: 2, UnitPriceCents: math.MaxInt64/2 + 1}}, model.ErrAmountOverflow},
		{"int32 multiplication overflow", []in.RegisterSaleLine{{Quantity: math.MaxInt32, UnitPriceCents: math.MaxInt64/math.MaxInt32 + 1}}, model.ErrAmountOverflow},
		{"addition overflow", []in.RegisterSaleLine{{Quantity: 1, UnitPriceCents: math.MaxInt64 - 1}, {Quantity: 1, UnitPriceCents: 2}}, model.ErrAmountOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, repo, _, _ := registrationFixture()
			cmd := registrationCommand(1)
			cmd.Lines = tt.lines
			result, err := uc.Register(context.Background(), cmd)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			assertNoRegistration(t, result)
			if len(repo.requests) != 0 || len(repo.stored) != 0 {
				t.Fatal("domain rejection reached persistence")
			}
		})
	}
}

func TestRegisterSale_AmountBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		lines []in.RegisterSaleLine
		total int64
	}{
		{"maximum unit price", []in.RegisterSaleLine{{Quantity: 1, UnitPriceCents: math.MaxInt64}}, math.MaxInt64},
		{"largest even product", []in.RegisterSaleLine{{Quantity: 2, UnitPriceCents: math.MaxInt64 / 2}}, math.MaxInt64 - 1},
		{"maximum int32 quantity", []in.RegisterSaleLine{{Quantity: math.MaxInt32, UnitPriceCents: math.MaxInt64 / math.MaxInt32}}, math.MaxInt32 * (math.MaxInt64 / math.MaxInt32)},
		{"total at maximum", []in.RegisterSaleLine{{Quantity: 1, UnitPriceCents: math.MaxInt64 - 1}, {Quantity: 1, UnitPriceCents: 1}}, math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, _, _, _ := registrationFixture()
			cmd := registrationCommand(1)
			cmd.Lines = tt.lines
			result, err := uc.Register(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Created || result.Sale == nil || result.Sale.TotalCents() != tt.total {
				t.Fatalf("result = %+v, want total %d", result, tt.total)
			}
		})
	}
}

func TestRegisterSale_IdempotencyKey(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		valid bool
	}{
		{"empty", "", false},
		{"7 characters", strings.Repeat("k", 7), false},
		{"8 characters", strings.Repeat("k", 8), true},
		{"128 characters", strings.Repeat("k", 128), true},
		{"129 characters", strings.Repeat("k", 129), false},
		{"7 multibyte characters", strings.Repeat("é", 7), false},
		{"8 multibyte characters", strings.Repeat("é", 8), true},
		{"128 multibyte characters", strings.Repeat("é", 128), true},
		{"129 multibyte characters", strings.Repeat("é", 129), false},
		{"spaces are not restricted", strings.Repeat(" ", 8), true},
		{"punctuation is not restricted", "key: /!?", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, repo, ids, _ := registrationFixture()
			cmd := registrationCommand(1)
			cmd.IdempotencyKey = tt.key
			result, err := uc.Register(context.Background(), cmd)
			if tt.valid {
				if err != nil || !result.Created || result.Sale == nil || len(repo.requests) != 1 {
					t.Fatalf("valid key rejected: result=%+v err=%v requests=%d", result, err, len(repo.requests))
				}
				if repo.requests[0].key != tt.key {
					t.Error("key was changed before persistence")
				}
				return
			}
			if !errors.Is(err, in.ErrInvalidIdempotencyKey) {
				t.Fatalf("error = %v, want ErrInvalidIdempotencyKey", err)
			}
			assertNoRegistration(t, result)
			if len(repo.requests) != 0 || ids.next != 0 {
				t.Error("invalid key reached persistence or ID generation")
			}
		})
	}
}

func TestRegisterSale_IdempotentReplayKeepsOriginal(t *testing.T) {
	uc, repo, _, clock := registrationFixture()
	cmd := registrationCommand(1)
	first, err := uc.Register(context.Background(), cmd)
	if err != nil || !first.Created || first.Sale == nil {
		t.Fatalf("first registration: %+v, %v", first, err)
	}
	originalDate := first.Sale.Date()
	originalDetails := first.Sale.Details()
	// Even a different valid payload and a later clock must return the original.
	cmd.CustomerID = "customer-2"
	cmd.CreatedBy = "user-2"
	cmd.Lines = []in.RegisterSaleLine{{ProductID: "product-2", Quantity: 3, UnitPriceCents: 9999}}
	clock.date = clock.date.Add(24 * time.Hour)
	again, err := uc.Register(context.Background(), cmd)
	if err != nil || again.Created || again.Sale == nil {
		t.Fatalf("replay: %+v, %v", again, err)
	}
	if again.Sale != first.Sale || again.Sale.ID() != first.Sale.ID() || again.Sale.CustomerID() != "customer-1" || again.Sale.CreatedBy() != "user-1" || !again.Sale.Date().Equal(originalDate) || again.Sale.TotalCents() != 2500 || !again.Sale.Active() || !reflect.DeepEqual(again.Sale.Details(), originalDetails) {
		t.Errorf("replay replaced original sale: %+v", again.Sale)
	}
	if len(repo.stored) != 1 || repo.stored[cmd.IdempotencyKey] != first.Sale || len(repo.requests) != 2 {
		t.Errorf("replay did not keep exactly one stored sale: %+v", repo)
	}
	if repo.requests[1].sale.ID() == first.Sale.ID() {
		t.Error("fixture did not exercise a newly generated attempted sale")
	}
}

func TestRegisterSale_DifferentKeysCreateDifferentSales(t *testing.T) {
	uc, repo, _, _ := registrationFixture()
	cmd := registrationCommand(1)
	first, err := uc.Register(context.Background(), cmd)
	if err != nil || !first.Created || first.Sale == nil {
		t.Fatalf("first registration: %+v, %v", first, err)
	}
	cmd.IdempotencyKey = "saga-456:register-sale"
	second, err := uc.Register(context.Background(), cmd)
	if err != nil || !second.Created || second.Sale == nil {
		t.Fatalf("second registration: %+v, %v", second, err)
	}
	if first.Sale.ID() == second.Sale.ID() || len(repo.stored) != 2 || len(repo.requests) != 2 {
		t.Fatal("different keys did not create separate sales")
	}
}

func TestRegisterSale_RepositoryFailure(t *testing.T) {
	uc, repo, _, _ := registrationFixture()
	want := errors.New("persistence unavailable")
	repo.err = want
	result, err := uc.Register(context.Background(), registrationCommand(1))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want repository error %v", err, want)
	}
	assertNoRegistration(t, result)
	if len(repo.requests) != 1 || len(repo.stored) != 0 {
		t.Fatal("failed persistence must not be retried automatically")
	}
}

func TestRegisterSale_CancelledContext(t *testing.T) {
	uc, repo, _, _ := registrationFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := uc.Register(ctx, registrationCommand(1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	assertNoRegistration(t, result)
	if len(repo.requests) != 1 || repo.requests[0].ctx != ctx || len(repo.stored) != 0 {
		t.Fatal("repository must receive the cancelled context without retrying or storing")
	}
}

func TestRegisterSale_ConcurrentReplay(t *testing.T) {
	const callers = 32
	uc, repo, ids, _ := registrationFixture()
	cmd := registrationCommand(1)
	type outcome struct {
		result in.RegisterSaleResult
		err    error
	}
	results := make(chan outcome, callers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < callers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := uc.Register(context.Background(), cmd)
			results <- outcome{result, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	created := 0
	original := repo.stored[cmd.IdempotencyKey]
	for got := range results {
		if got.err != nil || got.result.Sale == nil {
			t.Fatalf("concurrent registration: %+v, %v", got.result, got.err)
		}
		if got.result.Sale != original || got.result.Sale.ID() != original.ID() {
			t.Error("concurrent replay returned a different sale")
		}
		if got.result.Created {
			created++
		}
	}
	if created != 1 || len(repo.stored) != 1 || len(repo.requests) != callers || ids.next != callers*2 {
		t.Errorf("created=%d stored=%d attempts=%d IDs=%d", created, len(repo.stored), len(repo.requests), ids.next)
	}
}
