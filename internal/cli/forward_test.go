package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishAddr(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	a, err := publishAddr(env(""))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", a)
	a, err = publishAddr(env("0.0.0.0"))
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", a)
	_, err = publishAddr(env("localhost"))
	assert.ErrorContains(t, err, "FNB_PUBLISH_ADDR")
}

func TestCCaaSServices(t *testing.T) {
	names, err := ccaasServices(filepath.Join(t.TempDir(), "none.yaml"))
	require.NoError(t, err)
	assert.Empty(t, names)

	names, err = ccaasServices(filepath.Join("..", "render", "testdata", "ccaas", "three", "docker-compose-ccaas.yaml"))
	require.NoError(t, err)
	assert.Len(t, names, 3)

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("services: [\n"), 0o600))
	_, err = ccaasServices(bad)
	assert.Error(t, err)
}
