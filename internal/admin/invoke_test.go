package admin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePeer(t *testing.T) {
	for s, want := range map[string]PeerRef{
		"peer0.org1":                 {Org: 1, Peer: 0},
		"peer2.org10.luther.systems": {Org: 10, Peer: 2},
	} {
		got, err := ParsePeer(s)
		require.NoError(t, err, s)
		assert.Equal(t, want, got, s)
	}
	for _, s := range []string{"", "org1", "peer0", "orderer0.example.com", "peerX.org1"} {
		_, err := ParsePeer(s)
		assert.Error(t, err, s)
	}
}

// ParseCtor reads what the peer CLI's -c took, so the callers of
// scripts/init.sh can pass the same constructor to `invoke --init`.
func TestParseCtor(t *testing.T) {
	args, err := ParseCtor(`{"Args":["init","{\"a\":1}"]}`)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("init"), []byte(`{"a":1}`)}, args)

	args, err = ParseCtor(`{"Function":"put","Args":["k","v"]}`)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("put"), []byte("k"), []byte("v")}, args)

	args, err = ParseCtor(`{"args":["x"]}`)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("x")}, args)

	for _, s := range []string{"", "{}", `{"Args":[]}`, "init"} {
		_, err = ParseCtor(s)
		assert.Error(t, err, s)
	}
}
