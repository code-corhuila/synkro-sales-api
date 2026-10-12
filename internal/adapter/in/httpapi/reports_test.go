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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/usecase"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

const (
	dailyPath      = "/api/v1/sales/reports/daily"
	monthlyPath    = "/api/v1/sales/reports/monthly"
	topProductPath = "/api/v1/sales/reports/top-products"
)

// reportSpy records every query it receives and answers from canned pages.
type reportSpy struct {
	mu      sync.Mutex
	kinds   []string
	queries []in.ReportQuery
	daily   in.ReportPage[model.DailyTotal]
	monthly in.ReportPage[model.MonthlyTotal]
	top     in.ReportPage[model.ProductSales]
	err     error
	block   bool
}

func (s *reportSpy) record(ctx context.Context, kind string, q in.ReportQuery) error {
	s.mu.Lock()
	s.kinds, s.queries = append(s.kinds, kind), append(s.queries, q)
	err, block := s.err, s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}
func (s *reportSpy) Daily(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.DailyTotal], error) {
	return s.daily, s.record(ctx, "daily", q)
}
func (s *reportSpy) Monthly(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.MonthlyTotal], error) {
	return s.monthly, s.record(ctx, "monthly", q)
}
func (s *reportSpy) TopProducts(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.ProductSales], error) {
	return s.top, s.record(ctx, "top", q)
}
func (s *reportSpy) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.kinds)
}

var (
	reportKeyOnce sync.Once
	reportKey     *rsa.PrivateKey
	reportPublic  string
)

type reportFixture struct {
	router http.Handler
	spy    *reportSpy
	repo   *memorySales
	key    *rsa.PrivateKey
	now    time.Time
}

func newReportFixture(t *testing.T, withReports bool) reportFixture {
	t.Helper()
	// RSA key generation dominates a table of ~150 fixtures: share one key.
	reportKeyOnce.Do(func() { reportKey, reportPublic = testKey(t) })
	key, public := reportKey, reportPublic
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := fixedClock{now}
	verifier, err := NewJWTVerifier(public, clock)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memorySales{sales: map[string]*model.Sale{}, keys: map[string]string{}, inactive: map[string]bool{}}
	spy := &reportSpy{
		daily:   in.ReportPage[model.DailyTotal]{Items: []model.DailyTotal{{Day: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), TotalCents: 250000, SaleCount: 2}}, Page: 1, Limit: 20, Total: 1, TotalPages: 1},
		monthly: in.ReportPage[model.MonthlyTotal]{Items: []model.MonthlyTotal{{Month: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), TotalCents: 9223372036854775807, SaleCount: 3}}, Page: 1, Limit: 20, Total: 1, TotalPages: 1},
		top:     in.ReportPage[model.ProductSales]{Items: []model.ProductSales{{ProductID: productID, TotalQuantity: 6, TotalRevenueCents: 600}}, Page: 1, Limit: 20, Total: 1, TotalPages: 1},
	}
	d := Dependencies{Register: usecase.NewRegisterSale(repo, randomIDs{}, clock), Queries: usecase.NewSalesQueries(repo), Verifier: verifier, Health: func(context.Context) error { return nil }, Clock: clock, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Timeout: 200 * time.Millisecond}
	if withReports {
		d.Reports = spy
	}
	return reportFixture{router: NewRouter(d), spy: spy, repo: repo, key: key, now: now}
}

// get sends a GET with a bearer token for role/permissions. The forged
// X-User-* headers must never be treated as identity (ADR-006).
func (f reportFixture) get(t *testing.T, target, sub, role string, permissions ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", target, nil)
	if role != "" {
		r.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, sub, role, permissions...)))
	}
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

func (f reportFixture) admin(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return f.get(t, target, ownerID, "ADMIN", "reports:read")
}

func TestReportRoutesAreDispatchedBeforeSaleDetail(t *testing.T) {
	f := newReportFixture(t, true)
	for path, kind := range map[string]string{dailyPath: "daily", monthlyPath: "monthly", topProductPath: "top"} {
		before := f.spy.calls()
		w := f.admin(t, path)
		if w.Code != 200 || f.spy.calls() != before+1 || f.spy.kinds[before] != kind {
			t.Fatalf("%s: status=%d kinds=%v body=%s", path, w.Code, f.spy.kinds, w.Body.String())
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("report is not JSON")
		}
	}
	if f.repo.calls != 0 {
		t.Fatal("a report path was treated as a sale identifier")
	}
}

func TestOnlyExactReportPathsAreRoutes(t *testing.T) {
	f := newReportFixture(t, true)
	for _, path := range []string{"/api/v1/sales/reports", "/api/v1/sales/reports/", "/api/v1/sales/reports/daily/", "/api/v1/sales/reports/daily/extra", "/api/v1/sales/reports/yearly", "/api/v1/sales/reports//daily", "/api/v1/sales/Reports/daily", "/api/v1/sales/reports/Daily", "/api/v1/sales/reports/top-products/1", "/api/v1/reports/daily"} {
		assertHTTPError(t, f.admin(t, path), 404, "NOT_FOUND")
	}
	if f.spy.calls() != 0 || f.repo.calls != 0 {
		t.Fatal("an unknown report path reached a use case")
	}
}

func TestReportRoutesRequireGet(t *testing.T) {
	f := newReportFixture(t, true)
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			r := httptest.NewRequest(method, path, strings.NewReader("{}"))
			r.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, ownerID, "ADMIN", "reports:read")))
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, r)
			assertHTTPError(t, w, 400, "VALIDATION_ERROR")
			if w.Header().Get("Allow") != "GET" {
				t.Fatalf("%s %s Allow=%q", method, path, w.Header().Get("Allow"))
			}
		}
	}
	if f.spy.calls() != 0 {
		t.Fatal("non-GET reached a use case")
	}
}

func TestExistingSalesRoutesAreUnchangedByReports(t *testing.T) {
	f := newReportFixture(t, true)
	created := f.get(t, "/api/v1/sales", "", "")
	assertHTTPError(t, created, 401, "UNAUTHORIZED")
	list := f.get(t, "/api/v1/sales", ownerID, "ADMIN", "sales:read")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"meta"`) {
		t.Fatalf("listing changed: %d %s", list.Code, list.Body.String())
	}
	assertHTTPError(t, f.get(t, "/api/v1/sales/"+productID, ownerID, "ADMIN", "sales:read"), 404, "NOT_FOUND")
	assertHTTPError(t, f.get(t, "/api/v1/sales/not-a-uuid", ownerID, "ADMIN", "sales:read"), 400, "VALIDATION_ERROR")
	post := httptest.NewRequest("POST", "/api/v1/sales", strings.NewReader(requestBody))
	post.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, "synkro-workflow", "SERVICE", "sales:register")))
	post.Header.Set("Idempotency-Key", "saga-key:register-sale")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, post)
	if w.Code != 201 {
		t.Fatalf("registration changed: %d %s", w.Code, w.Body.String())
	}
	health := httptest.NewRecorder()
	f.router.ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
	if health.Code != 200 {
		t.Fatalf("health changed: %d", health.Code)
	}
	if f.spy.calls() != 0 {
		t.Fatal("sale routes reached the report use cases")
	}
}

func TestReportsRequireAuthentication(t *testing.T) {
	f := newReportFixture(t, true)
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		assertHTTPError(t, f.get(t, path, "", ""), 401, "UNAUTHORIZED")
		bad := httptest.NewRequest("GET", path, nil)
		bad.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "HS256", claimsAt(f.now, ownerID, "ADMIN", "reports:read")))
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, bad)
		assertHTTPError(t, w, 401, "UNAUTHORIZED")
		expired := claimsAt(f.now.Add(-time.Hour), ownerID, "ADMIN", "reports:read")
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", expired))
		w = httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		assertHTTPError(t, w, 401, "UNAUTHORIZED")
	}
	if f.spy.calls() != 0 {
		t.Fatal("unauthenticated report request reached a use case")
	}
}

func TestReportAuthorizationAndOwnership(t *testing.T) {
	f := newReportFixture(t, true)
	denied := []struct {
		name, sub, role string
		permissions     []string
	}{
		{"inventory", ownerID, "INVENTORY", []string{"reports:read"}},
		{"service", "synkro-workflow", "SERVICE", []string{"reports:read"}},
		{"worker", "synkro-worker", "SERVICE", []string{"reports:read", "sales:read"}},
		{"admin without reports:read", ownerID, "ADMIN", []string{"sales:read"}},
		{"salesperson without reports:read", ownerID, "SALESPERSON", []string{"sales:read", "sales:create"}},
		{"admin without permissions", ownerID, "ADMIN", []string{}},
	}
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		for _, c := range denied {
			t.Run(c.name+" "+path, func(t *testing.T) {
				// Authorization precedes validation: an invalid query still answers 403.
				assertHTTPError(t, f.get(t, path+"?limit=abc", c.sub, c.role, c.permissions...), 403, "FORBIDDEN")
				assertHTTPError(t, f.get(t, path, c.sub, c.role, c.permissions...), 403, "FORBIDDEN")
			})
		}
	}
	if f.spy.calls() != 0 {
		t.Fatal("forbidden role reached a use case")
	}
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		if w := f.get(t, path, ownerID, "ADMIN", "reports:read"); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		if w := f.get(t, path, ownerID, "SALESPERSON", "reports:read"); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if len(f.spy.queries) != 6 {
		t.Fatalf("calls=%d", len(f.spy.queries))
	}
	for i, q := range f.spy.queries {
		wantOwner := ""
		if i%2 == 1 {
			wantOwner = ownerID // from the verified token's sub, never from X-User-Id
		}
		if q.CreatedBy != wantOwner {
			t.Fatalf("call %d CreatedBy=%q want=%q", i, q.CreatedBy, wantOwner)
		}
	}
}

func TestReportsRejectUserSuppliedOwnership(t *testing.T) {
	f := newReportFixture(t, true)
	for _, role := range []string{"ADMIN", "SALESPERSON"} {
		for _, param := range []string{"createdBy", "userId", "owner", "sub", "customerId", "role"} {
			w := f.get(t, dailyPath+"?"+param+"="+otherID, ownerID, role, "reports:read")
			assertHTTPError(t, w, 400, "VALIDATION_ERROR")
		}
	}
	if f.spy.calls() != 0 {
		t.Fatal("ownership override reached a use case")
	}
}

func TestReportFilterValidation(t *testing.T) {
	cases := map[string]string{
		"unknown parameter":   "?foo=1",
		"repeated from":       "?from=2026-10-01&from=2026-10-02",
		"repeated to":         "?to=2026-10-01&to=2026-10-02",
		"repeated page":       "?page=1&page=2",
		"repeated limit":      "?limit=1&limit=2",
		"empty from":          "?from=",
		"empty to":            "?to=",
		"empty page":          "?page=",
		"empty limit":         "?limit=",
		"reversed range":      "?from=2026-10-02&to=2026-10-01",
		"invalid day":         "?from=2026-02-30",
		"invalid leap day":    "?to=2027-02-29",
		"invalid month":       "?from=2026-13-01",
		"slash format":        "?from=2026/10/01",
		"compact format":      "?from=20261001",
		"date time":           "?from=2026-10-01T00:00:00Z",
		"padded":              "?from=%202026-10-01",
		"page zero":           "?page=0",
		"page negative":       "?page=-1",
		"page fraction":       "?page=1.5",
		"page text":           "?page=abc",
		"page plus sign":      "?page=%2B1",
		"page overflow":       "?page=99999999999999999999",
		"offset overflow":     "?page=9223372036854775807&limit=100",
		"limit zero":          "?limit=0",
		"limit over maximum":  "?limit=101",
		"limit negative":      "?limit=-5",
		"limit fraction":      "?limit=2.5",
		"limit text":          "?limit=many",
		"malformed escape":    "?from=%zz",
		"semicolon separator": "?page=1;limit=2",
		"case sensitive name": "?From=2026-10-01",
	}
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		for name, query := range cases {
			t.Run(path+" "+name, func(t *testing.T) {
				f := newReportFixture(t, true)
				w := f.admin(t, path+query)
				assertHTTPError(t, w, 400, "VALIDATION_ERROR")
				if f.spy.calls() != 0 || f.repo.calls != 0 {
					t.Fatal("invalid filters reached the data layer")
				}
			})
		}
	}
}

func TestReportFilterMapping(t *testing.T) {
	day := func(y int, m time.Month, d int) *time.Time {
		v := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return &v
	}
	cases := []struct {
		name, query string
		want        in.ReportQuery
	}{
		{"defaults", "", in.ReportQuery{Page: 1, Limit: 20}},
		{"explicit paging", "?page=3&limit=100", in.ReportQuery{Page: 3, Limit: 100}},
		{"single from", "?from=2026-10-12", in.ReportQuery{Page: 1, Limit: 20, From: day(2026, 10, 12)}},
		{"single to is inclusive", "?to=2026-10-12", in.ReportQuery{Page: 1, Limit: 20, Until: day(2026, 10, 13)}},
		{"same day range", "?from=2026-10-12&to=2026-10-12", in.ReportQuery{Page: 1, Limit: 20, From: day(2026, 10, 12), Until: day(2026, 10, 13)}},
		{"month end", "?from=2026-10-01&to=2026-10-31", in.ReportQuery{Page: 1, Limit: 20, From: day(2026, 10, 1), Until: day(2026, 11, 1)}},
		{"year end", "?to=2026-12-31", in.ReportQuery{Page: 1, Limit: 20, Until: day(2027, 1, 1)}},
		{"leap day", "?from=2028-02-29&to=2028-02-29", in.ReportQuery{Page: 1, Limit: 20, From: day(2028, 2, 29), Until: day(2028, 3, 1)}},
		{"year start", "?from=2027-01-01&to=2027-01-01", in.ReportQuery{Page: 1, Limit: 20, From: day(2027, 1, 1), Until: day(2027, 1, 2)}},
	}
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		for _, c := range cases {
			t.Run(path+" "+c.name, func(t *testing.T) {
				f := newReportFixture(t, true)
				if w := f.admin(t, path+c.query); w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				got := f.spy.queries[0]
				if got.Page != c.want.Page || got.Limit != c.want.Limit || got.CreatedBy != "" || !sameInstant(got.From, c.want.From) || !sameInstant(got.Until, c.want.Until) {
					t.Fatalf("got=%+v want=%+v", got, c.want)
				}
			})
		}
	}
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b) && a.Location() == time.UTC
}

func keysOf(m map[string]any) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestReportResponseSchemas(t *testing.T) {
	f := newReportFixture(t, true)
	cases := []struct {
		path   string
		fields []string
		values map[string]any
	}{
		{dailyPath, []string{"dailyTotalCents", "day", "saleCount"}, map[string]any{"day": "2026-10-12", "dailyTotalCents": float64(250000), "saleCount": float64(2)}},
		{monthlyPath, []string{"month", "monthlyTotalCents", "saleCount"}, map[string]any{"month": "2026-10-01", "saleCount": float64(3)}},
		{topProductPath, []string{"productId", "totalQuantity", "totalRevenueCents"}, map[string]any{"productId": productID, "totalQuantity": float64(6), "totalRevenueCents": float64(600)}},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			w := f.admin(t, c.path)
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !reflect.DeepEqual(keysOf(body), []string{"data", "meta"}) {
				t.Fatalf("envelope=%v err=%v", body, err)
			}
			if !reflect.DeepEqual(keysOf(body["meta"].(map[string]any)), []string{"limit", "page", "total", "totalPages"}) {
				t.Fatalf("meta=%v", body["meta"])
			}
			items := body["data"].([]any)
			if len(items) != 1 || !reflect.DeepEqual(keysOf(items[0].(map[string]any)), c.fields) {
				t.Fatalf("items=%v", items)
			}
			for k, v := range c.values {
				if items[0].(map[string]any)[k] != v {
					t.Fatalf("%s=%v want=%v", k, items[0].(map[string]any)[k], v)
				}
			}
		})
	}
	// int64 amounts must be written exactly, never through float64.
	if w := f.admin(t, monthlyPath); !strings.Contains(w.Body.String(), `"monthlyTotalCents":9223372036854775807`) {
		t.Fatalf("amount lost precision: %s", w.Body.String())
	}
	if w := f.admin(t, dailyPath); !strings.Contains(w.Body.String(), `"meta":{"page":1,"limit":20,"total":1,"totalPages":1}`) {
		t.Fatalf("meta=%s", w.Body.String())
	}
}

func TestEmptyReportsReturnEmptyArrays(t *testing.T) {
	f := newReportFixture(t, true)
	f.spy.daily, f.spy.monthly, f.spy.top = in.ReportPage[model.DailyTotal]{Page: 1, Limit: 20}, in.ReportPage[model.MonthlyTotal]{Page: 1, Limit: 20}, in.ReportPage[model.ProductSales]{Page: 1, Limit: 20}
	for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
		w := f.admin(t, path)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) || !strings.Contains(w.Body.String(), `"total":0,"totalPages":0`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestReportCorrelationIsGeneratedWhenAbsent(t *testing.T) {
	f := newReportFixture(t, true)
	r := httptest.NewRequest("GET", dailyPath+"?limit=0", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, ownerID, "ADMIN", "reports:read")))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Header().Get("X-Correlation-Id") == "" {
		t.Fatal("correlation not generated")
	}
	assertHTTPError(t, w, 400, "VALIDATION_ERROR")
}

func TestReportFailuresUseSharedErrorEnvelope(t *testing.T) {
	cause := fmt.Errorf("%w: secret-database password mongodb://user:pw@host", in.ErrPersistenceFailure)
	for name, err := range map[string]error{"persistence": cause, "unexpected": errors.New("secret-database panic detail"), "invalid query": in.ErrInvalidQuery} {
		f := newReportFixture(t, true)
		f.spy.err = err
		for _, path := range []string{dailyPath, monthlyPath, topProductPath} {
			w := f.admin(t, path)
			if name == "invalid query" {
				assertHTTPError(t, w, 400, "VALIDATION_ERROR")
			} else {
				assertHTTPError(t, w, 500, "INTERNAL_ERROR")
			}
			if strings.Contains(w.Body.String(), "mongodb://") || strings.Contains(w.Body.String(), "password") {
				t.Fatalf("%s leaked internals: %s", name, w.Body.String())
			}
		}
	}
}

func TestReportDependencyMissingAndTimeout(t *testing.T) {
	missing := newReportFixture(t, false)
	assertHTTPError(t, missing.admin(t, dailyPath), 500, "INTERNAL_ERROR")
	slow := newReportFixture(t, true)
	slow.spy.block = true
	started := time.Now()
	assertHTTPError(t, slow.admin(t, topProductPath), 500, "INTERNAL_ERROR")
	if time.Since(started) > 5*time.Second {
		t.Fatal("report ignored the request timeout")
	}
}
