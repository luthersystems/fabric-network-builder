// Package chaincode builds Fabric chaincode packages, the files that
// `peer lifecycle chaincode install` takes, and computes their package IDs.
//
// It replaces scripts/generatecc.sh and the generateChaincode function of
// scripts/luther_utils.sh, which needed GNU tar and the peer binary.
package chaincode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mtime is the time of every tar entry, so the same inputs give the same
// package bytes and the same package ID. The shell version used it too.
var mtime = time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)

// Label is the package label of a chaincode: <name>-<version>. install
// finds the package and the installed chaincode by it.
func Label(name, version string) string { return name + "-" + version }

// PackageID is the ID the peer gives a package: <label>:<sha256 of the
// package file>.
func PackageID(label string, pkg []byte) string {
	sum := sha256.Sum256(pkg)
	return label + ":" + hex.EncodeToString(sum[:])
}

// connectionJSON is the CCaaS connection.json. The peer fills
// {{.index}}, so each peer reaches the chaincode service of its own index.
// The text is the shell version's, byte for byte.
func connectionJSON(name string) []byte {
	return []byte(`  {
    "address": "` + name + `-peer{{.index}}:8080",
    "dial_timeout": "10s",
    "tls_required": false,
    "client_auth_required": false
  }
`)
}

type entry struct {
	name string
	body []byte
}

// tarGz writes entries as a gzipped tar with fixed times and owners.
func tarGz(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = mtime
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o644,
			Size:     int64(len(e.body)),
			ModTime:  mtime,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// codeFromArchive returns the code.tar.gz entry of a chaincode archive
// (a tar, gzipped or not). The shell version extracted the whole archive
// and packaged its code.tar.gz.
func codeFromArchive(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied chaincode path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: no code.tar.gz in the archive", path)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if filepath.Clean(strings.TrimPrefix(hdr.Name, "./")) == "code.tar.gz" {
			return io.ReadAll(io.LimitReader(tr, 1<<30))
		}
	}
}

// Package is one built chaincode package.
type Package struct {
	Label string
	// Path is where the package is written: <dir of the source>/<label>.tar.gz.
	Path string
	// IDPath holds the package ID: <dir of the source>/<label>.id.
	IDPath string
	ID     string
	Bytes  []byte
}

// Build makes the package of chaincode name at version. A CCaaS package
// holds only a connection.json that points the peers at the
// <name>-peer<i>:8080 service. A Go package holds the code.tar.gz of the
// archive at source.
func Build(source, name, version string, ccaas bool) (*Package, error) {
	label := Label(name, version)
	typ := "golang"
	var code []byte
	var err error
	if ccaas {
		typ = "ccaas"
		code, err = tarGz([]entry{{"connection.json", connectionJSON(name)}})
	} else {
		code, err = codeFromArchive(source)
	}
	if err != nil {
		return nil, err
	}
	meta, err := json.Marshal(struct {
		Path  string `json:"path"`
		Type  string `json:"type"`
		Label string `json:"label"`
	}{"main", typ, label})
	if err != nil {
		return nil, err
	}
	pkg, err := tarGz([]entry{{"metadata.json", append(meta, '\n')}, {"code.tar.gz", code}})
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(source)
	return &Package{
		Label:  label,
		Path:   filepath.Join(dir, label+".tar.gz"),
		IDPath: filepath.Join(dir, label+".id"),
		ID:     PackageID(label, pkg),
		Bytes:  pkg,
	}, nil
}

// Write writes the package and its .id file (the ID and a newline, as
// `peer lifecycle chaincode calculatepackageid | tee` wrote it).
func (p *Package) Write() error {
	if err := os.WriteFile(p.Path, p.Bytes, 0o644); err != nil { //nolint:gosec // peers read the package
		return err
	}
	return os.WriteFile(p.IDPath, []byte(p.ID+"\n"), 0o644) //nolint:gosec // consumers' env.sh reads it
}
