package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"
	"gopkg.in/yaml.v3"
)

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), args, Streams{In: strings.NewReader(stdin), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

func archiveNamesIn(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	xr, err := xz.NewReader(f)
	require.NoError(t, err)
	tr := tar.NewReader(xr)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

func TestGenerateTemplateThenAssets(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errOut := runCLI(t, "", "--force", "generate", "--domain", "luther.systems", "--template")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "rendering crypto-config.yaml")
	assert.FileExists(t, "docker-compose-e2e-template.yaml")
	assert.NoDirExists(t, "crypto-config")

	code, _, errOut = runCLI(t, "", "--force", "generate", "--domain", "luther.systems", "--no-template")
	require.Equal(t, 0, code, errOut)
	assert.FileExists(t, "channel-artifacts/luther.block")
	// The v0.0.10 files that fnb-gen.sh ships to mars.
	for _, f := range []string{"genesis.block", "channel.tx", "Org1MSPanchors.tx", "Org2MSPanchors.tx"} {
		assert.FileExists(t, "channel-artifacts/"+f)
	}
	e2e, err := os.ReadFile("docker-compose-e2e.yaml")
	require.NoError(t, err)
	assert.NotContains(t, string(e2e), "_PRIVATE_KEY")
	keys, err := filepath.Glob("crypto-config/peerOrganizations/org2.luther.systems/ca/*_sk")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Contains(t, string(e2e), filepath.Base(keys[0]))
}

func TestGenerateRefusesExistingWithoutForce(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "y\n", "generate", "--org-count", "1", "--peer-count", "1")
	require.Equal(t, 0, code, errOut)
	code, _, errOut = runCLI(t, "y\n", "generate")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "already exist")
}

func TestPromptAbortsWithoutInput(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "generate", "--org-count", "1")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "no input on stdin")
	code, out, _ := runCLI(t, "maybe\nn\n", "--force", "generate", "--template")
	assert.Equal(t, 0, code, "--template asks nothing")
	assert.NotContains(t, out, "Continue")
}

func TestGenerateArchive(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "1", "--archive", "net.tar.xz")
	require.Equal(t, 0, code, errOut)
	names := archiveNamesIn(t, "net.tar.xz")
	assert.Contains(t, names, "configtx.yaml")
	assert.Contains(t, names, "channel-artifacts/luther.block")
	assert.Contains(t, names, "fabric-network.json")
	for _, n := range names {
		assert.NotContains(t, n, "scripts/", "generate writes no scripts (#35)")
	}
	assert.NotContains(t, names, "docker-compose-cli.yaml")
	left, err := filepath.Glob("fabric-network*")
	require.NoError(t, err)
	assert.Empty(t, left, "temporary directory removed")
	assert.NoFileExists(t, "configtx.yaml", "the archive does not touch the current directory")

	code, _, errOut = runCLI(t, "", "-f", "generate", "--template", "--archive", "x.tar.xz")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "cannot combine")
}

func TestExtendAddsPeersAndArchives(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "1")
	require.Equal(t, 0, code, errOut)
	assert.FileExists(t, "fabric-network.json")
	assert.NoDirExists(t, "scripts", "generate writes no scripts (#35)")
	code, _, errOut = runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "2", "--template")
	require.Equal(t, 0, code, errOut)
	code, _, errOut = runCLI(t, "", "-f", "extend", "--archive", "ext.tar.xz")
	require.Equal(t, 0, code, errOut)
	assert.DirExists(t, "crypto-config/peerOrganizations/org1.example.com/peers/peer1.org1.example.com")
	assert.Contains(t, archiveNamesIn(t, "ext.tar.xz"),
		"crypto-config/peerOrganizations/org1.example.com/peers/peer1.org1.example.com/tls/server.crt")
}

// extend --archive of a network that a builder before #35 generated: it
// has scripts/variables.sh and no fabric-network.json, and the archive must
// still carry its topology.
func TestExtendArchivesLegacyTopology(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "1")
	require.Equal(t, 0, code, errOut)
	require.NoError(t, os.Remove("fabric-network.json"))
	require.NoError(t, os.MkdirAll("scripts", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("scripts", "variables.sh"), []byte("DOMAIN_NAME=\"example.com\"\n"), 0o644))
	code, _, errOut = runCLI(t, "", "-f", "extend", "--archive", "ext.tar.xz")
	require.Equal(t, 0, code, errOut)
	names := archiveNamesIn(t, "ext.tar.xz")
	assert.Contains(t, names, "scripts/variables.sh")
	assert.NotContains(t, names, "fabric-network.json")
}

func TestExtendNeedsCryptoConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "-f", "extend")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "does not exist")
}

func TestGenerateccCCaaS(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("chaincodes", 0o755))
	code, _, errOut := runCLI(t, "", "-f", "generatecc", "--ccaas", "--image-override", "external=foo/bar:1.2",
		"cc", "1.0", "a-b external c", "/chaincodes/cc-1.0.tar.gz")
	require.Equal(t, 0, code, errOut)
	got, err := os.ReadFile("docker-compose-ccaas.yaml")
	require.NoError(t, err)
	for _, n := range []string{"a-b", "external", "c"} {
		assert.FileExists(t, "chaincodes/"+n+"-1.0.tar.gz")
		id, rerr := os.ReadFile("chaincodes/" + n + "-1.0.id")
		require.NoError(t, rerr)
		assert.True(t, strings.HasPrefix(string(id), n+"-1.0:"))
		// The compose file has the package ID itself; the goldens have
		// "feed" for the hash.
		got = bytes.ReplaceAll(got, bytes.TrimSpace(id), []byte(n+"-1.0:feed"))
	}
	want, err := os.ReadFile(filepath.Join(ccaasGoldens, "three", "docker-compose-ccaas.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

// ccaasGoldens is absolute because each test changes to a temp directory.
var ccaasGoldens = func() string {
	p, err := filepath.Abs(filepath.Join("..", "render", "testdata", "ccaas"))
	if err != nil {
		panic(err)
	}
	return p
}()

func TestGenerateccChecks(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, _ := runCLI(t, "", "-f", "generatecc", "cc", "1.0", "", "/chaincodes/x.tar.gz")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "No chaincodes to generate")
	assert.NoFileExists(t, "docker-compose-ccaas.yaml")

	code, _, errOut := runCLI(t, "", "-f", "generatecc", "--image-override", "a=b", "cc", "1.0", "a", "/chaincodes/x.tar.gz")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "requires --ccaas")

	code, _, errOut = runCLI(t, "", "-f", "generatecc", "--ccaas", "--image-override", "z=b", "cc", "1.0", "a", "/chaincodes/x.tar.gz")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "not in cc_variants")
}

func TestCertExpiriesAndReissue(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "1")
	require.Equal(t, 0, code, errOut)
	code, out, errOut := runCLI(t, "", "cert_expiries")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "\tcrypto-config/peerOrganizations/org1.example.com/ca/ca.org1.example.com-cert.pem\n")

	code, out, errOut = runCLI(t, "", "reissue", "--node", "peer0", "--days", "30", "--dry-run")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "DRY-RUN peer0.org1.example.com signcert")
	code, _, errOut = runCLI(t, "", "reissue", "--days", "0", "--all")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "positive")
}

func TestParseChown(t *testing.T) {
	o, err := parseChown("1000:1001")
	require.NoError(t, err)
	assert.Equal(t, &owner{1000, 1001}, o)
	o, err = parseChown("")
	require.NoError(t, err)
	assert.Nil(t, o)
	_, err = parseChown("nosuchuser-xyz:1")
	require.Error(t, err)
}

func TestResolveImageTags(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	def := imageTags{image: ImageTag, tools: ImageTag, ns: ImageNS, ca: CAImageTag, base: BaseImageTag, baseos: twoDigitVersion(ImageTag)}
	with := func(f func(*imageTags)) imageTags { tg := def; f(&tg); return tg }
	tags, err := resolveImageTags(env(nil))
	require.NoError(t, err)
	assert.Equal(t, def, tags, "no variable keeps the v0.1.0 output")
	assert.Equal(t, "hyperledger", tags.ns)
	tags, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "3.1.4"}))
	require.NoError(t, err)
	assert.Equal(t, with(func(tg *imageTags) { tg.image, tg.baseos = "3.1.4", "3.1" }), tags, "Fabric 3 has no tools image")
	tags, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "3.1.4", "FNB_TOOLSIMAGETAG": "2.5.9"}))
	require.NoError(t, err)
	assert.Equal(t, with(func(tg *imageTags) { tg.image, tg.tools, tg.baseos = "3.1.4", "2.5.9", "3.1" }), tags)
	_, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "2.4.9"}))
	require.ErrorContains(t, err, "2.5 and later")

	// A mirror: each override moves only its own value.
	ecr := "123456789012.dkr.ecr.us-east-1.amazonaws.com/docker-hub/hyperledger"
	tags, err = resolveImageTags(env(map[string]string{"FNB_IMAGENS": ecr}))
	require.NoError(t, err)
	assert.Equal(t, with(func(tg *imageTags) { tg.ns = ecr }), tags)
	tags, err = resolveImageTags(env(map[string]string{"FNB_CAIMAGETAG": "1.5.15", "FNB_BASEIMAGETAG": "0.4.18"}))
	require.NoError(t, err)
	assert.Equal(t, with(func(tg *imageTags) { tg.ca, tg.base = "1.5.15", "0.4.18" }), tags)
	tags, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "3.1.4", "FNB_IMAGENS": ecr}))
	require.NoError(t, err)
	assert.Equal(t, CAImageTag, tags.ca, "FNB_IMAGETAG does not move the CA tag")
	assert.Equal(t, BaseImageTag, tags.base, "FNB_IMAGETAG does not move the CouchDB tag")
	for _, name := range []string{"FNB_CAIMAGETAG", "FNB_BASEIMAGETAG"} {
		for _, v := range []string{":1.5.15", "1.5.15/", "sha256:abc", "1 5", ".1"} {
			_, err = resolveImageTags(env(map[string]string{name: v}))
			require.ErrorContains(t, err, name+" ", v)
		}
	}
	_, err = resolveImageTags(env(map[string]string{"FNB_IMAGENS": "bad/"}))
	require.ErrorContains(t, err, "FNB_IMAGENS")
}

func TestTwoDigitVersion(t *testing.T) {
	assert.Equal(t, "2.5", twoDigitVersion("2.5.16"))
	assert.Equal(t, "3.1", twoDigitVersion("3.1"))
	assert.Equal(t, "latest", twoDigitVersion("latest"))
}

func TestCheckImageNS(t *testing.T) {
	for _, ns := range []string{
		"hyperledger",
		"luthersystems",
		"docker.io/hyperledger",
		"ghcr.io/hyperledger",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/docker-hub/hyperledger",
		"localhost:5000/hyperledger",
		"localhost:5000",
		"Registry.Example.com:443/a/b_c/d-e",
	} {
		require.NoError(t, checkImageNS(ns), ns)
	}
	for ns, msg := range map[string]string{
		"hyperledger/":            "not a lowercase image path part",
		"/hyperledger":            "not a lowercase image path part",
		"ghcr.io//hyperledger":    "not a lowercase image path part",
		"ghcr.io/hyperledger:2":   "not a lowercase image path part",
		"ghcr.io/a:b/hyperledger": "not a lowercase image path part",
		"MyOrg/hyperledger":       "not a lowercase image path part",
		"hyper ledger":            "not a lowercase image path part",
		"$NS":                     "not a lowercase image path part",
		"hyperledger@sha256":      "not a lowercase image path part",
		"hyperledger:2.5":         "not a registry host",
		"hyperledger:25":          "not a registry host",
		"ghcr.io:x/hyperledger":   "not a registry host",
	} {
		require.ErrorContains(t, checkImageNS(ns), msg, ns)
	}
}

// The compose files read IMAGE_NS, CA_IMAGE_TAG and BASE_IMAGE_TAG from
// composeEnv, so an override must reach them there.
func TestComposeEnvImageOverrides(t *testing.T) {
	// composeEnv reads the process environment: clear what a shell may
	// have set, so the test does not depend on it.
	t.Setenv("FNB_IMAGETAG", "")
	t.Setenv("FNB_TOOLSIMAGETAG", "")
	t.Setenv("FNB_IMAGENS", "registry.example.com/hyperledger")
	t.Setenv("FNB_CAIMAGETAG", "1.5.15")
	t.Setenv("FNB_BASEIMAGETAG", "0.4.18")
	env, err := composeEnv(t.TempDir(), "luther", "")
	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/hyperledger", env["IMAGE_NS"])
	assert.Equal(t, "1.5.15", env["CA_IMAGE_TAG"])
	assert.Equal(t, "0.4.18", env["BASE_IMAGE_TAG"])
	assert.Equal(t, ImageTag, env["IMAGE_TAG"])
	assert.Equal(t, twoDigitVersion(ImageTag), env["BASEOS_IMAGE_TAG"])

	t.Setenv("FNB_IMAGENS", "bad/")
	_, err = composeEnv(t.TempDir(), "luther", "")
	require.ErrorContains(t, err, "FNB_IMAGENS")
}

// byfn.sh skipped generatecc for an empty variant list whatever the other
// arguments were, and consumer Makefiles pass empty names then.
func TestGenerateccEmptyVariantsSkipsFirst(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, errOut := runCLI(t, "", "-f", "generatecc", "", "v", "", "")
	assert.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "No chaincodes to generate")
}

// A network that v0.0.10 generated has the system channel files but no
// channel genesis block. `up` must create its channel from the channel creation
// transaction (CreateSystemChannel): its orderer has no channel
// participation API.
func TestSystemChannelNetworkDetection(t *testing.T) {
	dir := t.TempDir()
	e := &env{dir: dir, channel: "luther"}
	touch := func(rel string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	assert.False(t, e.systemChannelNetwork(), "empty dir")
	touch("channel-artifacts/genesis.block")
	assert.False(t, e.systemChannelNetwork(), "no channel.tx")
	touch("channel-artifacts/channel.tx")
	assert.True(t, e.systemChannelNetwork(), "v0.0.10 layout")
	touch("channel-artifacts/luther.block")
	assert.False(t, e.systemChannelNetwork(), "a new network has all files")
}

// A compose file that v0.0.x wrote gives each CCaaS service "$CCID_<NAME>".
// The builder takes it from the chaincodes/<name>-<version>.id files that
// generatecc writes, so a consumer needs no scripts/env.sh or .env file.
func TestChaincodeIDsFromIDFiles(t *testing.T) {
	dir := t.TempDir()
	cc := filepath.Join(dir, "chaincodes")
	require.NoError(t, os.MkdirAll(cc, 0o755))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(cc, name), []byte(body), 0o600))
	}
	write("a-2.1.0-SNAPSHOT.id", "a-2.1.0-SNAPSHOT:ff8e\n")
	write("my-cc-1.0.id", "my-cc-1.0:1234\n")
	write("b-1.0.tar.gz", "not an id")
	ids, err := chaincodeIDs(dir)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"CCID_A":  "a-2.1.0-SNAPSHOT:ff8e",
		"CCID_MY": "my-cc-1.0:1234",
	}, ids)

	none, err := chaincodeIDs(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, none)
}

// invoke checks its arguments before it touches the network.
func TestInvokeArgs(t *testing.T) {
	code, _, errOut := runCLI(t, "", "--force", "invoke", "a", "not json")
	assert.Equal(t, 2, code, errOut)
	assert.Contains(t, errOut, "constructor")
	code, _, errOut = runCLI(t, "", "--force", "invoke", "--peer", "org1", "a", `{"Args":["x"]}`)
	assert.Equal(t, 2, code, errOut)
	assert.Contains(t, errOut, "want peerJ.orgI")
	code, _, errOut = runCLI(t, "", "--force", "invoke", "a")
	assert.Equal(t, 2, code, errOut)
}

// ansible-vars writes the file that the mars sandbox template's
// fabric/fnb-gen.sh wrote with zip and base64: each value is a base64 zip
// (or, for collections.json, the base64 file) that the mars Kubernetes
// roles unpack.
func TestAnsibleVars(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errOut := runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "1")
	require.Equal(t, 0, code, errOut)
	code, _, errOut = runCLI(t, "", "ansible-vars", "--output", "fabric.yaml")
	require.Equal(t, 0, code, errOut)

	b, err := os.ReadFile("fabric.yaml")
	require.NoError(t, err)
	vars := map[string]string{}
	require.NoError(t, yaml.Unmarshal(b, &vars))
	assert.Len(t, vars, 3)

	coll, err := base64.StdEncoding.DecodeString(vars["k8s_fabric_collections"])
	require.NoError(t, err)
	want, err := os.ReadFile("collections.json")
	require.NoError(t, err)
	assert.Equal(t, want, coll)

	for key, dir := range map[string]string{
		"k8s_fabric_channel_artifacts": "channel-artifacts",
		"k8s_fabric_crypto_config":     "crypto-config",
	} {
		z, derr := base64.StdEncoding.DecodeString(vars[key])
		require.NoError(t, derr, key)
		zr, zerr := zip.NewReader(bytes.NewReader(z), int64(len(z)))
		require.NoError(t, zerr, key)
		got := map[string]string{}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, oerr := f.Open()
			require.NoError(t, oerr)
			data, rerr := io.ReadAll(rc)
			require.NoError(t, rerr)
			_ = rc.Close()
			got[f.Name] = string(data)
		}
		files := map[string]string{}
		require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil || d.IsDir() {
				return werr
			}
			data, rerr := os.ReadFile(p)
			files[filepath.ToSlash(p)] = string(data)
			return rerr
		}))
		assert.Equal(t, files, got, key)
		assert.NotEmpty(t, got, key)
	}
	z, err := base64.StdEncoding.DecodeString(vars["k8s_fabric_channel_artifacts"])
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "channel-artifacts/", "directory entries, as zip -r writes")
	assert.Contains(t, names, "channel-artifacts/genesis.block")
}

// ansible-vars --generate-from does all of the mars sandbox template's
// fabric/fnb-gen.sh, so the ui-core certs step can run the builder with no
// shell: it reads the network size from the Ansible env file, generates the
// network unless crypto-config exists (or --force), then writes the
// variables.
func TestAnsibleVarsGenerateFrom(t *testing.T) {
	t.Chdir(t.TempDir())
	env := `# mars env
project_id: abc
num_orgs: 1
num_peers: 2
num_orderers: 1
k8s_fabric_network_domain_root: "net.example"
`
	require.NoError(t, os.WriteFile("env.yaml", []byte(env), 0o600))
	code, _, errOut := runCLI(t, "", "ansible-vars", "--generate-from", "env.yaml", "-o", "fabric.yaml")
	require.Equal(t, 0, code, errOut)
	assert.DirExists(t, "crypto-config/peerOrganizations/org1.net.example/peers/peer1.org1.net.example")
	assert.NoDirExists(t, "crypto-config/peerOrganizations/org2.net.example")
	assert.FileExists(t, "channel-artifacts/genesis.block")
	first, err := os.ReadFile("fabric.yaml")
	require.NoError(t, err)
	vars := map[string]string{}
	require.NoError(t, yaml.Unmarshal(first, &vars))
	assert.Len(t, vars, 3)

	key := "crypto-config/ordererOrganizations/net.example/ca/ca.net.example-cert.pem"
	ca, err := os.ReadFile(key)
	require.NoError(t, err)

	// crypto-config exists: no new network, as fnb-gen.sh skipped.
	code, _, errOut = runCLI(t, "", "ansible-vars", "--generate-from", "env.yaml", "-o", "fabric.yaml")
	require.Equal(t, 0, code, errOut)
	again, err := os.ReadFile(key)
	require.NoError(t, err)
	assert.Equal(t, ca, again)

	// --force makes a new network.
	code, _, errOut = runCLI(t, "", "--force", "ansible-vars", "--generate-from", "env.yaml", "-o", "fabric.yaml")
	require.Equal(t, 0, code, errOut)
	again, err = os.ReadFile(key)
	require.NoError(t, err)
	assert.NotEqual(t, ca, again)
}

func TestAnsibleVarsGenerateFromMissingField(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("env.yaml", []byte("num_orgs: 1\nnum_peers: 1\nnum_orderers:\n"), 0o600))
	code, _, errOut := runCLI(t, "", "ansible-vars", "--generate-from", "env.yaml")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "num_orderers")
	assert.NoDirExists(t, "crypto-config")

	code, _, errOut = runCLI(t, "", "ansible-vars", "--generate-from", "missing.yaml")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "missing.yaml")
}
