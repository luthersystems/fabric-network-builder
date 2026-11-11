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

// Load forwards the CHAINCODE_*, SUBSTRATE_* and OTEL_* variables of the
// process environment to the ForwardEnv services only, and a value the file
// sets wins. It binds published ports without a host address to
// PublishAddr and leaves an explicit one as it is.
func TestLoadForwardEnvAndPublishAddr(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.yaml"), []byte(`services:
  cc-peer0:
    image: busybox
    ports:
      - "9080:8080"
    environment:
      CHAINCODE_LOG_LEVEL:
      OTEL_SERVICE_NAME: from-file
  peer0:
    image: busybox
    ports:
      - "7051:7051"
      - "10.1.2.3:7053:7053"
`), 0o600))
	t.Setenv("CHAINCODE_LOG_LEVEL", "debug")
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "200")
	t.Setenv("OTEL_SERVICE_NAME", "from-env")
	t.Setenv("SUBSTRATE_X", "y")
	t.Setenv("UNRELATED_VAR", "z")
	p, err := Load(context.Background(), Options{
		Dir:         dir,
		Name:        "forwardtest",
		Files:       []string{"docker-compose.yaml"},
		Out:         io.Discard,
		ForwardEnv:  []string{"cc-peer0", "missing"},
		PublishAddr: "127.0.0.1",
	})
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	cc := p.project.Services["cc-peer0"].Environment
	get := func(k string) string {
		v := cc[k]
		if v == nil {
			return "<unset>"
		}
		return *v
	}
	assert.Equal(t, "debug", get("CHAINCODE_LOG_LEVEL"))
	assert.Equal(t, "200", get("OTEL_BSP_SCHEDULE_DELAY"))
	assert.Equal(t, "from-file", get("OTEL_SERVICE_NAME"))
	assert.Equal(t, "y", get("SUBSTRATE_X"))
	assert.NotContains(t, cc, "UNRELATED_VAR")
	assert.NotContains(t, p.project.Services["peer0"].Environment, "OTEL_BSP_SCHEDULE_DELAY")

	assert.Equal(t, "127.0.0.1", p.project.Services["cc-peer0"].Ports[0].HostIP)
	peer := p.project.Services["peer0"].Ports
	require.Len(t, peer, 2)
	assert.Equal(t, "127.0.0.1", peer[0].HostIP)
	assert.Equal(t, "10.1.2.3", peer[1].HostIP)
}
