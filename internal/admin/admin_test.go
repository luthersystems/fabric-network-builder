package admin

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	lb "github.com/hyperledger/fabric-protos-go-apiv2/peer/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// Only the peer's "not defined" answer means the first sequence. Any other
// error (a timeout, a peer not up yet) must not turn into sequence 1, which
// the peer would reject at approve when the chaincode is committed.
func TestCommittedNotDefined(t *testing.T) {
	notDefined := status.Error(codes.Unknown,
		"evaluate call to endorser returned error: chaincode response 500, namespace smoke is not defined")
	assert.True(t, committedNotDefined(notDefined, "smoke"))
	assert.False(t, committedNotDefined(notDefined, "smo"), "another chaincode's name")
	assert.False(t, committedNotDefined(status.Error(codes.Unavailable, "connection refused"), "smoke"))
	assert.False(t, committedNotDefined(context.DeadlineExceeded, "smoke"))
}

func TestHasPackageMatchesTheID(t *testing.T) {
	installed := []*lb.QueryInstalledChaincodesResult_InstalledChaincode{
		{PackageId: "cc-1.0:old", Label: "cc-1.0"},
	}
	assert.True(t, hasPackage(installed, "cc-1.0:old"))
	assert.False(t, hasPackage(installed, "cc-1.0:new"), "same label, another package")
}
