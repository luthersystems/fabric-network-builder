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
	assert.Contains(t, names, "scripts/variables.sh")
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
	code, _, errOut = runCLI(t, "", "-f", "generate", "--org-count", "1", "--peer-count", "2", "--template")
	require.Equal(t, 0, code, errOut)
	code, _, errOut = runCLI(t, "", "-f", "extend", "--archive", "ext.tar.xz")
	require.Equal(t, 0, code, errOut)
	assert.DirExists(t, "crypto-config/peerOrganizations/org1.example.com/peers/peer1.org1.example.com")
	assert.Contains(t, archiveNamesIn(t, "ext.tar.xz"),
		"crypto-config/peerOrganizations/org1.example.com/peers/peer1.org1.example.com/tls/server.crt")
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
	for _, n := range []string{"a-b", "external", "c"} {
		assert.FileExists(t, "chaincodes/"+n+"-1.0.tar.gz")
		id, err := os.ReadFile("chaincodes/" + n + "-1.0.id")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(id), n+"-1.0:"))
	}
	got, err := os.ReadFile("docker-compose-ccaas.yaml")
	require.NoError(t, err)
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
	tags, err := resolveImageTags(env(nil))
	require.NoError(t, err)
	assert.Equal(t, imageTags{ImageTag, ImageTag}, tags)
	tags, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "3.1.4"}))
	require.NoError(t, err)
	assert.Equal(t, imageTags{"3.1.4", ImageTag}, tags, "Fabric 3 has no tools image")
	tags, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "3.1.4", "FNB_TOOLSIMAGETAG": "2.5.9"}))
	require.NoError(t, err)
	assert.Equal(t, imageTags{"3.1.4", "2.5.9"}, tags)
	_, err = resolveImageTags(env(map[string]string{"FNB_IMAGETAG": "2.4.9"}))
	require.ErrorContains(t, err, "2.5 and later")
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

// The CCaaS services read their package ID from CCID_<NAME>. The builder
// takes it from the chaincodes/<name>-<version>.id files that generatecc
// writes, so a consumer needs no scripts/env.sh or .env file.
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
