package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func runtimeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MONGODB_URI", "mongodb://sales_app:placeholder@localhost:27017/sales?authSource=admin")
	t.Setenv("JWT_PUBLIC_KEY", "configured-public-key")
	t.Setenv("JWT_PUBLIC_KEY_FILE", "")
	t.Setenv("MONGODB_TIMEOUT", "")
	t.Setenv("MONGODB_MAX_POOL_SIZE", "")
}
func TestRuntimeConfiguration(t *testing.T) {
	runtimeEnv(t)
	cfg, err := LoadRuntime()
	if err != nil || cfg.MongoTimeout != 5*time.Second || cfg.MongoMaxPoolSize != 10 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	key, err := cfg.PublicKey()
	if err != nil || key != "configured-public-key" {
		t.Fatal("environment key lost")
	}
	for _, tc := range []struct{ name, value string }{{"MONGODB_URI", ""}, {"MONGODB_URI", "mongodb://admin:placeholder@localhost:27017/sales"}, {"MONGODB_URI", "mongodb://localhost:27017/sales"}, {"MONGODB_URI", "mongodb://sales_app:placeholder@localhost:27017/other"}, {"MONGODB_TIMEOUT", "0s"}, {"MONGODB_TIMEOUT", "bad"}, {"MONGODB_MAX_POOL_SIZE", "0"}, {"MONGODB_MAX_POOL_SIZE", "bad"}, {"JWT_PUBLIC_KEY", ""}, {"HTTP_PORT", "bad"}} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			runtimeEnv(t)
			t.Setenv(tc.name, tc.value)
			if _, err := LoadRuntime(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	t.Setenv("MONGODB_TIMEOUT", "2s")
	t.Setenv("MONGODB_MAX_POOL_SIZE", "5")
	cfg, err = LoadRuntime()
	if err != nil || cfg.MongoTimeout != 2*time.Second || cfg.MongoMaxPoolSize != 5 {
		t.Fatal("custom limits lost", err)
	}
	t.Setenv("JWT_PUBLIC_KEY", "")
	path := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(path, []byte("file-key"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_PUBLIC_KEY_FILE", path)
	cfg, err = LoadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	key, err = cfg.PublicKey()
	if err != nil || key != "file-key" {
		t.Fatal("file key lost", err)
	}
	cfg.JWTKeyFile = filepath.Join(t.TempDir(), "missing")
	if _, err := cfg.PublicKey(); err == nil {
		t.Fatal("missing public key file accepted")
	}
	t.Setenv("JWT_PUBLIC_KEY", "another-key")
	if _, err := LoadRuntime(); err == nil {
		t.Fatal("ambiguous keys accepted")
	}
}
