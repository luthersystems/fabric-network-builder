package admin

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"io"
	"path/filepath"
	"testing"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/luthersystems/fabric-network-builder/internal/fabtools"
	"github.com/luthersystems/fabric-network-builder/internal/render"
	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

// verifyBy checks that sig over msg is by the identity in creator.
func verifyBy(t *testing.T, creator, msg, sig []byte) string {
	t.Helper()
	id := &msp.SerializedIdentity{}
	require.NoError(t, proto.Unmarshal(creator, id))
	blk, _ := pem.Decode(id.IdBytes)
	require.NotNil(t, blk)
	cert, err := x509.ParseCertificate(blk.Bytes)
	require.NoError(t, err)
	digest := sha256.Sum256(msg)
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok)
	assert.True(t, ecdsa.VerifyASN1(pub, digest[:], sig), "signature by %s", id.Mspid)
	return id.Mspid
}

// The channel creation and anchor peer updates of a v0.0.x network are
// signed the way `peer channel create -f` and `peer channel update -f`
// sign them: the admin adds a signature to the config update and signs the
// envelope.
func TestSignConfigUpdate(t *testing.T) {
	n, err := topology.New(topology.DefaultOptions())
	require.NoError(t, err)
	files, err := render.Network(n)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, render.Write(dir, files, nil, io.Discard))
	require.NoError(t, fabtools.GenerateCrypto(filepath.Join(dir, "crypto-config.yaml"), filepath.Join(dir, "crypto-config")))
	out := filepath.Join(dir, "channel-artifacts")
	require.NoError(t, fabtools.SystemChannelArtifacts(dir, "luther", out))

	net, err := New(dir, nil, io.Discard)
	require.NoError(t, err)
	checkSignedUpdate(t, net, filepath.Join(out, fabtools.ChannelCreateTx), 1)
	checkSignedUpdate(t, net, filepath.Join(out, "Org2MSPanchors.tx"), 2)

	id, err := signer(net.Vars.MSPID(1), net.path(net.Vars.OrgAdminDir(1)))
	require.NoError(t, err)
	_, err = signConfigUpdate(filepath.Join(out, fabtools.ChannelCreateTx), "other", sdkSigner{id})
	require.ErrorContains(t, err, `for channel "luther"`)
	_, err = signConfigUpdate(filepath.Join(out, fabtools.SystemChannelBlock), "luther", sdkSigner{id})
	assert.Error(t, err, "a block is not a config update")
}

// checkSignedUpdate signs the config update in file as the admin of org,
// and checks both signatures.
func checkSignedUpdate(t *testing.T, net *Network, file string, org int) {
	t.Helper()
	id, err := signer(net.Vars.MSPID(org), net.path(net.Vars.OrgAdminDir(org)))
	require.NoError(t, err)
	env, err := signConfigUpdate(file, "luther", sdkSigner{id})
	require.NoError(t, err, file)

	payload, err := protoutil.UnmarshalPayload(env.Payload)
	require.NoError(t, err)
	ch, err := protoutil.UnmarshalChannelHeader(payload.Header.ChannelHeader)
	require.NoError(t, err)
	assert.Equal(t, int32(cb.HeaderType_CONFIG_UPDATE), ch.Type)
	assert.Equal(t, "luther", ch.ChannelId)
	sh, err := protoutil.UnmarshalSignatureHeader(payload.Header.SignatureHeader)
	require.NoError(t, err)
	assert.Equal(t, net.Vars.MSPID(org), verifyBy(t, sh.Creator, env.Payload, env.Signature))

	update := &cb.ConfigUpdateEnvelope{}
	require.NoError(t, proto.Unmarshal(payload.Data, update))
	require.Len(t, update.Signatures, 1, file)
	sig := update.Signatures[0]
	csh, err := protoutil.UnmarshalSignatureHeader(sig.SignatureHeader)
	require.NoError(t, err)
	msg := append(append([]byte{}, sig.SignatureHeader...), update.ConfigUpdate...)
	assert.Equal(t, net.Vars.MSPID(org), verifyBy(t, csh.Creator, msg, sig.Signature))
}
