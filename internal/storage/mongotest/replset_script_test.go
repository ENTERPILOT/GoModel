package mongotest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replsetScript is the CI helper that provides MONGO_TEST_DSN in the unit job.
const replsetScript = "../../../tools/ci/mongo-replset.sh"

// fakeDocker stands in for the docker CLI. pull fails FAKE_PULL_FAILURES
// times before succeeding; run fails when FAKE_RUN_FAIL=1; inspect, exec and
// logs report a healthy single-node replica set once run has succeeded. It
// records calls in $FAKE_DOCKER_DIR so tests can count them.
const fakeDocker = `#!/bin/sh
dir="$FAKE_DOCKER_DIR"
case "$1" in
pull)
	n=$(cat "$dir/pulls" 2>/dev/null || echo 0)
	n=$((n + 1))
	echo "$n" >"$dir/pulls"
	if [ "$n" -le "${FAKE_PULL_FAILURES:-0}" ]; then
		echo "toomanyrequests: Rate exceeded" >&2
		exit 1
	fi
	;;
run)
	if [ "${FAKE_RUN_FAIL:-0}" = 1 ]; then
		echo "docker: port is already allocated" >&2
		exit 1
	fi
	touch "$dir/container"
	echo fake-container-id
	;;
inspect)
	[ -f "$dir/container" ] || exit 1
	echo true
	;;
exec)
	case "$*" in
	*ismaster*) echo true ;;
	*) echo 1 ;;
	esac
	;;
esac
exit 0
`

type replsetRun struct {
	dir    string
	env    []string
	output string
	err    error
}

// runReplset runs the script with docker replaced by fakeDocker and the state
// directory isolated, so it never touches a real container or a real CI run's
// state file.
func runReplset(t *testing.T, env map[string]string, args ...string) replsetRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the replica set helper is a POSIX shell script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}

	dir := env["MONGO_STATE_DIR"]
	if dir == "" {
		dir = t.TempDir()
	}
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755))

	vars := []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_DIR=" + dir,
		"MONGO_STATE_DIR=" + dir,
		"MONGO_PULL_BACKOFF_SECONDS=0",
	}
	for key, value := range env {
		if key != "MONGO_STATE_DIR" {
			vars = append(vars, key+"="+value)
		}
	}

	cmd := exec.Command(sh, append([]string{replsetScript}, args...)...)
	cmd.Env = vars
	out, err := cmd.CombinedOutput()
	return replsetRun{dir: dir, env: vars, output: string(out), err: err}
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

func TestReplsetScriptPullStates(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantErr   bool
		wantState string
		wantPulls string
		wantRun   bool
	}{
		{
			name:      "transient rate limit is retried",
			env:       map[string]string{"FAKE_PULL_FAILURES": "2", "MONGO_PULL_ATTEMPTS": "3"},
			wantState: "started",
			wantPulls: "3",
			wantRun:   true,
		},
		{
			name:      "exhausted retries fail without running",
			env:       map[string]string{"FAKE_PULL_FAILURES": "5", "MONGO_PULL_ATTEMPTS": "2"},
			wantErr:   true,
			wantState: "failed",
			wantPulls: "2",
		},
		{
			name:      "failed run is recorded",
			env:       map[string]string{"FAKE_RUN_FAIL": "1"},
			wantErr:   true,
			wantState: "failed",
			wantPulls: "1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := runReplset(t, tc.env, "__pull_and_run")
			if tc.wantErr {
				assert.Error(t, run.err, run.output)
			} else {
				assert.NoError(t, run.err, run.output)
			}
			assert.Equal(t, tc.wantState, readTrimmed(t, filepath.Join(run.dir, "gomodel-mongo-start.state")))
			assert.Equal(t, tc.wantPulls, readTrimmed(t, filepath.Join(run.dir, "pulls")))
			_, statErr := os.Stat(filepath.Join(run.dir, "container"))
			assert.Equal(t, tc.wantRun, statErr == nil, "container started")
		})
	}
}

// start detaches the pull; wait must follow it to a primary on success and
// stop as soon as the start records a failure, well before its deadline.
func TestReplsetScriptStartThenWait(t *testing.T) {
	t.Run("reaches a primary after a transient failure", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{"FAKE_PULL_FAILURES": "1", "MONGO_PULL_ATTEMPTS": "3", "MONGO_STATE_DIR": t.TempDir()}
		start := runReplset(t, env, "start")
		require.NoError(t, start.err, start.output)
		wait := runReplset(t, env, "wait")
		require.NoError(t, wait.err, wait.output)
		assert.Contains(t, wait.output, "primary ready")
	})

	t.Run("fails fast when the start failed", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{"FAKE_PULL_FAILURES": "9", "MONGO_PULL_ATTEMPTS": "2", "MONGO_WAIT_SECONDS": "120", "MONGO_STATE_DIR": t.TempDir()}
		start := runReplset(t, env, "start")
		require.NoError(t, start.err, start.output)
		wait := runReplset(t, env, "wait")
		require.Error(t, wait.err)
		assert.Contains(t, wait.output, "did not start")
		assert.Contains(t, wait.output, "failed after 2 attempts")
	})
}
