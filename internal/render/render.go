// Package render writes the network template: the config files and compose
// files that `generate` puts in the project directory.
//
// The templates are Go text/template ports of the Python builder's Jinja2
// templates. Consumers commit the rendered files, and substrate edits
// docker-compose-ccaas.yaml after each generatecc, so the output must stay
// byte-identical to the Python builder's: the golden tests in this package
// were rendered by it. The template data keeps the Jinja variable names
// (DOMAIN_NAME, ORG_INDICES, ...) so a template diff against the old .j2
// file stays readable. The topology file (network.File) is the one file the
// Python builder did not write.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/luthersystems/fabric-network-builder/internal/network"
	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

//go:embed templates
var templates embed.FS

// Dirs are the directories the template creates, in creation order.
var Dirs = []string{"base", "chaincodes", "couchdb"}

// Rendered are the templated files, in the Python builder's order.
var Rendered = []string{
	"crypto-config.yaml", "configtx.yaml",
	"shiroclient.yaml", "shiroclient_fast.yaml",
	"fabric-client.yaml", "fabric-client_fast.yaml", "fabric-client_template.yaml",
	"docker-compose-e2e-template.yaml",
	"docker-compose-cli.yaml",
	"docker-compose-couch.yaml",
	"base/docker-compose-base.yaml",
	"collections.json",
	"core.yaml",
}

// Copied are the files written as they are. The v0.0.x scripts/*.sh are
// no longer written (#35): the builder's own commands replaced them.
var Copied = []string{
	"base/peer-base.yaml",
	"couchdb/local.ini",
}

var funcs = template.FuncMap{
	"join": func(items any, sep string) (string, error) {
		switch v := items.(type) {
		case []string:
			return strings.Join(v, sep), nil
		case []int:
			s := make([]string, len(v))
			for i, n := range v {
				s[i] = strconv.Itoa(n)
			}
			return strings.Join(s, sep), nil
		default:
			return "", fmt.Errorf("join: unsupported type %T", items)
		}
	},
	// sanformat fills {i} in a peer SAN domain, as the Python builder's
	// str.format(i=i) did.
	"sanformat": func(domain, i string) string { return strings.ReplaceAll(domain, "{i}", i) },
}

func load(name string) (*template.Template, error) {
	b, err := templates.ReadFile("templates/" + name + ".tmpl")
	if err != nil {
		return nil, err
	}
	return template.New(name).Funcs(funcs).Option("missingkey=error").Parse(string(b))
}

// execute renders the named template. Jinja2 drops the template's final
// newline (keep_trailing_newline is off), so the result has none either;
// the caller adds the one the Python builder added.
func execute(name string, data any) (string, error) {
	t, err := load(name)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// Data is the template data for a network.
func Data(n *topology.Network) (map[string]any, error) {
	collections, err := n.CollectionsJSON()
	if err != nil {
		return nil, err
	}
	ordererIndices := make([]int, len(n.Orderers))
	for i, o := range n.Orderers {
		ordererIndices[i] = o.Index
	}
	// Python rendered a bool with Jinja's |string filter: True or False.
	nodeOUs := "False"
	if n.EnableNodeOUs {
		nodeOUs = "True"
	}
	return map[string]any{
		"CC_NAME":                           n.CCName,
		"DOMAIN_NAME":                       n.DomainName,
		"CONNECT_DOMAIN_NAME":               n.ConnectDomainName,
		"ENABLE_NODE_OUS":                   nodeOUs,
		"ORG_COUNT":                         strconv.Itoa(n.OrgCount),
		"ORG_INDICES":                       n.OrgIndices(),
		"ZIP_ORG_INDICES_CA_PORTS":          n.Orgs,
		"PEER_COUNT":                        strconv.Itoa(n.PeerCount),
		"PEER_INDICES":                      n.PeerIndices,
		"ORDERER_COUNT":                     strconv.Itoa(n.OrdererCount),
		"ORDERER_INDICES":                   ordererIndices,
		"ZIP_ORDERER_INDICES_ORDERER_PORTS": n.Orderers,
		"ORDERERORGS_TEMPLATE_COUNT":        strconv.Itoa(n.OrdererCount),
		"ORDERERS":                          n.Orderers,
		"ORDERER_TYPE":                      n.OrdererType,
		"ORDERER_ADDRESSES":                 n.OrdererAddresses(),
		"IJBP":                              n.PeerPorts,
		"ENDORSEMENT_POLICY":                n.EndorsementPolicy,
		"COLLECTIONS_JSON":                  collections,
		"MIN_ENDORSERS":                     strconv.Itoa(n.MinEndorsers),
		"EXECUTE_TIMEOUT":                   strconv.Itoa(n.ExecuteTimeout) + "s",
		"ORDERER_SAN_DOMAINS":               n.OrdererSANDomains,
		"PEER_SAN_DOMAINS":                  n.PeerSANDomains,
	}, nil
}

// File is one rendered file.
type File struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// Network renders every file of the network template.
func Network(n *topology.Network) ([]File, error) {
	data, err := Data(n)
	if err != nil {
		return nil, err
	}
	var files []File
	for _, name := range Rendered {
		s, err := execute(name, data)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: name, Content: []byte(s + "\n"), Mode: 0o644})
	}
	for _, name := range Copied {
		b, err := templates.ReadFile("templates/" + name)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: name, Content: b, Mode: 0o644})
	}
	top, terr := topologyFile(n)
	if terr != nil {
		return nil, terr
	}
	files = append(files, File{Path: network.File, Content: top, Mode: 0o644})
	return files, nil
}

// topologyFile is the content of network.File for n.
func topologyFile(n *topology.Network) ([]byte, error) {
	v := network.Vars{DomainName: n.DomainName, EndorsementPolicy: n.EndorsementPolicy}
	for _, o := range n.Orderers {
		v.OrdererIndices = append(v.OrdererIndices, o.Index)
	}
	for _, idx := range []struct {
		dst *[]int
		src []string
	}{{&v.OrgIndices, n.OrgIndices()}, {&v.PeerIndices, n.PeerIndices}} {
		for _, s := range idx.src {
			i, err := strconv.Atoi(s)
			if err != nil {
				return nil, fmt.Errorf("index %q: %w", s, err)
			}
			*idx.dst = append(*idx.dst, i)
		}
	}
	return v.JSON()
}

// CCaaSService is one chaincode-as-a-service compose service.
type CCaaSService struct {
	ServiceName string
	// ChaincodeID is the package ID (<label>:<hash>) that the service gets
	// as its command.
	ChaincodeID string
	Image       string
	Port        int
}

// packageID is a Fabric package ID: a label of the characters Fabric
// allows, ":", and a hash. It needs no quoting in the compose file.
var packageID = regexp.MustCompile(`^[[:alnum:]][[:alnum:]_.+-]*:[[:xdigit:]]+$`)

// CCaaSCompose renders docker-compose-ccaas.yaml. It has no final newline,
// as the Python builder wrote it; substrate adds one after generatecc.
func CCaaSCompose(services []CCaaSService) ([]byte, error) {
	for _, s := range services {
		if !packageID.MatchString(s.ChaincodeID) {
			return nil, fmt.Errorf("%s: %q is not a chaincode package ID", s.ServiceName, s.ChaincodeID)
		}
	}
	s, err := execute("docker-compose-ccaas.yaml", map[string]any{"Chaincodes": services})
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// Write writes files under dir, creating Dirs first, and names each file on
// out as the Python builder did. It calls chown, when not nil, on each
// directory and file it writes.
func Write(dir string, files []File, chown func(string) error, out io.Writer) error {
	for _, d := range Dirs {
		p := filepath.Join(dir, d)
		// 0755: the node containers read these files as another user.
		if err := os.MkdirAll(p, 0o755); err != nil { //nolint:gosec // see above
			return err
		}
		if chown != nil {
			if err := chown(p); err != nil {
				return err
			}
		}
	}
	for _, f := range files {
		p := filepath.Join(dir, f.Path)
		_, _ = fmt.Fprintf(out, "rendering %s\n", f.Path)
		if err := os.WriteFile(p, f.Content, f.Mode); err != nil {
			return err
		}
		// WriteFile keeps the mode of a file that exists, so set it.
		if err := os.Chmod(p, f.Mode); err != nil {
			return err
		}
		if chown != nil {
			if err := chown(p); err != nil {
				return err
			}
		}
	}
	return nil
}
