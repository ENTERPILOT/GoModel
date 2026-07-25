package mongotest

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestDatabaseName(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())

	tests := []struct {
		name     string
		testName string
		counter  uint64
	}{
		{"plain", "TestStoreDelete", 1},
		{"subtest path", "TestStoreDelete/mongodb", 2},
		{"deeply nested", strings.Repeat("TestSomethingWithAVeryLongName/", 8), 3},
		{"forbidden characters", `Test$Store."weird"\name`, 4},
		{"empty", "", 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DatabaseName(tc.testName, tc.counter)

			if len(got) >= 64 {
				t.Errorf("len(%q) = %d, want < 64", got, len(got))
			}
			// MongoDB rejects these outright.
			if strings.ContainsAny(got, `/\. "$`+"\x00") {
				t.Errorf("name %q holds a character MongoDB rejects", got)
			}
			// The pid keeps parallel package processes apart; the counter keeps
			// subtests within one process apart.
			if !strings.HasSuffix(got, "_"+pid+"_"+strconv.FormatUint(tc.counter, 10)) {
				t.Errorf("name %q does not end in the pid and counter", got)
			}
		})
	}
}

// TestDatabaseNameSeparatesProcessesAndSubtests is the property that matters:
// two packages running the same test name concurrently must not create — and
// then drop — the same database.
func TestDatabaseNameSeparatesProcessesAndSubtests(t *testing.T) {
	first := DatabaseName("TestStoreDelete/mongodb", 1)
	second := DatabaseName("TestStoreDelete/mongodb", 2)
	if first == second {
		t.Fatalf("counter did not separate subtests: both %q", first)
	}
	if !strings.Contains(first, strconv.Itoa(os.Getpid())) {
		t.Fatalf("name %q carries no pid, so another test process could collide", first)
	}
}
