//go:build integration

package integration

import "testing"

func TestParseDockerRunContainerID(t *testing.T) {
	t.Parallel()

	output := "Unable to find image 'postgres:16-alpine' locally\r\n" +
		"16-alpine: Pulling from library/postgres\r\n" +
		"Status: Downloaded newer image for postgres:16-alpine\r\n" +
		"e591860a76dbb858258e678a2d9c76fbd5fc3e6c5bf6e86f93266a14ca6be7f8\r\n"

	id, err := parseDockerRunContainerID(output)
	if err != nil {
		t.Fatalf("parseDockerRunContainerID returned error: %v", err)
	}

	const want = "e591860a76dbb858258e678a2d9c76fbd5fc3e6c5bf6e86f93266a14ca6be7f8"
	if id != want {
		t.Fatalf("parseDockerRunContainerID returned %q, want %q", id, want)
	}
}

func TestParseDockerRunContainerIDRejectsUnexpectedOutput(t *testing.T) {
	t.Parallel()

	_, err := parseDockerRunContainerID("Status: Downloaded newer image for postgres:16-alpine")
	if err == nil {
		t.Fatal("parseDockerRunContainerID unexpectedly succeeded")
	}
}
