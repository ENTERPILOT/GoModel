package app

import (
	"context"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// ApplyOutboundTLS installs the http.tls settings as the process-wide trust
// for every outbound HTTPS connection. New calls it with resolved settings
// before any provider builds a transport. Installing unchanged settings is a
// no-op.
func ApplyOutboundTLS(cfg config.HTTPTLSConfig) error {
	return httpclient.SetConfiguredTLS(httpclient.TLSSettings{
		CAFile:             cfg.CAFile,
		ClientCertFile:     cfg.ClientCertFile,
		ClientKeyFile:      cfg.ClientKeyFile,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
}

// ApplyOutboundTLSEarly installs the http.tls settings before extensions set
// up and secret references resolve, so a secret store behind a private CA is
// reachable on first start. The paths may themselves be references: those
// using the local built-in schemes (${env:...}, ${file:...}) are resolved on
// a copy, leaving the configuration for ResolveSecrets. When a path needs any
// other scheme, or a reference cannot be resolved yet, nothing is installed:
// New applies the resolved settings, and ResolveSecrets reports the error.
func ApplyOutboundTLSEarly(ctx context.Context, cfg config.HTTPTLSConfig) error {
	builtins := config.NewSecrets()
	for _, path := range []*string{&cfg.CAFile, &cfg.ClientCertFile, &cfg.ClientKeyFile} {
		resolved, err := builtins.Resolve(ctx, *path)
		if err != nil {
			return nil
		}
		*path = resolved
	}
	return ApplyOutboundTLS(cfg)
}
