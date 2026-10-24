package httpclient

import (
	"bytes"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"
)

func pemEncode(t *testing.T, der []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return buf.Bytes()
}
