package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPPort         string
	MongoURI         string
	JWTKey           string
	JWTKeyFile       string
	MongoTimeout     time.Duration
	MongoMaxPoolSize uint64
}

func Load() Config {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	return Config{HTTPPort: port, MongoURI: os.Getenv("MONGODB_URI"), JWTKey: os.Getenv("JWT_PUBLIC_KEY"), JWTKeyFile: os.Getenv("JWT_PUBLIC_KEY_FILE"), MongoTimeout: 5 * time.Second, MongoMaxPoolSize: 10}
}

// LoadRuntime validates configuration without logging credentials or URIs.
func LoadRuntime() (Config, error) {
	cfg := Load()
	port, err := strconv.Atoi(cfg.HTTPPort)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, errors.New("invalid HTTP_PORT")
	}
	uri, err := url.Parse(cfg.MongoURI)
	if err != nil || (uri.Scheme != "mongodb" && uri.Scheme != "mongodb+srv") || uri.Host == "" || uri.User == nil || uri.User.Username() != "sales_app" || uri.Path != "/sales" {
		return Config{}, errors.New("MONGODB_URI must target sales authenticated as sales_app")
	}
	if (cfg.JWTKey == "") == (cfg.JWTKeyFile == "") {
		return Config{}, errors.New("configure exactly one of JWT_PUBLIC_KEY or JWT_PUBLIC_KEY_FILE")
	}
	if value := os.Getenv("MONGODB_TIMEOUT"); value != "" {
		cfg.MongoTimeout, err = time.ParseDuration(value)
		if err != nil || cfg.MongoTimeout <= 0 || cfg.MongoTimeout > 10*time.Second {
			return Config{}, errors.New("MONGODB_TIMEOUT must be positive and at most 10s")
		}
	}
	if value := os.Getenv("MONGODB_MAX_POOL_SIZE"); value != "" {
		cfg.MongoMaxPoolSize, err = strconv.ParseUint(value, 10, 64)
		if err != nil || cfg.MongoMaxPoolSize == 0 {
			return Config{}, errors.New("MONGODB_MAX_POOL_SIZE must be positive")
		}
	}
	return cfg, nil
}
func (cfg Config) PublicKey() (string, error) {
	if cfg.JWTKey != "" {
		return cfg.JWTKey, nil
	}
	data, err := os.ReadFile(cfg.JWTKeyFile)
	if err != nil {
		return "", errors.New("cannot read JWT_PUBLIC_KEY_FILE")
	}
	return string(data), nil
}
