package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogText(t *testing.T) {
	tests := map[string]string{
		"openai":                         "openai",
		"":                               "",
		"pii\nlevel=ERROR msg=forged":    "piilevel=ERROR msg=forged",
		"docs\r\nlevel=ERROR msg=forged": "docslevel=ERROR msg=forged",
	}
	for in, want := range tests {
		assert.Equal(t, want, LogText(in), in)
	}
}
