package mongotest

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

			assert.Less(t, len(got), 64, "len(%q) = %d, want < 64", got, len(got))

			// MongoDB rejects these outright.
			assert.False(t, strings.ContainsAny(got, `/\. "$`+"\x00"), "name %q holds a character MongoDB rejects", got)

			// The pid keeps parallel package processes apart; the counter keeps
			// subtests within one process apart.
			assert.True(t, strings.HasSuffix(got, "_"+pid+"_"+strconv.FormatUint(tc.counter, 10)), "name %q does not end in the pid and counter", got)

		})
	}
}

// TestDatabaseNameSeparatesProcessesAndSubtests is the property that matters:
// two packages running the same test name concurrently must not create — and
// then drop — the same database.
func TestDatabaseNameSeparatesProcessesAndSubtests(t *testing.T) {
	first := DatabaseName("TestStoreDelete/mongodb", 1)
	second := DatabaseName("TestStoreDelete/mongodb", 2)
	require.NotEqual(t, second, first)
	require.Contains(t, first, strconv.Itoa(os.Getpid()))
}
