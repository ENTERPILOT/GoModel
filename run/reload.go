package run

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/enterpilot/gomodel/config"
)

// reloadSignal asks a running gateway to re-read its configuration, the same
// way SIGHUP does for nginx. `gomodel --reload` sends it; `kill -HUP <pid>`
// works just as well.
const reloadSignal = syscall.SIGHUP

// envFile is the environment file loaded at startup and re-read on reload.
const envFile = ".env"

// dotenv applies envFile to the process environment and remembers which
// variables it set. A reload can then pick up edited values, and drop the ones
// removed from the file, without clobbering variables that came from the real
// environment: a value already exported wins over the file, exactly as it does
// at startup.
type dotenv struct {
	applied map[string]string
}

func newDotenv() *dotenv {
	return &dotenv{applied: make(map[string]string)}
}

// apply merges the current contents of envFile into the process environment.
// A missing file is normal — configuration may come entirely from the real
// environment — and clears whatever the file previously contributed.
func (d *dotenv) apply() {
	values, err := godotenv.Read(envFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("failed to read env file; keeping the current environment", "file", envFile, "error", err)
			return
		}
		values = map[string]string{}
	}

	for key, value := range values {
		if _, owned := d.applied[key]; !owned {
			if _, exported := os.LookupEnv(key); exported {
				continue
			}
		}
		if err := os.Setenv(key, value); err != nil {
			slog.Warn("failed to apply env file variable", "file", envFile, "variable", key, "error", err)
			continue
		}
		d.applied[key] = value
	}

	for key := range d.applied {
		if _, present := values[key]; present {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			slog.Warn("failed to unset removed env file variable", "file", envFile, "variable", key, "error", err)
			continue
		}
		delete(d.applied, key)
	}
}

// writePIDFile records the running process id so `gomodel --reload` can find
// the gateway to signal. The returned function removes the file again, unless
// another instance has claimed it in the meantime.
func writePIDFile(path string) (func(), error) {
	remove := func() {}
	path = strings.TrimSpace(path)
	if path == "" {
		return remove, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return remove, fmt.Errorf("create pid file directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return remove, fmt.Errorf("write pid file %s: %w", path, err)
	}
	return func() {
		// Leave the file alone if it is no longer ours: another instance has
		// taken this path over, and removing it would leave that one
		// unreachable from --reload.
		if pid, err := readPIDFile(path); err == nil && pid != os.Getpid() {
			return
		}
		_ = os.Remove(path)
	}, nil
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("no pid file at %s: is the gateway running?", path)
		}
		return 0, fmt.Errorf("read pid file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pid file %s does not contain a process id", path)
	}
	return pid, nil
}

// sendReloadSignal implements --reload: it tells the gateway recorded in the
// pid file to re-read its configuration, the way `nginx -s reload` does. The
// running process keeps serving on the current configuration if the new one
// turns out to be invalid.
func sendReloadSignal(stdout io.Writer) error {
	result, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	path := strings.TrimSpace(result.Config.Server.PIDFile)
	if path == "" {
		return errors.New("no pid file configured: set server.pid_file (or PID_FILE) on the gateway and on this command")
	}
	pid, err := readPIDFile(path)
	if err != nil {
		return err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d from %s: %w", pid, path, err)
	}
	if err := process.Signal(reloadSignal); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("process %d from %s is not running: remove the stale pid file", pid, path)
		}
		return fmt.Errorf("signal process %d from %s: %w", pid, path, err)
	}

	fmt.Fprintf(stdout, "reload requested (pid %d)\n", pid)
	return nil
}
