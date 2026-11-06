package admin

import (
	"testing"

	"github.com/luthersystems/fabric-network-builder/third_party/fabric/policydsl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Endorsement and collection policies must parse to the same bytes as the
// peer CLI makes, or an org that approves with `peer lifecycle` and an org
// that approves with the builder approve different definitions. These are
// the inputs where fabric-x-common's parser differs from Fabric v3.1.4's.
func TestPolicyParsesLikeFabric(t *testing.T) {
	// Fabric accepts lower-case operators.
	p, err := policydsl.FromString("or('Org1MSP.member', 'Org2MSP.member')")
	require.NoError(t, err)
	assert.Len(t, p.Identities, 2)

	// Fabric keeps one identity for each mention of a principal.
	p, err = policydsl.FromString("OR('Org1MSP.member', 'Org1MSP.member')")
	require.NoError(t, err)
	assert.Len(t, p.Identities, 2)

	// Fabric accepts the OutOf count as a string.
	_, err = policydsl.FromString("OutOf('1', 'Org1MSP.member', 'Org2MSP.member')")
	require.NoError(t, err)

	// Fabric rejects an upper-case role.
	_, err = policydsl.FromString("OR('Org1MSP.MEMBER')")
	assert.Error(t, err)
}
