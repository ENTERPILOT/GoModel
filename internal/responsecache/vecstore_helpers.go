package responsecache

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"
)

func vecPointID64(key, paramsHash string) uint64 {
	h := sha256.Sum256([]byte(key + "\x00" + paramsHash))
	return binary.BigEndian.Uint64(h[:8]) ^ binary.BigEndian.Uint64(h[8:16])
}

func pgvectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func trimSlash(s string) string {
	return strings.TrimSuffix(strings.TrimSpace(s), "/")
}
