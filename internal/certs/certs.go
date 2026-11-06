// Package certs lists the expiry dates of a network's certificates and
// renews (re-signs) expiring leaf certificates in place.
//
// Renewal exists because a local or test network outlives its cryptogen
// certificates (one year by default) far more often than it outlives its CAs
// (ten years). Re-signing the leaf with the CA key that cryptogen left on
// disk, with the same subject, public key and extensions, keeps every MSP
// identity unchanged, so the channel config and the ledger stay valid and no
// node needs to re-enroll.
package certs

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/md5" //nolint:gosec // groups identical files; not a security use
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TimeFormat is the format of every date this package prints.
const TimeFormat = "2006-01-02T15:04:05Z"

// Expiry is one certificate file and its expiry.
type Expiry struct {
	Expiry string
	Path   string
}

// Expiries lists the expiry of each certificate (*.pem and *.crt) under
// root, sorted by expiry then path. cryptogen writes the same certificate in
// several places (an org's CA cert is also in each node's msp/cacerts), so
// for each set of identical files it lists only the paths with the fewest
// components.
func Expiries(root string) ([]Expiry, error) {
	type group struct {
		expiry string
		paths  []string
	}
	groups := map[string]*group{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(p, ".pem") && !strings.HasSuffix(p, ".crt")) {
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // walked from the operator's crypto-config tree
		if err != nil {
			return err
		}
		sum := md5.Sum(b) //nolint:gosec // see the import
		key := hex.EncodeToString(sum[:])
		g, ok := groups[key]
		if !ok {
			c, err := parseCert(b)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			g = &group{expiry: c.NotAfter.UTC().Format(TimeFormat)}
			groups[key] = g
		}
		g.paths = append(g.paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []Expiry
	for _, g := range groups {
		depth := func(p string) int { return len(strings.Split(filepath.Clean(p), string(filepath.Separator))) }
		least := -1
		for _, p := range g.paths {
			if d := depth(p); least < 0 || d < least {
				least = d
			}
		}
		for _, p := range g.paths {
			if depth(p) == least {
				out = append(out, Expiry{Expiry: g.expiry, Path: p})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Expiry != out[j].Expiry {
			return out[i].Expiry < out[j].Expiry
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func parseCert(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("no PEM data")
	}
	return x509.ParseCertificate(block.Bytes)
}

func loadCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path) //nolint:gosec // paths come from the operator's crypto-config tree
	if err != nil {
		return nil, err
	}
	c, err := parseCert(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func loadKey(path string) (crypto.Signer, error) {
	b, err := os.ReadFile(path) //nolint:gosec // see loadCert
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("no PEM data")
	}
	var key any
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	s, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("unsupported key type %T", key)
	}
	return s, nil
}

// Kind is a kind of leaf certificate.
type Kind string

// The leaf kinds of a cryptogen node.
const (
	SignCert Kind = "signcert"
	TLS      Kind = "tls"
)

type leafSpec struct {
	certGlob string
	keyGlob  string
	caSubdir string
}

var leafSpecs = map[Kind]leafSpec{
	SignCert: {"msp/signcerts/*.pem", "msp/keystore/*_sk", "ca"},
	TLS:      {"tls/server.crt", "tls/server.key", "tlsca"},
}

// nodeParents are the directories under an org that hold one directory per
// node with leaf material.
var nodeParents = []string{"peers", "orderers", "users"}

// Leaf is one leaf certificate of a node.
type Leaf struct {
	Cert     *x509.Certificate
	Expiry   time.Time
	Node     string
	Org      string
	Kind     Kind
	CertPath string
	CADir    string
}

// Discover walks a cryptogen-style crypto-config tree and returns one Leaf
// per node and kind found.
func Discover(cryptoConfig string, kinds []Kind) ([]*Leaf, error) {
	var out []*Leaf
	for _, parent := range nodeParents {
		dirs, err := filepath.Glob(filepath.Join(cryptoConfig, "*Organizations", "*", parent, "*"))
		if err != nil {
			return nil, err
		}
		sort.Strings(dirs)
		for _, nodeDir := range dirs {
			if fi, err := os.Stat(nodeDir); err != nil || !fi.IsDir() {
				continue
			}
			orgDir := filepath.Dir(filepath.Dir(nodeDir))
			for _, kind := range kinds {
				spec := leafSpecs[kind]
				certs, err := filepath.Glob(filepath.Join(nodeDir, spec.certGlob))
				if err != nil {
					return nil, err
				}
				if len(certs) == 0 {
					continue
				}
				sort.Strings(certs)
				c, err := loadCert(certs[0])
				if err != nil {
					return nil, err
				}
				out = append(out, &Leaf{
					Node:     filepath.Base(nodeDir),
					Org:      filepath.Base(orgDir),
					Kind:     kind,
					CertPath: certs[0],
					Cert:     c,
					CADir:    filepath.Join(orgDir, spec.caSubdir),
					Expiry:   c.NotAfter.UTC(),
				})
			}
		}
	}
	return out, nil
}

// ErrReissue is returned when a leaf cannot be reissued safely.
var ErrReissue = errors.New("cannot reissue")

func reissueErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrReissue, fmt.Sprintf(format, args...))
}

func publicKeyDER(pub any) ([]byte, error) { return x509.MarshalPKIXPublicKey(pub) }

// CA is an issuing CA: its certificate and private key.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
}

// ResolveCA finds the CA certificate in caDir that issued leaf (matched by
// subject) and the private key that pairs with it. It fails when the key is
// not there, which is the case for fabric-ca issued material: that must be
// renewed with fabric-ca-client reenroll.
func ResolveCA(caDir string, leaf *x509.Certificate) (*CA, error) {
	if fi, err := os.Stat(caDir); err != nil || !fi.IsDir() {
		return nil, reissueErrorf("CA directory not found: %s", caDir)
	}
	pems, err := filepath.Glob(filepath.Join(caDir, "*.pem"))
	if err != nil {
		return nil, err
	}
	sort.Strings(pems)
	var ca *x509.Certificate
	for _, p := range pems {
		c, lerr := loadCert(p)
		if lerr != nil {
			return nil, lerr
		}
		if bytes.Equal(c.RawSubject, leaf.RawIssuer) {
			ca = c
			break
		}
	}
	if ca == nil {
		return nil, reissueErrorf("no CA certificate in %s matches issuer %s", caDir, leaf.Issuer)
	}
	caPub, err := publicKeyDER(ca.PublicKey)
	if err != nil {
		return nil, err
	}
	keys, err := filepath.Glob(filepath.Join(caDir, "*_sk"))
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	for _, k := range keys {
		key, err := loadKey(k)
		if err != nil {
			continue
		}
		pub, err := publicKeyDER(key.Public())
		if err == nil && bytes.Equal(pub, caPub) {
			return &CA{Cert: ca, Key: key}, nil
		}
	}
	return nil, reissueErrorf("no CA private key (*_sk) in %s pairs with the CA certificate - "+
		"the CA key is not on this filesystem (fabric-ca material must use fabric-ca-client reenroll)", caDir)
}

// signatureAlgorithm signs with SHA-256 for every key but Ed25519, as the
// Python builder did. Go would otherwise pick SHA-384 for a P-384 CA.
func signatureAlgorithm(key crypto.Signer) (x509.SignatureAlgorithm, error) {
	switch key.Public().(type) {
	case *ecdsa.PublicKey:
		return x509.ECDSAWithSHA256, nil
	case *rsa.PublicKey:
		return x509.SHA256WithRSA, nil
	case ed25519.PublicKey:
		return x509.PureEd25519, nil
	default:
		return 0, fmt.Errorf("unsupported CA key type %T", key.Public())
	}
}

// Rebuild returns PEM bytes for old re-dated to notAfter and re-signed by
// caKey. The subject, the public key and every extension (with its critical
// flag) are copied verbatim; only the validity and the serial number change,
// so the identity is the same.
func Rebuild(old, ca *x509.Certificate, caKey crypto.Signer, notAfter time.Time) ([]byte, error) {
	alg, err := signatureAlgorithm(caKey)
	if err != nil {
		return nil, err
	}
	// A positive 159-bit serial, the size Python's random_serial_number
	// produces, under the 20-byte limit of RFC 5280.
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 159))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		RawSubject:         old.RawSubject,
		NotBefore:          time.Now().UTC().Add(-5 * time.Minute),
		NotAfter:           notAfter,
		SignatureAlgorithm: alg,
		// ExtraExtensions replace any extension Go would derive, so the
		// old extensions come through unchanged.
		ExtraExtensions: old.Extensions,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, old.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// BackupPath is where a replaced certificate is saved: <cert>.bak, or, to
// keep an earlier backup, <cert>.<timestamp>[-n].bak.
func BackupPath(certPath string) string {
	bak := certPath + ".bak"
	if _, err := os.Stat(bak); errors.Is(err, fs.ErrNotExist) {
		return bak
	}
	ts := time.Now().UTC().Format("20060102150405")
	bak = fmt.Sprintf("%s.%s.bak", certPath, ts)
	for n := 1; ; n++ {
		if _, err := os.Stat(bak); errors.Is(err, fs.ErrNotExist) {
			return bak
		}
		bak = fmt.Sprintf("%s.%s-%d.bak", certPath, ts, n)
	}
}
