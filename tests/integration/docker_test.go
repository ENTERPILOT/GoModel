//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type dockerContainer struct {
	id string
}

func dockerRunDetached(ctx context.Context, options []string, image string, command ...string) (*dockerContainer, error) {
	args := []string{"run", "-d", "--rm"}
	args = append(args, options...)
	args = append(args, image)
	args = append(args, command...)

	output, err := runDocker(ctx, args...)
	if err != nil {
		return nil, err
	}

	id, err := parseDockerRunContainerID(output)
	if err != nil {
		return nil, err
	}

	return &dockerContainer{id: id}, nil
}

func (c *dockerContainer) exec(ctx context.Context, args ...string) (string, error) {
	dockerArgs := append([]string{"exec", c.id}, args...)
	return runDocker(ctx, dockerArgs...)
}

func (c *dockerContainer) hostPort(ctx context.Context, containerPort string) (string, error) {
	template := fmt.Sprintf(`{{(index (index .NetworkSettings.Ports %q) 0).HostPort}}`, containerPort)
	out, err := runDocker(ctx, "inspect", "--format", template, c.id)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("port %s is not published", containerPort)
	}
	return out, nil
}

func (c *dockerContainer) ip(ctx context.Context) (string, error) {
	out, err := runDocker(ctx, "inspect", "--format", `{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}`, c.id)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("container IP is empty")
	}
	return out, nil
}

func (c *dockerContainer) terminate(ctx context.Context) error {
	_, err := runDocker(ctx, "rm", "-f", c.id)
	return err
}

func runDocker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err == nil {
		return trimmed, nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("docker CLI is not available in PATH")
	}

	if trimmed == "" {
		return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, trimmed)
}

func waitForCondition(ctx context.Context, interval time.Duration, check func(context.Context) error) error {
	var lastErr error

	for {
		attemptCtx, cancel := context.WithTimeout(ctx, interval)
		lastErr = check(attemptCtx)
		cancel()
		if lastErr == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: last error: %v", ctx.Err(), lastErr)
		case <-time.After(interval):
		}
	}
}

func dockerPublishedHost() string {
	raw := os.Getenv("DOCKER_HOST")
	if raw == "" {
		return "127.0.0.1"
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "127.0.0.1"
	}

	switch parsed.Scheme {
	case "", "unix", "npipe":
		return "127.0.0.1"
	default:
		if host := parsed.Hostname(); host != "" {
			return host
		}
		return "127.0.0.1"
	}
}

func parseDockerRunContainerID(output string) (string, error) {
	var id string

	for _, line := range strings.FieldsFunc(output, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		line = strings.TrimSpace(line)
		if line != "" {
			id = line
		}
	}

	if id == "" {
		return "", fmt.Errorf("docker run did not return a container ID")
	}
	if !isLikelyDockerContainerID(id) {
		return "", fmt.Errorf("docker run returned an unexpected container ID: %q", id)
	}
	return id, nil
}

func isLikelyDockerContainerID(id string) bool {
	if len(id) < 12 {
		return false
	}

	for _, r := range id {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}

	return true
}
