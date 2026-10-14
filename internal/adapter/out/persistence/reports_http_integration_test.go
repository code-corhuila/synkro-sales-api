package persistence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type reportRequester func(handler http.Handler, method, path, body, sub, role, permission string) *httptest.ResponseRecorder

// runReportHTTPIntegration drives the three report endpoints through the real
// router, use cases and MongoDB adapter, asserting exact bodies rather than
// only status codes. Each scenario owns a distinct UTC year and owners.
func runReportHTTPIntegration(t *testing.T, collection *mongo.Collection, router http.Handler, request reportRequester, unavailable func() http.Handler) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := func(owner string, date time.Time, active bool, product string, quantity int, price int64) {
		t.Helper()
		sale, err := model.RegisterSale(uuid.NewString(), uuid.NewString(), owner, date, []model.LineInput{{DetailID: uuid.NewString(), ProductID: product, Quantity: quantity, UnitPriceCents: price}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := encodeSale(uuid.NewString()+":report-http", sale)
		if err != nil {
			t.Fatal(err)
		}
		var doc bson.M
		if err := bson.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc["active"] = active
		if _, err := collection.InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path, sub, role, permission string) *httptest.ResponseRecorder {
		return request(router, "GET", path, "", sub, role, permission)
	}
	exact := func(t *testing.T, w *httptest.ResponseRecorder, want string) {
		t.Helper()
		if w.Code != 200 || w.Body.String() != want+"\n" {
			t.Fatalf("status=%d\n got=%s\nwant=%s", w.Code, w.Body.String(), want)
		}
	}
	meta := func(page, limit, total, pages int) string {
		return `"meta":{"page":` + strconv.Itoa(page) + `,"limit":` + strconv.Itoa(limit) + `,"total":` + strconv.Itoa(total) + `,"totalPages":` + strconv.Itoa(pages) + `}`
	}
	p1, p2, p3 := reportProduct(11), reportProduct(12), reportProduct(13)
	seller, rival := uuid.NewString(), uuid.NewString()
	admin := uuid.NewString()
	const window = "?from=2042-01-01&to=2042-12-31"
	store(seller, utc(2042, 1, 31, 23, 0, 0, 0), true, p1, 2, 500)
	store(seller, utc(2042, 2, 1, 0, 0, 0, 0), true, p1, 1, 500)
	store(rival, utc(2042, 2, 1, 10, 0, 0, 0), true, p2, 4, 100)
	store(rival, utc(2042, 2, 1, 11, 0, 0, 0), false, p3, 9, 100)

	t.Run("ReportsAdminAndSalesperson", func(t *testing.T) {
		exact(t, get("/api/v1/sales/reports/daily"+window, admin, "ADMIN", "reports:read"),
			`{"data":[{"day":"2042-02-01","dailyTotalCents":900,"saleCount":2},{"day":"2042-01-31","dailyTotalCents":1000,"saleCount":1}],`+meta(1, 20, 2, 1)+`}`)
		exact(t, get("/api/v1/sales/reports/monthly"+window, admin, "ADMIN", "reports:read"),
			`{"data":[{"month":"2042-02-01","monthlyTotalCents":900,"saleCount":2},{"month":"2042-01-01","monthlyTotalCents":1000,"saleCount":1}],`+meta(1, 20, 2, 1)+`}`)
		exact(t, get("/api/v1/sales/reports/top-products"+window, admin, "ADMIN", "reports:read"),
			`{"data":[{"productId":"`+p2+`","totalQuantity":4,"totalRevenueCents":400},{"productId":"`+p1+`","totalQuantity":3,"totalRevenueCents":1500}],`+meta(1, 20, 2, 1)+`}`)
		exact(t, get("/api/v1/sales/reports/daily"+window, seller, "SALESPERSON", "reports:read"),
			`{"data":[{"day":"2042-02-01","dailyTotalCents":500,"saleCount":1},{"day":"2042-01-31","dailyTotalCents":1000,"saleCount":1}],`+meta(1, 20, 2, 1)+`}`)
		exact(t, get("/api/v1/sales/reports/monthly"+window, rival, "SALESPERSON", "reports:read"),
			`{"data":[{"month":"2042-02-01","monthlyTotalCents":400,"saleCount":1}],`+meta(1, 20, 1, 1)+`}`)
		exact(t, get("/api/v1/sales/reports/top-products"+window, seller, "SALESPERSON", "reports:read"),
			`{"data":[{"productId":"`+p1+`","totalQuantity":3,"totalRevenueCents":1500}],`+meta(1, 20, 1, 1)+`}`)
		exact(t, get("/api/v1/sales/reports/top-products"+window, uuid.NewString(), "SALESPERSON", "reports:read"),
			`{"data":[],`+meta(1, 20, 0, 0)+`}`)
		// A single inclusive day keeps the group but drops the other days.
		exact(t, get("/api/v1/sales/reports/daily?from=2042-01-31&to=2042-01-31", admin, "ADMIN", "reports:read"),
			`{"data":[{"day":"2042-01-31","dailyTotalCents":1000,"saleCount":1}],`+meta(1, 20, 1, 1)+`}`)
	})
	t.Run("ReportsAccessControl", func(t *testing.T) {
		for _, path := range []string{"/api/v1/sales/reports/daily", "/api/v1/sales/reports/monthly", "/api/v1/sales/reports/top-products"} {
			for _, c := range [][3]string{{"INVENTORY", "reports:read", admin}, {"SERVICE", "reports:read", "synkro-workflow"}, {"ADMIN", "sales:read", admin}, {"SALESPERSON", "sales:read", seller}} {
				w := get(path+window, c[2], c[0], c[1])
				if w.Code != 403 || !strings.Contains(w.Body.String(), `"error":"FORBIDDEN"`) || !strings.Contains(w.Body.String(), `"traceId":"mongo-http-trace"`) {
					t.Fatal(path, c, w.Code, w.Body.String())
				}
			}
			unauthenticated := httptest.NewRecorder()
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("X-Correlation-Id", "mongo-http-trace")
			router.ServeHTTP(unauthenticated, r)
			if unauthenticated.Code != 401 || !strings.Contains(unauthenticated.Body.String(), `"error":"UNAUTHORIZED"`) {
				t.Fatal(path, unauthenticated.Code, unauthenticated.Body.String())
			}
		}
	})
	t.Run("ReportsPaginationAndMeta", func(t *testing.T) {
		owner := uuid.NewString()
		for day := 1; day <= 3; day++ {
			store(owner, utc(2043, 3, day, 9, 0, 0, 0), true, p1, day, 10)
		}
		path := "/api/v1/sales/reports/daily?from=2043-01-01&to=2043-12-31&limit=2&page="
		exact(t, get(path+"1", owner, "SALESPERSON", "reports:read"),
			`{"data":[{"day":"2043-03-03","dailyTotalCents":30,"saleCount":1},{"day":"2043-03-02","dailyTotalCents":20,"saleCount":1}],`+meta(1, 2, 3, 2)+`}`)
		exact(t, get(path+"2", owner, "SALESPERSON", "reports:read"),
			`{"data":[{"day":"2043-03-01","dailyTotalCents":10,"saleCount":1}],`+meta(2, 2, 3, 2)+`}`)
		exact(t, get(path+"3", owner, "SALESPERSON", "reports:read"),
			`{"data":[],`+meta(3, 2, 3, 2)+`}`)
	})
	t.Run("ReportsFiltersAndRouting", func(t *testing.T) {
		for _, query := range []string{"?from=2042-02-30", "?foo=1", "?from=2042-02-02&to=2042-02-01", "?limit=101", "?page=0", "?from=", "?from=2042-01-01&from=2042-01-02", "?createdBy=" + rival} {
			w := get("/api/v1/sales/reports/daily"+query, admin, "ADMIN", "reports:read")
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"error":"VALIDATION_ERROR"`) {
				t.Fatal(query, w.Code, w.Body.String())
			}
		}
		for _, path := range []string{"/api/v1/sales/reports", "/api/v1/sales/reports/daily/", "/api/v1/sales/reports/weekly"} {
			if w := get(path, admin, "ADMIN", "reports:read"); w.Code != 404 {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
		if w := get("/api/v1/sales?limit=1", admin, "ADMIN", "sales:read"); w.Code != 200 {
			t.Fatal("sale listing regressed", w.Code, w.Body.String())
		}
	})
	t.Run("ReportsDatabaseFailure", func(t *testing.T) {
		broken := unavailable()
		for _, path := range []string{"/api/v1/sales/reports/daily", "/api/v1/sales/reports/monthly", "/api/v1/sales/reports/top-products"} {
			w := request(broken, "GET", path, "", admin, "ADMIN", "reports:read")
			if w.Code != 500 || !strings.Contains(w.Body.String(), `"error":"INTERNAL_ERROR"`) || strings.Contains(w.Body.String(), "127.0.0.1") {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
	})
}
