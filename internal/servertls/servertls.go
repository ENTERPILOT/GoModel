// Package servertls serves the gateway's own listener over TLS from PEM files
// that are re-read when they change on disk, so a rotated certificate takes
// effect without a restart. It covers inbound connections only; outbound
// trust lives in internal/httpclient.
package servertls

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enterpilot/gomodel/internal/pemfile"
)

// DefaultCheckInterval is how often, at most, the files are checked for a
// change. A rotation is picked up by the first handshake after it.
const DefaultCheckInterval = 5 * time.Second

// Settings names the PEM files the listener serves from. The zero value
// means plain HTTP.
type Settings struct {
	// CertFile and KeyFile hold the serving certificate (with any
	// intermediates after it) and its private key. Both or neither.
	CertFile string
	KeyFile  string
	// ClientCAFile, when set, verifies every client certificate presented
	// against the CAs in this bundle (mTLS). A handshake without one still
	// completes, so the health probes can get through; the HTTP layer rejects
	// every other request that lacks a verified certificate.
	ClientCAFile string
}

// Source hands each TLS handshake the configuration built from the files'
// current contents.
//
// The files are polled rather than watched. A handshake that finds the last
// check older than the interval stats the files, and only a changed stat
// (another inode, size, or modification time) re-reads them. Polling needs no
// goroutine to stop when a reload replaces the server, follows the symlink
// swap Kubernetes uses to update a mounted Secret (which file watchers
// commonly miss once the watched link target is deleted), and costs every
// other handshake one atomic load. A rotation therefore lands within the
// interval of the next handshake, which is far inside any certificate's
// renewal window.
//
// A failed reload, such as a pair caught half written, keeps the last good
// configuration and logs a warning; it never stops the listener.
type Source struct {
	settings Settings
	interval time.Duration

	current   atomic.Pointer[tls.Config]
	nextCheck atomic.Int64 // unix nanoseconds

	mu          sync.Mutex // serializes reloads; guards the fields below
	stamp       []os.FileInfo
	lastFailure string
}

// New loads settings for the first time. Unlike a later reload, a missing or
// unusable file is an error, so a typo fails startup instead of serving
// nothing.
func New(settings Settings) (*Source, error) {
	settings.CertFile = strings.TrimSpace(settings.CertFile)
	settings.KeyFile = strings.TrimSpace(settings.KeyFile)
	settings.ClientCAFile = strings.TrimSpace(settings.ClientCAFile)
	if settings.CertFile == "" || settings.KeyFile == "" {
		return nil, errors.New("server TLS: both a certificate and a key file are required")
	}
	if err := errors.Join(
		pemfile.CheckPath("server.tls_cert_file", settings.CertFile),
		pemfile.CheckPath("server.tls_key_file", settings.KeyFile),
		pemfile.CheckPath("server.tls_client_ca_file", settings.ClientCAFile),
	); err != nil {
		return nil, err
	}
	s := &Source{settings: settings, interval: DefaultCheckInterval}
	stamp, err := statFiles(settings)
	if err != nil {
		return nil, err
	}
	cfg, err := load(settings)
	if err != nil {
		return nil, err
	}
	s.stamp = stamp
	s.current.Store(cfg)
	s.nextCheck.Store(time.Now().Add(s.interval).UnixNano())
	return s, nil
}

// ServerConfig returns the configuration to wrap the listener with. It
// carries no certificate of its own: every handshake asks the Source.
func (s *Source) ServerConfig() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		GetConfigForClient: s.configForClient,
	}
}

// MutualTLS reports whether requests must carry a verified client
// certificate.
func (s *Source) MutualTLS() bool { return s.settings.ClientCAFile != "" }

// Leaf returns the certificate currently served.
func (s *Source) Leaf() *x509.Certificate {
	return s.current.Load().Certificates[0].Leaf
}

func (s *Source) configForClient(*tls.ClientHelloInfo) (*tls.Config, error) {
	now := time.Now().UnixNano()
	next := s.nextCheck.Load()
	// One handshake per interval claims the check; the others never wait.
	if now >= next && s.nextCheck.CompareAndSwap(next, now+int64(s.interval)) {
		s.refresh()
	}
	return s.current.Load(), nil
}

// refresh reloads the files when their stat changed since the last attempt.
func (s *Source) refresh() {
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()

	stamp, err := statFiles(s.settings)
	if err != nil {
		s.warn(err)
		return
	}
	if sameStamp(s.stamp, stamp) {
		return
	}
	// The stamp is recorded only once a load succeeds, so a failed one is
	// retried every interval: a fix that leaves the stat unchanged, such as
	// restored permissions, is still picked up. warn logs each failure once.
	cfg, err := load(s.settings)
	if err != nil {
		s.warn(err)
		return
	}
	s.stamp = stamp
	s.lastFailure = ""
	s.current.Store(cfg)
	leaf := cfg.Certificates[0].Leaf
	slog.Info("server TLS certificate reloaded",
		"subject", leaf.Subject.String(), "not_after", leaf.NotAfter)
}

// warn logs a failed reload once per distinct failure.
func (s *Source) warn(err error) {
	if msg := err.Error(); msg != s.lastFailure {
		s.lastFailure = msg
		slog.Warn("server TLS reload failed; still serving the previous certificate", "error", err)
	}
}

func (s Settings) files() []string {
	files := []string{s.CertFile, s.KeyFile}
	if s.ClientCAFile != "" {
		files = append(files, s.ClientCAFile)
	}
	return files
}

func statFiles(settings Settings) ([]os.FileInfo, error) {
	files := settings.files()
	stamp := make([]os.FileInfo, len(files))
	for i, name := range files {
		info, err := os.Stat(name) // follows symlinks to the file served
		if err != nil {
			return nil, fmt.Errorf("server TLS: %w", err)
		}
		// Reading a FIFO or device could block the handshake that holds the
		// reload, or startup, forever.
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("server TLS: %s is not a regular file", name)
		}
		stamp[i] = info
	}
	return stamp, nil
}

func sameStamp(a, b []os.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !os.SameFile(a[i], b[i]) || a[i].Size() != b[i].Size() || !a[i].ModTime().Equal(b[i].ModTime()) {
			return false
		}
	}
	return true
}

// load builds the per-handshake configuration from the files' contents.
func load(settings Settings) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(settings.CertFile, settings.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("server TLS: load certificate: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		// Advertised explicitly: a configuration returned by
		// GetConfigForClient replaces the listener's for ALPN too.
		NextProtos: []string{"h2", "http/1.1"},
	}
	if settings.ClientCAFile != "" {
		caPEM, err := os.ReadFile(settings.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("server TLS: read client CA: %w", err)
		}
		pool, err := clientCAPool(caPEM)
		if err != nil {
			return nil, fmt.Errorf("server TLS: client CA file %s: %w", settings.ClientCAFile, err)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

// clientCAPool holds only the configured CAs: the system store must not vouch
// for clients. Unlike AppendCertsFromPEM, it rejects a block that does not
// parse, so a bundle cannot silently drop one of its CAs. pem.Decode itself
// skips a block with bad framing or base64, so the decoded blocks are counted
// against the BEGIN markers. Like pem.Decode, only a marker that starts a
// line counts, so a comment that quotes one does not fail the bundle.
func clientCAPool(data []byte) (*x509.CertPool, error) {
	markers := bytes.Count(append([]byte("\n"), data...), []byte("\n-----BEGIN "))
	pool := x509.NewCertPool()
	count, blocks := 0, 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		blocks++
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate %d: %w", count+1, err)
		}
		pool.AddCert(cert)
		count++
	}
	if blocks != markers {
		return nil, fmt.Errorf("%d of %d PEM blocks are malformed", markers-blocks, markers)
	}
	if count == 0 {
		return nil, errors.New("contains no PEM certificates")
	}
	return pool, nil
}
