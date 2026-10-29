// Package pemfile holds the checks shared by the inbound (internal/servertls)
// and outbound (internal/httpclient) TLS loaders for the PEM files named in
// configuration.
package pemfile

import (
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
