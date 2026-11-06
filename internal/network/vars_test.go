package network

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadVarsFromGolden(t *testing.T) {
	v, err := ReadVars(filepath.Join("..", "render", "testdata", "golden", "big"))
	require.NoError(t, err)
	assert.Equal(t, "example.org", v.DomainName)
	assert.Equal(t, []int{1, 2, 3}, v.OrgIndices)
	assert.Equal(t, []int{0}, v.PeerIndices)
	assert.Equal(t, []int{0, 1, 2}, v.OrdererIndices)
	assert.Contains(t, v.EndorsementPolicy, "'Org3MSP.member'")
}

func TestReadVarsErrors(t *testing.T) {
	_, err := ReadVars(t.TempDir())
	require.ErrorContains(t, err, "run generate first")
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "variables.sh"), []byte("DOMAIN_NAME=\"x\"\n"), 0o644))
	_, err = ReadVars(dir)
	require.ErrorContains(t, err, "ORG_INDICES")
}

// Projects generated before the network had several orderers commit a
// variables.sh with no ORDERER_INDICES (substrate's e2e networks, ui-core).
// That network had one orderer, orderer0.
func TestReadVarsWithoutOrdererIndices(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "variables.sh"), []byte(`#!/usr/bin/env false

DOMAIN_NAME="luther.systems"
ORG_INDICES=( 1 2 )
PEER_INDICES=( 0 1 )
ENDORSEMENT_POLICY="OR('Org1MSP.member', 'Org2MSP.member')"
`), 0o644))
	v, err := ReadVars(dir)
	require.NoError(t, err)
	assert.Equal(t, []int{0}, v.OrdererIndices)
	assert.Equal(t, []int{1, 2}, v.OrgIndices)
}
