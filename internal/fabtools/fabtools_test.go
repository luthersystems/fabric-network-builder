package fabtools

import (
	"os"
	"path/filepath"
	"testing"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/luthersystems/fabric-network-builder/internal/render"
	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

func project(t *testing.T, o topology.Options) string {
	t.Helper()
	n, err := topology.New(o)
	require.NoError(t, err)
	files, err := render.Network(n)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, render.Write(dir, files, nil))
	return dir
}

func TestGenerateExtendAndGenesisBlock(t *testing.T) {
	o := topology.DefaultOptions()
	o.OrdererCount = 3
	dir := project(t, o)
	cc := filepath.Join(dir, "crypto-config")
	require.NoError(t, GenerateCrypto(filepath.Join(dir, "crypto-config.yaml"), cc))
	for _, p := range []string{
		"peerOrganizations/org1.example.com/peers/peer1.org1.example.com/tls/server.crt",
		"peerOrganizations/org2.example.com/users/Admin@org2.example.com/msp/signcerts/Admin@org2.example.com-cert.pem",
		"ordererOrganizations/example.com/orderers/orderer2.example.com/tls/server.crt",
	} {
		assert.FileExists(t, filepath.Join(cc, p))
	}

	block := filepath.Join(dir, "channel-artifacts", "luther.block")
	require.NoError(t, ChannelGenesisBlock(dir, "luther", block))
	b, err := os.ReadFile(block)
	require.NoError(t, err)
	blk, err := protoutil.UnmarshalBlock(b)
	require.NoError(t, err)
	env, err := protoutil.ExtractEnvelope(blk, 0)
	require.NoError(t, err)
	payload, err := protoutil.UnmarshalPayload(env.Payload)
	require.NoError(t, err)
	ch, err := protoutil.UnmarshalChannelHeader(payload.Header.ChannelHeader)
	require.NoError(t, err)
	assert.Equal(t, "luther", ch.ChannelId)
	cfg := &cb.ConfigEnvelope{}
	_, err = protoutil.UnmarshalEnvelopeOfType(env, cb.HeaderType_CONFIG, cfg)
	require.NoError(t, err)
	app := cfg.Config.ChannelGroup.Groups["Application"]
	require.NotNil(t, app)
	assert.Contains(t, app.Groups, "Org1MSP")
	assert.Contains(t, app.Groups, "Org2MSP")

	// Extend with a third peer per org: the CAs stay, the new peer appears.
	caBefore, err := os.ReadFile(filepath.Join(cc, "peerOrganizations/org1.example.com/ca/ca.org1.example.com-cert.pem"))
	require.NoError(t, err)
	o.PeerCount = 3
	n, err := topology.New(o)
	require.NoError(t, err)
	files, err := render.Network(n)
	require.NoError(t, err)
	require.NoError(t, render.Write(dir, files, nil))
	require.NoError(t, ExtendCrypto(filepath.Join(dir, "crypto-config.yaml"), cc))
	assert.FileExists(t, filepath.Join(cc, "peerOrganizations/org1.example.com/peers/peer2.org1.example.com/tls/server.crt"))
	caAfter, err := os.ReadFile(filepath.Join(cc, "peerOrganizations/org1.example.com/ca/ca.org1.example.com-cert.pem"))
	require.NoError(t, err)
	assert.Equal(t, caBefore, caAfter)
}

func TestBadConfigIsAnError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "configtx.yaml"), []byte("Profiles: {}\n"), 0o644))
	require.ErrorContains(t, ChannelGenesisBlock(dir, "c", filepath.Join(dir, "x.block")), "configtxgen")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "crypto.yaml"), []byte("PeerOrgs: ["), 0o644))
	require.ErrorContains(t, GenerateCrypto(filepath.Join(dir, "crypto.yaml"), dir), "cryptogen")
}
