//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDockerRunContainerID(t *testing.T) {
	t.Parallel()

	output := "Unable to find image 'postgres:16-alpine' locally\r\n" +
		"16-alpine: Pulling from library/postgres\r\n" +
		"Status: Downloaded newer image for postgres:16-alpine\r\n" +
		"e591860a76dbb858258e678a2d9c76fbd5fc3e6c5bf6e86f93266a14ca6be7f8\r\n"

	id, err := parseDockerRunContainerID(output)
	require.NoError(t, err)

	const want = "e591860a76dbb858258e678a2d9c76fbd5fc3e6c5bf6e86f93266a14ca6be7f8"
	require.Equal(t, want, id)
}

func TestParseDockerRunContainerIDRejectsUnexpectedOutput(t *testing.T) {
	t.Parallel()

	_, err := parseDockerRunContainerID("Status: Downloaded newer image for postgres:16-alpine")
	require.Error(t, err)
}

// TestDockerContainerTerminateRemovesAnonymousVolumes guards the -v in
// terminate. Both database images declare data volumes, and an explicit
// `docker rm` without it left them behind on every run of the suite.
func TestDockerContainerTerminateRemovesAnonymousVolumes(t *testing.T) {
	image := mongoImage
	if image == "" {
		image = postgresImage
	}
	if image == "" {
		t.Skip("both databases are external, so no image was pulled")
	}

	ctx, cancel := context.WithTimeout(testCtx, time.Minute)
	defer cancel()

	// The image's entrypoint runs any other command as-is, so the volumes are
	// created without starting a second database.
	container, err := dockerRunDetached(ctx, nil, image, "sleep", "300")
	require.NoError(t, err)
	t.Cleanup(func() {
		// Fails harmlessly after the terminate below; it only matters when
		// the test stops before reaching it.
		_ = container.terminate(context.Background())
	})

	out, err := runDocker(ctx, "inspect", "--format", `{{range .Mounts}}{{.Name}} {{end}}`, container.id)
	require.NoError(t, err)
	volumes := strings.Fields(out)
	require.NotEmpty(t, volumes, "%s declares no volume, so this proves nothing", image)

	require.NoError(t, container.terminate(ctx))

	for _, volume := range volumes {
		_, err := runDocker(ctx, "volume", "inspect", volume)
		assert.Error(t, err, "volume %s outlived its container", volume)
	}
}
