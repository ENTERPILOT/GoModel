package server

import (
	"net/http"
	"testing"
	"time"
)

func TestConfigureGatewayHTTPServer_DisablesWriteTimeoutForInference(t *testing.T) {
	server := &http.Server{
		ReadTimeout:  time.Second,
		WriteTimeout: 30 * time.Second,
	}

	if err := configureGatewayHTTPServer(server, 0); err != nil {
		t.Fatalf("configureGatewayHTTPServer() error = %v", err)
	}

	if got := server.ReadTimeout; got != inboundServerReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", got, inboundServerReadTimeout)
	}
	if got := server.ReadHeaderTimeout; got != inboundServerReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got, inboundServerReadHeaderTimeout)
	}
	if got := server.WriteTimeout; got != 0 {
		t.Fatalf("WriteTimeout = %v, want 0", got)
	}
}

func TestNewGatewayStartConfig_AppliesTimeoutOverrides(t *testing.T) {
	cfg := newGatewayStartConfig(":0", 0)
	if cfg.BeforeServeFunc == nil {
		t.Fatal("BeforeServeFunc = nil, want configured server overrides")
	}

	server := &http.Server{}
	if err := cfg.BeforeServeFunc(server); err != nil {
		t.Fatalf("BeforeServeFunc() error = %v", err)
	}

	if got := server.ReadTimeout; got != inboundServerReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", got, inboundServerReadTimeout)
	}
	if got := server.ReadHeaderTimeout; got != inboundServerReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got, inboundServerReadHeaderTimeout)
	}
	if got := server.WriteTimeout; got != 0 {
		t.Fatalf("WriteTimeout = %v, want 0", got)
	}
}
