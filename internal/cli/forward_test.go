package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
