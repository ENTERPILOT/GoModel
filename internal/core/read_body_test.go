package core

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadAllSized(t *testing.T) {
	const body = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	n := int64(len(body))
	readFailed := errors.New("connection reset")

	tests := []struct {
		name        string
		reader      func() io.Reader
		size        int64
		maxPrealloc int64
		want        string
		wantErr     error
		wantCap     int // 0: not asserted
	}{
		{name: "declared length", reader: func() io.Reader { return strings.NewReader(body) }, size: n, maxPrealloc: 1 << 20, want: body, wantCap: len(body) + 1},
		{name: "declared length one byte at a time", reader: func() io.Reader { return iotest.OneByteReader(strings.NewReader(body)) }, size: n, maxPrealloc: 1 << 20, want: body, wantCap: len(body) + 1},
		{name: "data returned with EOF", reader: func() io.Reader { return iotest.DataErrReader(strings.NewReader(body)) }, size: n, maxPrealloc: 1 << 20, want: body, wantCap: len(body) + 1},
		{name: "shorter than declared", reader: func() io.Reader { return strings.NewReader(body) }, size: n + 100, maxPrealloc: 1 << 20, want: body},
		{name: "longer than declared", reader: func() io.Reader { return strings.NewReader(body) }, size: 4, maxPrealloc: 1 << 20, want: body},
		{name: "empty body", reader: func() io.Reader { return strings.NewReader("") }, size: 0, maxPrealloc: 1 << 20, want: ""},
		{name: "unknown length", reader: func() io.Reader { return strings.NewReader(body) }, size: -1, maxPrealloc: 1 << 20, want: body},
		{name: "declared above prealloc cap", reader: func() io.Reader { return strings.NewReader(body) }, size: n, maxPrealloc: n - 1, want: body},
		{
			name: "read error keeps the bytes read so far",
			reader: func() io.Reader {
				return io.MultiReader(strings.NewReader(body[:10]), iotest.ErrReader(readFailed))
			},
			size: n, maxPrealloc: 1 << 20, want: body[:10], wantErr: readFailed,
		},
		{
			name:   "wrapped EOF is an error, as in io.ReadAll",
			reader: func() io.Reader { return iotest.ErrReader(&wrappedEOF{}) },
			size:   n, maxPrealloc: 1 << 20, want: "", wantErr: io.EOF,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadAllSized(tt.reader(), tt.size, tt.maxPrealloc)
			wantAll, wantAllErr := io.ReadAll(tt.reader())

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, string(got))
			assert.Equal(t, string(wantAll), string(got), "must match io.ReadAll")
			assert.Equal(t, wantAllErr, err, "must match io.ReadAll")
			assert.NotNil(t, got)
			if tt.wantCap > 0 {
				assert.Equal(t, tt.wantCap, cap(got), "a body of the declared length needs no growth")
			}
		})
	}
}

type wrappedEOF struct{}

func (*wrappedEOF) Error() string { return "wrapped: EOF" }

func (*wrappedEOF) Unwrap() error { return io.EOF }
