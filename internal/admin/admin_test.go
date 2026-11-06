package admin

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every policy and collection set the templates can produce must parse as
// the peer CLI parsed it (policydsl), or install fails on a real network.
func TestGoldenPoliciesAndCollectionsParse(t *testing.T) {
	for _, name := range []string{"default", "luther", "big", "nchoose2", "nchoose2common"} {
		t.Run(name, func(t *testing.T) {
			n, err := New(filepath.Join("..", "render", "testdata", "golden", name), nil, io.Discard)
			require.NoError(t, err)
			p, err := n.applicationPolicy()
			require.NoError(t, err)
			assert.NotNil(t, p.GetSignaturePolicy())
			cols, err := n.collections()
			require.NoError(t, err)
			assert.NotEmpty(t, cols.GetConfig())
			for _, c := range cols.GetConfig() {
				assert.NotNil(t, c.GetStaticCollectionConfig().GetMemberOrgsPolicy().GetSignaturePolicy())
			}
		})
	}
}

func TestDefaultResolverIsHostPort(t *testing.T) {
	n, err := New(filepath.Join("..", "render", "testdata", "golden", "default"), nil, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "peer0.org1.example.com:7051", n.Resolve(n.Vars.Peer(1, 0), 7051))
	assert.Equal(t, "crypto-config/peerOrganizations/org1.example.com/users/Admin@org1.example.com", n.Vars.OrgAdminDir(1))
}
