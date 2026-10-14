package httpapi

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/google/uuid"
)

var errCredential = errors.New("invalid authentication credential")

type Principal struct {
	Subject, Role string
	Permissions   []string
}

func (p Principal) has(permission string) bool {
	for _, v := range p.Permissions {
		if v == permission {
			return true
		}
	}
	return false
}
func (p Principal) CanRegister() bool {
	return p.Role == "SERVICE" && p.Subject == "synkro-workflow" && p.has("sales:register")
}
func (p Principal) CanRead() bool {
	return (p.Role == "ADMIN" || p.Role == "SALESPERSON") && p.has("sales:read")
}

// CanReadReports applies authentication.md: reports need reports:read, held
// by ADMIN and by SALESPERSON (own sales only). INVENTORY and service tokens
// never read reports, even if their token claims the permission.
func (p Principal) CanReadReports() bool {
	return (p.Role == "ADMIN" || p.Role == "SALESPERSON") && p.has("reports:read")
}

type principalKey struct{}

func principalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

type JWTVerifier struct {
	key   *rsa.PublicKey
	clock out.Clock
}

// NewJWTVerifier accepts only a configured RSA public key, never a private key.
func NewJWTVerifier(value string, clock out.Clock) (*JWTVerifier, error) {
	block, rest := pem.Decode([]byte(strings.ReplaceAll(value, `\n`, "\n")))
	if block == nil || strings.TrimSpace(string(rest)) != "" || clock == nil {
		return nil, errors.New("invalid JWT public key configuration")
	}
	var key *rsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err == nil {
			key, _ = parsed.(*rsa.PublicKey)
		}
	case "RSA PUBLIC KEY":
		key, _ = x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if key == nil || key.N.BitLen() < 2048 {
		return nil, errors.New("invalid JWT RSA public key")
	}
	return &JWTVerifier{key: key, clock: clock}, nil
}
func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func (v *JWTVerifier) Verify(token string) (Principal, error) {
	if v == nil || len(token) > 16384 {
		return Principal{}, errCredential
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, errCredential
	}
	decoded := make([][]byte, 3)
	for i, p := range parts {
		b, err := base64.RawURLEncoding.Strict().DecodeString(p)
		if err != nil || len(b) == 0 {
			return Principal{}, errCredential
		}
		decoded[i] = b
	}
	var header struct {
		Algorithm string   `json:"alg"`
		Critical  []string `json:"crit"`
	}
	if uniqueJSON(decoded[0]) != nil || uniqueJSON(decoded[1]) != nil {
		return Principal{}, errCredential
	}
	if json.Unmarshal(decoded[0], &header) != nil || header.Algorithm != "RS256" || len(header.Critical) > 0 {
		return Principal{}, errCredential
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(v.key, crypto.SHA256, hash[:], decoded[2]) != nil {
		return Principal{}, errCredential
	}
	var claims struct {
		Subject     string   `json:"sub"`
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
		Issued      *int64   `json:"iat"`
		Expires     *int64   `json:"exp"`
		ID          string   `json:"jti"`
	}
	if json.Unmarshal(decoded[1], &claims) != nil || len(claims.Roles) != 1 || claims.Permissions == nil || claims.Issued == nil || claims.Expires == nil || strings.TrimSpace(claims.ID) == "" {
		return Principal{}, errCredential
	}
	now := v.clock.Now()
	if *claims.Expires <= now.Add(-30*time.Second).Unix() || *claims.Issued > now.Add(30*time.Second).Unix() || *claims.Issued >= *claims.Expires {
		return Principal{}, errCredential
	}
	role := claims.Roles[0]
	switch role {
	case "ADMIN", "SALESPERSON", "INVENTORY":
		if !canonicalID(claims.Subject) {
			return Principal{}, errCredential
		}
	case "SERVICE":
		if claims.Subject != "synkro-workflow" && claims.Subject != "synkro-worker" {
			return Principal{}, errCredential
		}
	default:
		return Principal{}, errCredential
	}
	for _, p := range claims.Permissions {
		if strings.TrimSpace(p) == "" {
			return Principal{}, errCredential
		}
	}
	return Principal{Subject: claims.Subject, Role: role, Permissions: claims.Permissions}, nil
}
