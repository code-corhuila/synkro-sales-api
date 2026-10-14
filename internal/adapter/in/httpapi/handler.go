package httpapi

import (
	"context"
	"errors"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type Dependencies struct {
	Register in.RegisterSaleUseCase
	Queries  in.SalesQueryUseCase
	Reports  in.SalesReportUseCase
	Verifier *JWTVerifier
	Health   func(context.Context) error
	Clock    out.Clock
	Logger   *slog.Logger
	Timeout  time.Duration
}
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

type correlationKey struct{}
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}

func NewRouter(d Dependencies) http.Handler {
	if d.Clock == nil {
		d.Clock = wallClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	if d.Timeout <= 0 {
		d.Timeout = 5 * time.Second
	}
	api := requireAuth(d.Verifier, http.HandlerFunc(d.serveSales))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace := r.Header.Get("X-Correlation-Id")
		if trace == "" {
			trace = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", trace)
		r = r.WithContext(context.WithValue(r.Context(), correlationKey{}, trace))
		sw := &statusWriter{ResponseWriter: w}
		start := d.Clock.Now()
		defer func() {
			if recover() != nil {
				if sw.status == 0 {
					writeError(sw, 500, "INTERNAL_ERROR", "Unexpected error", nil)
				}
				d.Logger.ErrorContext(r.Context(), "request failed", "service", "synkro-sales-api", "correlationId", trace)
			}
			d.Logger.InfoContext(r.Context(), "HTTP request completed", "service", "synkro-sales-api", "correlationId", trace, "method", r.Method, "status", sw.status, "durationMs", d.Clock.Now().Sub(start).Milliseconds())
		}()
		if r.URL.Path == "/health" {
			if r.Method != "GET" {
				sw.Header().Set("Allow", "GET")
				validationError(sw, "method", "unsupported method")
				return
			}
			d.health(sw, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			api.ServeHTTP(sw, r)
			return
		}
		writeError(sw, 404, "NOT_FOUND", "Resource not found", nil)
	})
}
func (d Dependencies) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), d.Timeout)
	defer cancel()
	status, state, database := 200, "ok", "ok"
	if d.Health == nil || d.Health(ctx) != nil {
		status, state, database = 503, "degraded", "down"
	}
	writeJSON(w, status, struct {
		Status       string            `json:"status"`
		Service      string            `json:"service"`
		Timestamp    string            `json:"timestamp"`
		Dependencies map[string]string `json:"dependencies"`
	}{state, "synkro-sales-api", d.Clock.Now().UTC().Format(time.RFC3339Nano), map[string]string{"database": database}})
}
func (d Dependencies) serveSales(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/v1/sales" {
		switch r.Method {
		case "POST":
			d.register(w, r)
		case "GET":
			d.list(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			validationError(w, "method", "unsupported method")
		}
		return
	}
	// Reports are matched explicitly, before any generic sale-identifier match.
	if path == reportsPath || strings.HasPrefix(path, reportsPath+"/") {
		d.serveReport(w, r)
		return
	}
	if strings.HasPrefix(path, "/api/v1/sales/") && !strings.Contains(strings.TrimPrefix(path, "/api/v1/sales/"), "/") && strings.TrimPrefix(path, "/api/v1/sales/") != "" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			validationError(w, "method", "unsupported method")
			return
		}
		d.detail(w, r, strings.TrimPrefix(path, "/api/v1/sales/"))
		return
	}
	writeError(w, 404, "NOT_FOUND", "Resource not found", nil)
}
func authorize(w http.ResponseWriter, allowed bool) bool {
	if !allowed {
		writeError(w, 403, "FORBIDDEN", "Caller is not authorized for this operation", nil)
	}
	return allowed
}
func (d Dependencies) failure(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := statusFor(err)
	if status == 500 {
		d.Logger.ErrorContext(r.Context(), "sales operation failed", "service", "synkro-sales-api", "correlationId", w.Header().Get("X-Correlation-Id"), "failureType", "persistence_or_dependency")
	}
	writeError(w, status, code, message, details)
}
func (d Dependencies) register(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, principalFrom(r.Context()).CanRegister()) {
		return
	}
	cmd, ok := registrationCommand(w, r)
	if !ok {
		return
	}
	if d.Register == nil {
		d.failure(w, r, errors.New("registration dependency missing"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), d.Timeout)
	defer cancel()
	result, err := d.Register.Register(ctx, cmd)
	if err != nil {
		d.failure(w, r, err)
		return
	}
	if result.Sale == nil || !result.Sale.Active() {
		d.failure(w, r, errors.New("invalid registration result"))
		return
	}
	status := 200
	if result.Created {
		status = 201
		w.Header().Set("Location", "/api/v1/sales/"+result.Sale.ID())
	}
	writeJSON(w, status, saleResponse(result.Sale))
}
func (d Dependencies) list(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !authorize(w, p.CanRead()) {
		return
	}
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	if p.Role == "SALESPERSON" {
		q.CreatedBy = p.Subject
	}
	if d.Queries == nil {
		d.failure(w, r, errors.New("query dependency missing"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), d.Timeout)
	defer cancel()
	result, err := d.Queries.List(ctx, q)
	if err != nil {
		d.failure(w, r, err)
		return
	}
	response := ListSalesResponse{Data: []SaleResponse{}, Meta: PageMeta{Page: result.Page, Limit: result.Limit, Total: result.Total, TotalPages: result.TotalPages}}
	for _, s := range result.Sales {
		response.Data = append(response.Data, saleResponse(s))
	}
	writeJSON(w, 200, response)
}
func (d Dependencies) detail(w http.ResponseWriter, r *http.Request, id string) {
	p := principalFrom(r.Context())
	if !authorize(w, p.CanRead()) {
		return
	}
	if !canonicalID(id) {
		validationError(w, "id", "must be a canonical UUID")
		return
	}
	if r.URL.RawQuery != "" {
		validationError(w, "query", "detail accepts no query parameters")
		return
	}
	if d.Queries == nil {
		d.failure(w, r, errors.New("query dependency missing"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), d.Timeout)
	defer cancel()
	sale, err := d.Queries.Get(ctx, id)
	if err != nil {
		d.failure(w, r, err)
		return
	}
	if p.Role == "SALESPERSON" && sale.CreatedBy() != p.Subject {
		authorize(w, false)
		return
	}
	writeJSON(w, 200, saleResponse(sale))
}

// statusFor is the only mapping from application/domain failures to HTTP.
func statusFor(err error) (int, string, string, []ErrorDetail) {
	switch {
	case errors.Is(err, in.ErrPersistenceFailure):
		return 500, "INTERNAL_ERROR", "Unexpected error", nil
	case errors.Is(err, in.ErrSaleNotFound):
		return 404, "NOT_FOUND", "Resource not found", nil
	case errors.Is(err, in.ErrInvalidQuery), errors.Is(err, in.ErrInvalidIdempotencyKey):
		return 400, "VALIDATION_ERROR", "Invalid request", []ErrorDetail{{Field: "request", Message: "invalid parameters"}}
	case errors.Is(err, model.ErrAmountOverflow):
		return 422, "BUSINESS_RULE_VIOLATION", "Computed sale amounts exceed the supported range", []ErrorDetail{{Field: "details", Message: "calculated amounts exceed int64 maximum"}}
	case errors.Is(err, model.ErrInvalidQuantity), errors.Is(err, model.ErrInvalidUnitPrice), errors.Is(err, model.ErrQuantityTooLarge), errors.Is(err, model.ErrNoLines), errors.Is(err, model.ErrTooManyLines):
		return 422, "BUSINESS_RULE_VIOLATION", "Sale business invariant failed", []ErrorDetail{{Field: "details", Message: "sale lines violate a business invariant"}}
	default:
		return 500, "INTERNAL_ERROR", "Unexpected error", nil
	}
}
