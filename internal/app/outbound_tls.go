package app

import (
	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// ApplyOutboundTLS installs the http.tls settings as the process-wide trust
// for every outbound HTTPS connection. New calls it before any provider
// builds a transport; the run loop calls it earlier still, before extensions
// set up and secrets resolve, so a secret store behind a private CA is
// reachable on first start. Installing unchanged settings is a no-op.
func ApplyOutboundTLS(cfg config.HTTPTLSConfig) error {
	return httpclient.SetConfiguredTLS(httpclient.TLSSettings{
		CAFile:             cfg.CAFile,
		ClientCertFile:     cfg.ClientCertFile,
		ClientKeyFile:      cfg.ClientKeyFile,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
}
