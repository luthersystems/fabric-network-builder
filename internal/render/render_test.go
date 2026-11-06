package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

// The goldens under testdata/golden were rendered by the Python builder
// (network.py generate --template, at the commit before the Go rewrite) with
// the options below. Consumers commit these files, so the Go templates must
// reproduce them byte for byte. Do not regenerate them from the Go code: a
// diff here is a change every consumer sees.
func goldenOptions(t *testing.T) map[string]topology.Options {
	t.Helper()
	opts := func(f func(*topology.Options)) topology.Options {
		o := topology.DefaultOptions()
		f(&o)
		return o
	}
	return map[string]topology.Options{
		"default": topology.DefaultOptions(),
		"luther": opts(func(o *topology.Options) {
			o.DomainName = "luther.systems"
			o.CCName = "smoke"
		}),
		"big": opts(func(o *topology.Options) {
			o.DomainName = "example.org"
			o.OrgCount = 3
			o.PeerCount = 1
			o.OrdererCount = 3
			o.OrdererSANDomains = []string{"a.example", "b.example"}
			o.PeerSANDomains = []string{"org{i}.x.example"}
			o.EnableNodeOUs = true
			o.MinEndorsers = -1
			o.ExecuteTimeout = 120
			o.ConnectDomainName = "svc.cluster.local"
		}),
		"nchoose2": opts(func(o *topology.Options) {
			o.PrivateStructure = "nchoose2"
			o.OrgCount = 3
			o.ReqPeerCount = 1
			o.MaxPeerCount = 3
		}),
		"nchoose2common": opts(func(o *topology.Options) {
			o.PrivateStructure = "nchoose2common,alpha,beta,gamma,delta"
			o.OrgCount = 4
			o.PeerCount = 1
			o.MinEndorsers = 3
		}),
	}
}

func TestNetworkMatchesPythonGoldens(t *testing.T) {
	for name, o := range goldenOptions(t) {
		t.Run(name, func(t *testing.T) {
			n, err := topology.New(o)
			require.NoError(t, err)
			files, err := Network(n)
			require.NoError(t, err)
			dir := t.TempDir()
			require.NoError(t, Write(dir, files, nil))

			golden := filepath.Join("testdata", "golden", name)
			var want []string
			require.NoError(t, filepath.WalkDir(golden, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(golden, p)
				want = append(want, rel)
				return err
			}))
			require.Len(t, files, len(want), "file count")
			for _, rel := range want {
				exp, err := os.ReadFile(filepath.Join(golden, rel))
				require.NoError(t, err)
				got, err := os.ReadFile(filepath.Join(dir, rel))
				require.NoError(t, err, rel)
				assert.Equal(t, string(exp), string(got), rel)
				info, err := os.Stat(filepath.Join(dir, rel))
				require.NoError(t, err)
				ginfo, err := os.Stat(filepath.Join(golden, rel))
				require.NoError(t, err)
				assert.Equal(t, ginfo.Mode().Perm()&0o100, info.Mode().Perm()&0o100, "executable bit of %s", rel)
			}
			for _, d := range Dirs {
				assert.DirExists(t, filepath.Join(dir, d))
			}
		})
	}
}

func TestCCaaSComposeMatchesPythonGoldens(t *testing.T) {
	cases := map[string][]CCaaSService{
		"one": {{ServiceName: "substrate-peer0", CCIDEnvVar: "CCID_SUBSTRATE", Port: 9080, Image: "luthersystems/substrate:$CHAINCODE_VERSION"}},
		"three": {
			{ServiceName: "a-b-peer0", CCIDEnvVar: "CCID_A-B", Port: 9080, Image: "luthersystems/substrate:$CHAINCODE_VERSION"},
			{ServiceName: "external-peer0", CCIDEnvVar: "CCID_EXTERNAL", Port: 9081, Image: "foo/bar:1.2"},
			{ServiceName: "c-peer0", CCIDEnvVar: "CCID_C", Port: 9082, Image: "luthersystems/substrate:$CHAINCODE_VERSION"},
		},
	}
	for name, services := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := CCaaSCompose(services)
			require.NoError(t, err)
			want, err := os.ReadFile(filepath.Join("testdata", "ccaas", name, "docker-compose-ccaas.yaml"))
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got))
		})
	}
}
