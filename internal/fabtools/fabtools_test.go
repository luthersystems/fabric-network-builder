package fabtools

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

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
	require.NoError(t, render.Write(dir, files, nil, io.Discard))
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
	// Fabric 2.5 and 3.x peers and orderers check that the header data hash
	// is the SHA-256 of the joined data. fabric-x-common hashes with length
	// prefixes, and a Fabric 3 orderer refuses that block (#34).
	want := sha256.Sum256(bytes.Join(blk.Data.Data, nil))
	assert.Equal(t, want[:], blk.Header.DataHash)
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
	require.NoError(t, render.Write(dir, files, nil, io.Discard))
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

// The mars Kubernetes roles start a Fabric 2.5 orderer from genesis.block
// (the orderer system channel), create the channel with channel.tx and set
// the anchor peers with <MSP>anchors.tx. fnb-gen.sh ships these files to
// mars, so generate must keep writing them, as v0.0.10 did.
func TestSystemChannelArtifacts(t *testing.T) {
	dir := project(t, topology.DefaultOptions())
	cc := filepath.Join(dir, "crypto-config")
	require.NoError(t, GenerateCrypto(filepath.Join(dir, "crypto-config.yaml"), cc))
	out := filepath.Join(dir, "channel-artifacts")
	require.NoError(t, SystemChannelArtifacts(dir, "luther", out))

	b, err := os.ReadFile(filepath.Join(out, SystemChannelBlock))
	require.NoError(t, err)
	blk, err := protoutil.UnmarshalBlock(b)
	require.NoError(t, err)
	want := sha256.Sum256(bytes.Join(blk.Data.Data, nil))
	assert.Equal(t, want[:], blk.Header.DataHash)
	env, err := protoutil.ExtractEnvelope(blk, 0)
	require.NoError(t, err)
	cfg := &cb.ConfigEnvelope{}
	_, err = protoutil.UnmarshalEnvelopeOfType(env, cb.HeaderType_CONFIG, cfg)
	require.NoError(t, err)
	assert.Contains(t, cfg.Config.ChannelGroup.Groups, "Consortiums")
	assert.NotContains(t, cfg.Config.ChannelGroup.Groups, "Application")

	updates := map[string]string{ChannelCreateTx: "", "Org1MSPanchors.tx": "AnchorPeers", "Org2MSPanchors.tx": "AnchorPeers"}
	for name, value := range updates {
		b, err := os.ReadFile(filepath.Join(out, name))
		require.NoError(t, err, name)
		env := &cb.Envelope{}
		require.NoError(t, proto.Unmarshal(b, env))
		cue := &cb.ConfigUpdateEnvelope{}
		ch, err := protoutil.UnmarshalEnvelopeOfType(env, cb.HeaderType_CONFIG_UPDATE, cue)
		require.NoError(t, err, name)
		assert.Equal(t, "luther", ch.ChannelId, name)
		cu := &cb.ConfigUpdate{}
		require.NoError(t, proto.Unmarshal(cue.ConfigUpdate, cu))
		app := cu.WriteSet.Groups["Application"]
		require.NotNil(t, app, name)
		if value != "" {
			org := app.Groups[name[:len(name)-len("anchors.tx")]]
			require.NotNil(t, org, name)
			assert.Contains(t, org.Values, value, name)
		}
	}
}
