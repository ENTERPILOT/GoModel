package core

import "testing"

func TestLogText(t *testing.T) {
	tests := map[string]string{
		"openai":                         "openai",
		"":                               "",
		"pii\nlevel=ERROR msg=forged":    "piilevel=ERROR msg=forged",
		"docs\r\nlevel=ERROR msg=forged": "docslevel=ERROR msg=forged",
	}
	for in, want := range tests {
		if got := LogText(in); got != want {
			t.Errorf("LogText(%q) = %q, want %q", in, got, want)
		}
	}
}
