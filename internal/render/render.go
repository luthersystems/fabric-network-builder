// Package render writes the network template: the config files, compose
// files and scripts that `generate` puts in the project directory.
//
// The templates are Go text/template ports of the Python builder's Jinja2
// templates. Consumers commit the rendered files, and substrate edits
// docker-compose-ccaas.yaml after each generatecc, so the output must stay
// byte-identical to the Python builder's: the golden tests in this package
// were rendered by it. The template data keeps the Jinja variable names
// (DOMAIN_NAME, ORG_INDICES, ...) so a template diff against the old .j2
// file stays readable.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

//go:embed templates
var templates embed.FS

// Dirs are the directories the template creates, in creation order.
var Dirs = []string{"base", "chaincodes", "couchdb", "scripts"}

// Rendered are the templated files, in the Python builder's order.
var Rendered = []string{
	"crypto-config.yaml", "configtx.yaml",
	"shiroclient.yaml", "shiroclient_fast.yaml",
	"fabric-client.yaml", "fabric-client_fast.yaml", "fabric-client_template.yaml",
	"docker-compose-e2e-template.yaml",
	"docker-compose-cli.yaml",
	"docker-compose-couch.yaml",
	"base/docker-compose-base.yaml",
	"scripts/variables.sh",
	"collections.json",
	"core.yaml",
}

// Copied are the files written as they are. The scripts run in the cli
// container (init.sh) or on the consumer's host (env.sh), and consumers
// call them directly, so they stay in the template although the builder no
// longer runs them itself.
var Copied = []string{
	"base/peer-base.yaml",
	"couchdb/local.ini",
	"scripts/channel.sh",
	"scripts/create_channel.sh",
	"scripts/init.sh",
	"scripts/install.sh",
	"scripts/generatecc.sh",
	"scripts/env.sh",
	"scripts/join_channel.sh",
	"scripts/luther_utils.sh",
}

// Executable are the copied files that get mode 0755. luther_utils.sh is
// sourced, not run, so the Python builder left it out of its chmod list;
// it was still executable because the template file was.
var Executable = map[string]bool{
	"scripts/channel.sh":        true,
	"scripts/create_channel.sh": true,
	"scripts/init.sh":           true,
	"scripts/install.sh":        true,
	"scripts/generatecc.sh":     true,
	"scripts/env.sh":            true,
	"scripts/join_channel.sh":   true,
	"scripts/luther_utils.sh":   true,
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
		mode := fs.FileMode(0o644)
		if Executable[name] {
			mode = 0o755
		}
		files = append(files, File{Path: name, Content: b, Mode: mode})
	}
	return files, nil
}

// CCaaSService is one chaincode-as-a-service compose service.
type CCaaSService struct {
	ServiceName string
	CCIDEnvVar  string
	Port        int
	Image       string
}

// CCaaSCompose renders docker-compose-ccaas.yaml. It has no final newline,
// as the Python builder wrote it; substrate adds one after generatecc.
func CCaaSCompose(services []CCaaSService) ([]byte, error) {
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
		if err := os.MkdirAll(p, 0o755); err != nil {
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
