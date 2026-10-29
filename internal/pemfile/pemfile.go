// Package pemfile holds the checks shared by the inbound (internal/servertls)
// and outbound (internal/httpclient) TLS loaders for the PEM files named in
// configuration.
package pemfile

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// CheckPath rejects PEM contents given where a file path belongs, as with
// SERVER_TLS_KEY_FILE="$(cat tls.key)" or a ${file:...} reference, which
// resolves to the file's contents. The error names only the field: an
// *fs.PathError would echo the whole value, writing a private key to the logs.
func CheckPath(field, path string) error {
	if strings.Contains(path, "-----BEGIN") {
		return fmt.Errorf("%s expects a file path, not PEM contents", field)
	}
	return nil
}

// AppendCertificates adds every certificate in data to pool. Unlike
// AppendCertsFromPEM, it rejects a block that does not parse, so a bundle
// cannot silently drop one of its CAs. pem.Decode itself skips a block with
// bad framing or base64, so the decoded blocks are counted against the BEGIN
// markers. Like pem.Decode, only a marker that starts a line counts, so a
// comment that quotes one does not fail the bundle. Blocks of other types are
// skipped.
func AppendCertificates(pool *x509.CertPool, data []byte) error {
	markers := bytes.Count(append([]byte("\n"), data...), []byte("\n-----BEGIN "))
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
			return fmt.Errorf("certificate %d: %w", count+1, err)
		}
		pool.AddCert(cert)
		count++
	}
	if blocks != markers {
		return fmt.Errorf("%d of %d PEM blocks are malformed", markers-blocks, markers)
	}
	if count == 0 {
		return errors.New("contains no PEM certificates")
	}
	return nil
}
