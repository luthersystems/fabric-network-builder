package cli

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

// ansibleVars writes the variables that the mars Kubernetes roles read the
// network from, as the mars sandbox template's fabric/fnb-gen.sh wrote them
// with zip and base64:
//
//	k8s_fabric_channel_artifacts  base64 zip of channel-artifacts/
//	k8s_fabric_collections        base64 collections.json
//	k8s_fabric_crypto_config      base64 zip of crypto-config/
//
// The values hold the network's private keys, so the file is not world
// readable.
func (e *env) ansibleVars() error {
	if src := e.v.str("generate_from", ""); src != "" {
		if err := e.generateFrom(src); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	for _, d := range []struct{ key, path string }{
		{"k8s_fabric_channel_artifacts", "channel-artifacts"},
		{"k8s_fabric_collections", "collections.json"},
		{"k8s_fabric_crypto_config", "crypto-config"},
	} {
		var b []byte
		var err error
		if d.path == "collections.json" {
			b, err = os.ReadFile(e.path(d.path))
		} else {
			b, err = zipDir(e.dir, d.path)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", d.key, err)
		}
		fmt.Fprintf(&buf, "%s: %s\n\n", d.key, base64.StdEncoding.EncodeToString(b))
	}
	out := e.v.str("output", "")
	if out == "" {
		_, err := io.Copy(e.out, &buf)
		return err
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return e.owner.chown(out)
}

// generateFrom generates the network as fnb-gen.sh did: sized by the
// Ansible env file, skipped when crypto-config exists unless --force.
func (e *env) generateFrom(src string) error {
	if exists(e.path("crypto-config")) && !e.force {
		e.printf("crypto-config exists; keeping it (use --force to generate a new network)\n")
		return nil
	}
	b, err := os.ReadFile(src) //nolint:gosec // a file the caller names
	if err != nil {
		return err
	}
	o := topology.DefaultOptions()
	for _, f := range []struct {
		n   *int
		key string
	}{
		{&o.OrgCount, "num_orgs"},
		{&o.PeerCount, "num_peers"},
		{&o.OrdererCount, "num_orderers"},
	} {
		s, ferr := ansibleField(b, src, f.key)
		if ferr != nil {
			return ferr
		}
		if *f.n, ferr = strconv.Atoi(s); ferr != nil {
			return fmt.Errorf("%s: %s: %w", src, f.key, ferr)
		}
	}
	if o.DomainName, err = ansibleField(b, src, "k8s_fabric_network_domain_root"); err != nil {
		return err
	}
	n, err := topology.New(o)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	// fnb-gen.sh always ran `generate --force`: the rendered files of a
	// network directory are replaced.
	e.force = true
	return e.doGenerate(n)
}

// ansibleField reads a top-level scalar from an Ansible vars file the way
// the template's mustGetAnsibleField did: the first line that starts with
// "key:". It does not parse the whole file as YAML, because an Ansible
// value may be a Jinja expression that is not valid YAML.
func ansibleField(b []byte, src, key string) (string, error) {
	for line := range strings.Lines(string(b)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), key+":")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if v == "" {
			break
		}
		return v, nil
	}
	return "", fmt.Errorf("%s: required field %q is missing or empty", src, key)
}

// zipDir zips dir (relative to root) as `zip -r - dir` does: entries named
// from root, a directory entry for each directory, the files' modes and
// times kept.
func zipDir(root, dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(p) //nolint:gosec // a file of the network project
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
