package httpapi

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/application/usecase"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
)

const ownerID = "550e8400-e29b-41d4-a716-446655440000"
const otherID = "550e8400-e29b-41d4-a716-446655440001"
const customerID = "550e8400-e29b-41d4-a716-446655440002"
const productID = "550e8400-e29b-41d4-a716-446655440003"
const requestBody = `{"customerId":"` + customerID + `","createdBy":"` + ownerID + `","details":[{"productId":"` + productID + `","quantity":2,"unitPriceCents":1250}]}`

type memorySales struct {
	mu       sync.Mutex
	sales    map[string]*model.Sale
	keys     map[string]string
	inactive map[string]bool
	failure  error
	calls    int
}

func (f *memorySales) CreateOnce(ctx context.Context, key string, s *model.Sale) (*model.Sale, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if f.failure != nil {
		return nil, false, f.failure
	}
	if id, ok := f.keys[key]; ok {
		return f.sales[id], false, nil
	}
	f.sales[s.ID()] = s
	f.keys[key] = s.ID()
	return s, true, nil
}
func (f *memorySales) FindActiveByID(ctx context.Context, id string) (*model.Sale, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f.failure != nil {
		return nil, f.failure
	}
	s := f.sales[id]
	if s == nil || f.inactive[id] {
		return nil, out.ErrSaleNotFound
	}
	return s, nil
}
func (f *memorySales) matching(filter out.SaleFilter) []*model.Sale {
	rows := []*model.Sale{}
	for _, s := range f.sales {
		if f.inactive[s.ID()] || filter.CreatedBy != "" && s.CreatedBy() != filter.CreatedBy || filter.CustomerID != "" && s.CustomerID() != filter.CustomerID || filter.From != nil && s.Date().Before(*filter.From) || filter.Until != nil && !s.Date().Before(*filter.Until) {
			continue
		}
		rows = append(rows, s)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date().Equal(rows[j].Date()) {
			return rows[i].ID() > rows[j].ID()
		}
		return rows[i].Date().After(rows[j].Date())
	})
	return rows
}
func (f *memorySales) ListActive(ctx context.Context, filter out.SaleFilter) ([]*model.Sale, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f.failure != nil {
		return nil, f.failure
	}
	rows := f.matching(filter)
	start := (filter.Page - 1) * filter.Limit
	if start >= int64(len(rows)) {
		return []*model.Sale{}, nil
	}
	end := start + filter.Limit
	if end > int64(len(rows)) {
		end = int64(len(rows))
	}
	return rows[start:end], nil
}
func (f *memorySales) CountActive(ctx context.Context, filter out.SaleFilter) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if f.failure != nil {
		return 0, f.failure
	}
	return int64(len(f.matching(filter))), nil
}

type randomIDs struct{}

func (randomIDs) NewID() string { return uuid.NewString() }

type httpFixture struct {
	router http.Handler
	repo   *memorySales
	key    *rsa.PrivateKey
	now    time.Time
}

func newHTTPFixture(t *testing.T) httpFixture {
	t.Helper()
	key, public := testKey(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := fixedClock{now}
	verifier, err := NewJWTVerifier(public, clock)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memorySales{sales: map[string]*model.Sale{}, keys: map[string]string{}, inactive: map[string]bool{}}
	return httpFixture{repo: repo, key: key, now: now, router: NewRouter(Dependencies{Register: usecase.NewRegisterSale(repo, randomIDs{}, clock), Queries: usecase.NewSalesQueries(repo), Verifier: verifier, Health: func(context.Context) error { return nil }, Clock: clock, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Timeout: time.Second})}
}
func (f httpFixture) request(t *testing.T, method, path, body, sub, role string, permissions ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		r.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, sub, role, permissions...)))
	}
	r.Header.Set("Idempotency-Key", "saga-key:register-sale")
	r.Header.Set("X-Correlation-Id", "contract-trace")
	r.Header.Set("X-User-Id", otherID)
	r.Header.Set("X-User-Roles", "ADMIN")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Header().Get("X-Correlation-Id") != "contract-trace" {
		t.Fatal("correlation lost")
	}
	return w
}
func assertHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var e errorResponse
	if json.Unmarshal(w.Body.Bytes(), &e) != nil || w.Code != status || e.Error != code || e.Message == "" || e.TraceID != w.Header().Get("X-Correlation-Id") {
		t.Fatalf("status=%d body=%s want=%d %s", w.Code, w.Body.String(), status, code)
	}
	if code == "VALIDATION_ERROR" && len(e.Details) == 0 {
		t.Fatal("missing field details")
	}
	if strings.Contains(w.Body.String(), "secret-database") {
		t.Fatal("internal error leaked")
	}
}
func fixtureSale(t *testing.T, id, owner, customer string, date time.Time) *model.Sale {
	t.Helper()
	s, err := model.RegisterSale(id, customer, owner, date, []model.LineInput{{DetailID: uuid.NewString(), ProductID: productID, Quantity: 2, UnitPriceCents: 1250}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSalesHTTPRegistrationAndReplay(t *testing.T) {
	f := newHTTPFixture(t)
	first := f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register")
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body.String())
	}
	var got SaleResponse
	if err := json.Unmarshal(first.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !canonicalID(got.SaleID) || got.CreatedBy != ownerID || got.TotalCents != 2500 || !got.Active || len(got.Details) != 1 || !got.Details[0].Active || got.Details[0].UnitPriceCents != 1250 || got.Details[0].SubtotalCents != 2500 || first.Header().Get("Location") != "/api/v1/sales/"+got.SaleID || !strings.HasSuffix(got.Date, "Z") {
		t.Fatalf("DTO=%+v", got)
	}
	var raw map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &raw)
	if len(raw) != 7 {
		t.Fatal("unexpected response fields", raw)
	}
	var line map[string]any
	_ = json.Unmarshal([]byte(mustJSON(t, got.Details[0])), &line)
	if len(line) != 6 {
		t.Fatal("unexpected detail fields", line)
	}
	replay := f.request(t, "POST", "/api/v1/sales", strings.ReplaceAll(requestBody, "1250", "9000"), "synkro-workflow", "SERVICE", "sales:register")
	if replay.Code != 200 || replay.Body.String() != first.Body.String() || len(f.repo.sales) != 1 {
		t.Fatal("replay changed original", replay.Code, replay.Body.String())
	}
	for _, role := range []string{"ADMIN", "SALESPERSON", "INVENTORY", "SERVICE"} {
		sub := ownerID
		if role == "SERVICE" {
			sub = "synkro-workflow"
		}
		before := f.repo.calls
		w := f.request(t, "POST", "/api/v1/sales", requestBody, sub, role, "sales:read")
		assertHTTPError(t, w, 403, "FORBIDDEN")
		if f.repo.calls != before {
			t.Fatal("unauthorized replay touched persistence")
		}
	}
	for _, role := range []string{"ADMIN", "SALESPERSON", "INVENTORY"} {
		before := f.repo.calls
		w := f.request(t, "POST", "/api/v1/sales", requestBody, ownerID, role, "sales:register")
		assertHTTPError(t, w, 403, "FORBIDDEN")
		if f.repo.calls != before {
			t.Fatal("human direct registration reached repository")
		}
	}
	assertHTTPError(t, f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-worker", "SERVICE", "sales:register"), 403, "FORBIDDEN")
	assertHTTPError(t, f.request(t, "POST", "/api/v1/sales", requestBody, "", ""), 401, "UNAUTHORIZED")
	overflow := strings.ReplaceAll(requestBody, "1250", "9223372036854775807")
	assertHTTPError(t, f.request(t, "POST", "/api/v1/sales", overflow, "synkro-workflow", "SERVICE", "sales:register"), 422, "BUSINESS_RULE_VIOLATION")
	f.repo.failure = errors.New("secret-database password driver failure")
	assertHTTPError(t, f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register"), 500, "INTERNAL_ERROR")
}
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSalesHTTPValidation(t *testing.T) {
	f := newHTTPFixture(t)
	cases := []string{"", "{", "null", "[]", requestBody + "{}", strings.ReplaceAll(requestBody, `"customerId"`, `"saleId"`), strings.ReplaceAll(requestBody, customerID, "bad-id"), strings.ReplaceAll(requestBody, ownerID, strings.ToUpper(ownerID)), strings.ReplaceAll(requestBody, `"quantity":2`, `"quantity":2.5`), strings.ReplaceAll(requestBody, `"quantity":2`, `"quantity":"2"`), strings.ReplaceAll(requestBody, `"quantity":2`, `"quantity":null`), strings.ReplaceAll(requestBody, `"quantity":2`, `"quantity":0`), strings.ReplaceAll(requestBody, `"unitPriceCents":1250`, `"unitPriceCents":0`), strings.ReplaceAll(requestBody, `"unitPriceCents":1250`, `"unitPriceCents":null`), strings.ReplaceAll(requestBody, `"unitPriceCents":1250`, `"unitPriceCents":1.2`), strings.ReplaceAll(requestBody, `"productId"`, `"detailId"`), strings.ReplaceAll(requestBody, `"quantity":2`, `"subtotalCents":2500,"quantity":2`), strings.ReplaceAll(requestBody, `"details":`, `"totalCents":2500,"details":`), `{"customerId":"` + customerID + `","createdBy":"` + ownerID + `","details":[]}`, `{"customerId":"` + customerID + `","createdBy":"` + ownerID + `","details":null}`}
	var body map[string]any
	_ = json.Unmarshal([]byte(requestBody), &body)
	line := body["details"].([]any)[0]
	lines := make([]any, 101)
	for i := range lines {
		lines[i] = line
	}
	body["details"] = lines
	cases = append(cases, mustJSON(t, body))
	for i, b := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			before := f.repo.calls
			w := f.request(t, "POST", "/api/v1/sales", b, "synkro-workflow", "SERVICE", "sales:register")
			assertHTTPError(t, w, 400, "VALIDATION_ERROR")
			if f.repo.calls != before {
				t.Fatal("invalid body persisted")
			}
		})
	}
	for _, key := range []string{"", "short", strings.Repeat("a", 129)} {
		r := httptest.NewRequest("POST", "/api/v1/sales", strings.NewReader(requestBody))
		r.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, "synkro-workflow", "SERVICE", "sales:register")))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		assertHTTPError(t, w, 400, "VALIDATION_ERROR")
	}
	for _, query := range []string{"page=0", "page=-1", "page=1.2", "limit=0", "limit=101", "page=9223372036854775807&limit=100", "from=2026-02-30", "from=2026-10-12&to=2026-10-11", "to=2026-10-11T00:00:00Z", "customerId=%7B%22%24ne%22%3Anull%7D", "customerId[$ne]=x", "active=false", "createdBy=" + otherID, "limit=1&limit=2", "page=", "from=", "customerId=", "%ZZ"} {
		t.Run(query, func(t *testing.T) {
			before := f.repo.calls
			w := f.request(t, "GET", "/api/v1/sales?"+query, "", ownerID, "ADMIN", "sales:read")
			assertHTTPError(t, w, 400, "VALIDATION_ERROR")
			if f.repo.calls != before {
				t.Fatal("invalid filter reached database")
			}
		})
	}
	assertHTTPError(t, f.request(t, "GET", "/api/v1/sales/bad-id", "", ownerID, "ADMIN", "sales:read"), 400, "VALIDATION_ERROR")
}

func TestSalesHTTPQueriesAndOwnership(t *testing.T) {
	f := newHTTPFixture(t)
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	ids := []string{ownerID, otherID, customerID, productID, uuid.NewString()}
	dates := []time.Time{day, day, day.Add(24*time.Hour - time.Millisecond), day.Add(24 * time.Hour), day.Add(-time.Millisecond)}
	for i, id := range ids {
		owner := ownerID
		if i == 1 {
			owner = otherID
		}
		f.repo.sales[id] = fixtureSale(t, id, owner, customerID, dates[i])
	}
	hidden := uuid.NewString()
	f.repo.sales[hidden] = fixtureSale(t, hidden, ownerID, customerID, day)
	f.repo.inactive[hidden] = true
	for _, tc := range []struct {
		sub, role string
		want      int
	}{{ownerID, "ADMIN", 200}, {ownerID, "SALESPERSON", 200}, {otherID, "SALESPERSON", 403}, {ownerID, "INVENTORY", 403}, {"synkro-workflow", "SERVICE", 403}} {
		w := f.request(t, "GET", "/api/v1/sales/"+ownerID, "", tc.sub, tc.role, "sales:read")
		if tc.want == 200 {
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		} else {
			assertHTTPError(t, w, tc.want, "FORBIDDEN")
		}
	}
	for _, id := range []string{hidden, uuid.NewString()} {
		assertHTTPError(t, f.request(t, "GET", "/api/v1/sales/"+id, "", ownerID, "ADMIN", "sales:read"), 404, "NOT_FOUND")
	}
	for _, role := range []string{"ADMIN", "SALESPERSON", "INVENTORY", "SERVICE"} {
		sub := ownerID
		if role == "SERVICE" {
			sub = "synkro-workflow"
		}
		before := f.repo.calls
		assertHTTPError(t, f.request(t, "GET", "/api/v1/sales", "", sub, role, "products:read"), 403, "FORBIDDEN")
		if f.repo.calls != before {
			t.Fatal("denied query touched database")
		}
	}
	for _, role := range []string{"INVENTORY", "SERVICE"} {
		sub := ownerID
		if role == "SERVICE" {
			sub = "synkro-workflow"
		}
		before := f.repo.calls
		assertHTTPError(t, f.request(t, "GET", "/api/v1/sales", "", sub, role, "sales:read"), 403, "FORBIDDEN")
		if f.repo.calls != before {
			t.Fatal("denied list touched database")
		}
	}
	var allIDs []string
	for page := 1; page <= 4; page++ {
		w := f.request(t, "GET", fmt.Sprintf("/api/v1/sales?from=2026-10-11&to=2026-10-11&limit=1&page=%d", page), "", ownerID, "ADMIN", "sales:read")
		var response ListSalesResponse
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if w.Code != 200 || response.Meta.Total != 3 || response.Meta.TotalPages != 3 || response.Meta.Page != int64(page) {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, s := range response.Data {
			allIDs = append(allIDs, s.SaleID)
		}
	}
	if strings.Join(allIDs, ",") != customerID+","+otherID+","+ownerID {
		t.Fatal("UTC day or stable pagination incorrect", allIDs)
	}
	w := f.request(t, "GET", "/api/v1/sales?from=2026-10-11&to=2026-10-11&customerId="+customerID, "", ownerID, "SALESPERSON", "sales:read")
	var own ListSalesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &own)
	if w.Code != 200 || own.Meta.Total != 2 || len(own.Data) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, s := range own.Data {
		if s.CreatedBy != ownerID {
			t.Fatal("ownership leak")
		}
	}
	w = f.request(t, "GET", "/api/v1/sales?customerId="+uuid.NewString(), "", ownerID, "ADMIN", "sales:read")
	var empty ListSalesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &empty)
	if w.Code != 200 || empty.Data == nil || len(empty.Data) != 0 || empty.Meta.Total != 0 || empty.Meta.TotalPages != 0 || empty.Meta.Page != 1 || empty.Meta.Limit != 20 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.repo.failure = errors.New("secret-database driver failure")
	for _, path := range []string{"/api/v1/sales", "/api/v1/sales/" + ownerID} {
		assertHTTPError(t, f.request(t, "GET", path, "", ownerID, "ADMIN", "sales:read"), 500, "INTERNAL_ERROR")
	}
}

func TestRouterErrorsAndHealth(t *testing.T) {
	f := newHTTPFixture(t)
	for _, path := range []string{"/unknown", "/api/v1/unknown"} {
		assertHTTPError(t, f.request(t, "GET", path, "", ownerID, "ADMIN", "sales:read"), 404, "NOT_FOUND")
	}
	assertHTTPError(t, f.request(t, "DELETE", "/api/v1/sales", "", ownerID, "ADMIN", "sales:read"), 400, "VALIDATION_ERROR")
	if w := f.request(t, "GET", "/health", "", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"database":"ok"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	down := NewRouter(Dependencies{Clock: fixedClock{f.now}, Health: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded health context")
		}
		return errors.New("secret-database")
	}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	r := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	down.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"database":"down"`) || w.Header().Get("X-Correlation-Id") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
}
