package wrapper

import (
	"testing"

	"amdl/internal/config"
)

func TestWrapperTransportCredentials(t *testing.T) {
	tests := []struct {
		name             string
		insecure         bool
		securityProtocol string
	}{
		{name: "plaintext", insecure: true, securityProtocol: "insecure"},
		{name: "TLS", insecure: false, securityProtocol: "tls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds := wrapperTransportCredentials(config.WrapperConfig{Insecure: tt.insecure})
			if got := creds.Info().SecurityProtocol; got != tt.securityProtocol {
				t.Fatalf("security protocol = %q, want %q", got, tt.securityProtocol)
			}
		})
	}
}
