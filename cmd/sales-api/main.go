package main

import (
	"context"
	"errors"
	"github.com/code-corhuila/synkro-sales-api/internal/adapter/in/httpapi"
	"github.com/code-corhuila/synkro-sales-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/synkro-sales-api/internal/application/usecase"
	"github.com/code-corhuila/synkro-sales-api/internal/config"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type uuidGenerator struct{}

func (uuidGenerator) NewID() string { return uuid.NewString() }
func uriPassword(value string) string {
	u, _ := url.Parse(value)
	password, _ := u.User.Password()
	return password
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		switch a.Key {
		case slog.TimeKey:
			a.Key = "timestamp"
			a.Value = slog.StringValue(a.Value.Time().UTC().Format(time.RFC3339Nano))
		case slog.MessageKey:
			a.Key = "message"
		}
		return a
	}}))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "service", "synkro-sales-api", "failureType", "startup_or_server_failure", "reason", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadRuntime()
	if err != nil {
		return err
	}
	key, err := cfg.PublicKey()
	if err != nil {
		return err
	}
	clock := systemClock{}
	verifier, err := httpapi.NewJWTVerifier(key, clock)
	if err != nil {
		return err
	}
	// One client owns the reusable pool; schema and indexes remain migration-owned.
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI).SetAuth(options.Credential{AuthSource: "admin", Username: "sales_app", Password: uriPassword(cfg.MongoURI)}).SetMaxPoolSize(cfg.MongoMaxPoolSize).SetServerSelectionTimeout(cfg.MongoTimeout).SetTimeout(cfg.MongoTimeout).SetRetryWrites(false))
	if err != nil {
		return errors.New("cannot configure MongoDB client")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.MongoTimeout)
		defer cancel()
		_ = client.Disconnect(ctx)
	}()
	repo := persistence.NewMongoRepository(client.Database("sales").Collection("sale"))
	router := httpapi.NewRouter(httpapi.Dependencies{Register: usecase.NewRegisterSale(repo, uuidGenerator{}, clock), Queries: usecase.NewSalesQueries(repo), Reports: usecase.NewSalesReports(repo), Verifier: verifier, Health: func(ctx context.Context) error { return client.Ping(ctx, nil) }, Clock: clock, Logger: logger, Timeout: cfg.MongoTimeout})
	srv := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	logger.Info("starting synkro-sales-api", "service", "synkro-sales-api", "port", cfg.HTTPPort)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
