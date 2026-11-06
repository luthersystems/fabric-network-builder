package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, args ...string) (*command, map[string]string, *values) {
	t.Helper()
	cmd, pos, v, err := parse(globals, commands, args)
	require.NoError(t, err)
	return cmd, pos, v
}

func parseErr(t *testing.T, args ...string) string {
	t.Helper()
	_, _, _, err := parse(globals, commands, args)
	require.Error(t, err)
	return err.Error()
}

// The command lines below are the ones substrate, sandbox and ui-core run.
func TestConsumerCommandLines(t *testing.T) {
	cmd, _, v := mustParse(t, "--chown", "1000:1000", "--channel", "luther", "--force", "generate",
		"--domain=luther.systems", "--cc-name=com_luthersystems_chaincode_substrate01", "--domain", "luther.systems",
		"--org-count=2", "--peer-count=2", "--template")
	assert.Equal(t, cmdGenerate, cmd)
	assert.Equal(t, "1000:1000", v.str("chown", ""))
	assert.Equal(t, "luther.systems", v.str("domain_name", ""))
	assert.Equal(t, 2, v.integer("org_count", 0))
	assert.True(t, v.boolean("force", false))
	tmpl, set := v.bools["template"]
	assert.True(t, set && tmpl)

	cmd, _, v = mustParse(t, "--channel", "luther", "--force", "-s", "goleveldb", "up", "--log-spec", "debug", "--cc-version", "1.2.3")
	assert.Equal(t, cmdUp, cmd)
	assert.Equal(t, "goleveldb", v.str("storage", ""))
	assert.Equal(t, "1.2.3", v.str("cc_version", ""))

	cmd, pos, v := mustParse(t, "--force", "generatecc", "--ccaas", "--image-override", "a=x/y:1", "--image-override", "b=z:2",
		"com_luthersystems_chaincode_substrate01", "1.0", "a b", "/chaincodes/com_luthersystems_chaincode_substrate01-1.0.tar.gz")
	assert.Equal(t, cmdGeneratecc, cmd)
	assert.Equal(t, []string{"a=x/y:1", "b=z:2"}, v.lists["image_override"])
	assert.Equal(t, "a b", pos["cc_variants"])
	assert.Equal(t, "/chaincodes/com_luthersystems_chaincode_substrate01-1.0.tar.gz", pos["cc_path"])

	cmd, pos, v = mustParse(t, "-f", "install", "--init-required", "n", "v", "a", "/chaincodes/x.tar.gz")
	assert.Equal(t, cmdInstall, cmd)
	assert.True(t, v.boolean("init_required", false))
	assert.Equal(t, "n", pos["cc_name"])

	cmd, _, v = mustParse(t, "--force", "generate", "--domain-name", "x.com", "--peer-count", "1", "--org-count", "1", "--orderer-count", "3")
	assert.Equal(t, cmdGenerate, cmd)
	assert.Equal(t, 3, v.integer("orderer_count", 0))
}

func TestListOptionTakesFollowingWords(t *testing.T) {
	_, _, v := mustParse(t, "generate", "--orderer-san-domains", "a.example", "b.example", "--peer-san-domains", "org{i}.x")
	assert.Equal(t, []string{"a.example", "b.example"}, v.lists["orderer_san_domains"])
	assert.Equal(t, []string{"org{i}.x"}, v.lists["peer_san_domains"])
	assert.Contains(t, parseErr(t, "generate", "--peer-san-domains"), "expected at least one argument")
}

func TestPrefixMatching(t *testing.T) {
	_, _, v := mustParse(t, "generate", "--node", "--org", "3")
	assert.True(t, v.boolean("enable_node_ous", false), "--node is a prefix of --node-ous only")
	assert.Equal(t, 3, v.integer("org_count", 0))
	_, _, v = mustParse(t, "generate", "--node-ous", "--no-node-ous")
	assert.False(t, v.boolean("enable_node_ous", true), "the last one wins")
	assert.Contains(t, parseErr(t, "generate", "--no", "1"), "ambiguous option")
	_, _, v = mustParse(t, "reissue", "--node", "peer1", "--peer", "peer2")
	assert.Equal(t, []string{"peer1", "peer2"}, v.lists["node"], "--node is an exact name here")
}

func TestShortOptions(t *testing.T) {
	_, _, v := mustParse(t, "-scouchdb", "-f", "generate", "-a", "out.tar.xz")
	assert.Equal(t, "couchdb", v.str("storage", ""))
	assert.Equal(t, "out.tar.xz", v.str("archive_path", ""))
}

func TestErrors(t *testing.T) {
	assert.Contains(t, parseErr(t), "required: command")
	assert.Contains(t, parseErr(t, "nope"), "invalid choice")
	assert.Contains(t, parseErr(t, "generate", "--org-count", "x"), "invalid int value")
	assert.Contains(t, parseErr(t, "generate", "--bogus"), "unrecognized arguments: --bogus")
	assert.Contains(t, parseErr(t, "install", "a", "b"), "required: cc_variants, cc_path")
	assert.Contains(t, parseErr(t, "down", "extra"), "unrecognized arguments: extra")
	assert.Contains(t, parseErr(t, "generate", "--force"), "unrecognized arguments: --force", "a global option after the command")
	assert.Contains(t, parseErr(t, "generate", "--template=x"), "ignored explicit argument")
	_, _, _, err := parse(globals, commands, []string{"generate", "-h"})
	var h errHelp
	require.ErrorAs(t, err, &h)
	assert.Equal(t, cmdGenerate, h.cmd)
}
