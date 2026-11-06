package compose

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The network a running node is on is "byfn" when the CCaaS compose file
// was part of up, and "<project>_byfn" when it was not.
func TestIsNetworkFor(t *testing.T) {
	assert.True(t, isNetworkFor("byfn", "fnb"))
	assert.True(t, isNetworkFor("fnb_byfn", "fnb"))
	assert.False(t, isNetworkFor("other_byfn", "fnb"))
	assert.False(t, isNetworkFor("bridge", "fnb"))
}

// The CCaaS services take their package ID from CCID_<NAME> in the
// project's .env file, which the consumer writes (substrate's
// scripts/env.sh) before `up`. Load must read that file, as the
// docker-compose of v0.0.10 did; the variables of Options.Env still win.
func TestLoadReadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("docker-compose.yaml", `services:
  a-peer0:
    image: busybox
    environment:
      - CHAINCODE_ID=${CCID_A}
      - TAG=${IMAGE_TAG}
`)
	write(".env", "CCID_A=a-1:abc\nIMAGE_TAG=from-dotenv\n")
	p, err := Load(context.Background(), Options{
		Dir:   dir,
		Name:  "dotenvtest",
		Files: []string{"docker-compose.yaml"},
		Env:   map[string]string{"IMAGE_TAG": "from-builder"},
		Out:   io.Discard,
	})
	require.NoError(t, err)
	defer func() { _ = p.Close() }()
	env := p.project.Services["a-peer0"].Environment
	require.NotNil(t, env["CHAINCODE_ID"])
	assert.Equal(t, "a-1:abc", *env["CHAINCODE_ID"])
	require.NotNil(t, env["TAG"])
	assert.Equal(t, "from-builder", *env["TAG"])
}
