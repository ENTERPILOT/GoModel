package pluginapi

import (
	"os/exec"
	"strings"
	"testing"
)

// TestStdlibOnly enforces the contract's one hard rule: pluginapi depends on
// the standard library only, so a plugin shares nothing else with the host.
func TestStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "github.com/enterpilot/gomodel/pluginapi" {
			continue
		}
		t.Errorf("pluginapi must import the standard library only, found dependency %q", line)
	}
}
