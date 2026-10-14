package config

import (
	"os"
	"testing"
)

func TestLoad_DefaultHTTPPort(t *testing.T) {
	for _, state := range []string{"unset", "empty"} {
		t.Run(state, func(t *testing.T) {
			// Setenv restores the caller's environment during cleanup, including
			// when the variable is explicitly removed for the unset case.
			t.Setenv("HTTP_PORT", "")
			if state == "unset" {
				if err := os.Unsetenv("HTTP_PORT"); err != nil {
					t.Fatal(err)
				}
			}

			if got := Load().HTTPPort; got != "8080" {
				t.Errorf("HTTPPort = %q, want default 8080", got)
			}
		})
	}
}

func TestLoad_CustomHTTPPort(t *testing.T) {
	t.Setenv("HTTP_PORT", "9090")

	if got := Load().HTTPPort; got != "9090" {
		t.Errorf("HTTPPort = %q, want configured 9090", got)
	}
}
