package certs

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests build real cryptogen-style trees on disk. They are the Go port
// of the Python builder's tests/test_reissue.py, so each behaviour the old
// tool promised is still pinned.

type keyCert struct {
	key  crypto.Signer
	cert *x509.Certificate
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return k
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	s, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)
	return s
}

func mkCA(t *testing.T, cn string, notAfter time.Time, key crypto.Signer) keyCert {
	t.Helper()
	if key == nil {
		key = newKey(t)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notAfter.Add(-3650 * 24 * time.Hour),
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	require.NoError(t, err)
	c, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return keyCert{key, c}
}

var customOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}

func mkLeaf(t *testing.T, cn, ou string, ca keyCert, notAfter time.Time, sans []string, extra []pkix.Extension) keyCert {
	t.Helper()
	key := newKey(t)
	name := pkix.Name{CommonName: cn}
	if ou != "" {
		name.OrganizationalUnit = []string{ou}
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               name,
		NotBefore:             time.Now().Add(-400 * 24 * time.Hour),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              sans,
		ExtraExtensions:       extra,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	require.NoError(t, err)
	c, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return keyCert{key, c}
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, b, 0o644))
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func keyPEM(t *testing.T, k crypto.Signer) []byte {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
}

func addCA(t *testing.T, orgDir, sub, cn string, notAfter time.Time) keyCert {
	t.Helper()
	kc := mkCA(t, cn, notAfter, nil)
	writeFile(t, filepath.Join(orgDir, sub, cn+"-cert.pem"), certPEM(kc.cert))
	writeFile(t, filepath.Join(orgDir, sub, "priv_sk"), keyPEM(t, kc.key))
	return kc
}

func addNode(t *testing.T, orgDir, parent, name, ou string, ca, tlsca keyCert, notAfter time.Time) {
	t.Helper()
	dir := filepath.Join(orgDir, parent, name)
	sc := mkLeaf(t, name, ou, ca, notAfter, nil, nil)
	writeFile(t, filepath.Join(dir, "msp", "signcerts", name+"-cert.pem"), certPEM(sc.cert))
	writeFile(t, filepath.Join(dir, "msp", "keystore", "priv_sk"), keyPEM(t, sc.key))
	tc := mkLeaf(t, name, "", tlsca, notAfter, []string{name, strings.Split(name, ".")[0], "localhost"}, nil)
	writeFile(t, filepath.Join(dir, "tls", "server.crt"), certPEM(tc.cert))
	writeFile(t, filepath.Join(dir, "tls", "server.key"), keyPEM(t, tc.key))
}

type tree struct {
	root     string
	caExpiry time.Time
	cas      map[string]keyCert
}

// buildTree makes org1 (peer0 valid, peer1 expired, peer10 valid, Admin
// valid), an orderer org (orderer0 valid), and optionally org2 (peer0 valid).
func buildTree(t *testing.T, caExpiry time.Time, secondOrg bool) tree {
	t.Helper()
	now := time.Now()
	if caExpiry.IsZero() {
		caExpiry = now.Add(5 * 365 * 24 * time.Hour)
	}
	valid := now.Add(4 * 365 * 24 * time.Hour)
	expired := now.Add(-24 * time.Hour)
	root := filepath.Join(t.TempDir(), "crypto-config")
	tr := tree{root: root, caExpiry: caExpiry, cas: map[string]keyCert{}}

	org1 := filepath.Join(root, "peerOrganizations", "org1.example.com")
	ca1 := addCA(t, org1, "ca", "ca.org1.example.com", caExpiry)
	tca1 := addCA(t, org1, "tlsca", "tlsca.org1.example.com", caExpiry)
	addNode(t, org1, "peers", "peer0.org1.example.com", "peer", ca1, tca1, valid)
	addNode(t, org1, "peers", "peer1.org1.example.com", "peer", ca1, tca1, expired)
	addNode(t, org1, "peers", "peer10.org1.example.com", "peer", ca1, tca1, valid)
	addNode(t, org1, "users", "Admin@org1.example.com", "admin", ca1, tca1, valid)
	tr.cas["org1"] = ca1

	oorg := filepath.Join(root, "ordererOrganizations", "example.com")
	oca := addCA(t, oorg, "ca", "ca.example.com", caExpiry)
	otca := addCA(t, oorg, "tlsca", "tlsca.example.com", caExpiry)
	addNode(t, oorg, "orderers", "orderer0.example.com", "orderer", oca, otca, valid)

	if secondOrg {
		org2 := filepath.Join(root, "peerOrganizations", "org2.example.com")
		ca2 := addCA(t, org2, "ca", "ca.org2.example.com", caExpiry)
		tca2 := addCA(t, org2, "tlsca", "tlsca.org2.example.com", caExpiry)
		addNode(t, org2, "peers", "peer0.org2.example.com", "peer", ca2, tca2, valid)
		tr.cas["org2"] = ca2
	}
	return tr
}

func (tr tree) signcert(org, node string) string {
	return filepath.Join(tr.root, "peerOrganizations", org, "peers", node, "msp", "signcerts", node+"-cert.pem")
}

func readCert(t *testing.T, p string) *x509.Certificate {
	t.Helper()
	c, err := loadCert(p)
	require.NoError(t, err)
	return c
}

func snapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		m[p] = b
		return err
	}))
	return m
}

func backups(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for p := range snapshot(t, root) {
		if strings.HasSuffix(p, ".bak") {
			out = append(out, p)
		}
	}
	return out
}

func run(t *testing.T, o ReissueOptions) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := Reissue(o, &buf)
	return buf.String(), err
}

func TestDiscoverFindsPeersOrderersAndUsers(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	leaves, err := Discover(tr.root, []Kind{SignCert, TLS})
	require.NoError(t, err)
	found := map[string]bool{}
	for _, l := range leaves {
		found[l.Node+"/"+string(l.Kind)] = true
	}
	for _, k := range []string{"peer0.org1.example.com/signcert", "peer0.org1.example.com/tls",
		"orderer0.example.com/tls", "Admin@org1.example.com/signcert"} {
		assert.True(t, found[k], k)
	}
	only, err := Discover(tr.root, []Kind{SignCert})
	require.NoError(t, err)
	for _, l := range only {
		assert.Equal(t, SignCert, l.Kind)
	}
}

func TestExpiriesListsEachCertOnceWithItsExpiry(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	ca := filepath.Join(tr.root, "peerOrganizations", "org1.example.com", "ca", "ca.org1.example.com-cert.pem")
	deeper := filepath.Join(tr.root, "peerOrganizations", "org1.example.com", "msp", "cacerts", "deeper", "ca.org1.example.com-cert.pem")
	b, err := os.ReadFile(ca)
	require.NoError(t, err)
	writeFile(t, deeper, b)

	out, err := Expiries(tr.root)
	require.NoError(t, err)
	paths := map[string]string{}
	for _, e := range out {
		_, dup := paths[e.Path]
		assert.False(t, dup, e.Path)
		paths[e.Path] = e.Expiry
	}
	assert.Contains(t, paths, ca)
	assert.NotContains(t, paths, deeper)
	assert.Equal(t, tr.caExpiry.UTC().Format(TimeFormat), paths[ca])
	for i := 1; i < len(out); i++ {
		a, b := out[i-1], out[i]
		assert.True(t, a.Expiry < b.Expiry || (a.Expiry == b.Expiry && a.Path < b.Path), "sorted")
	}
}

func TestReissuePreservesIdentityAndHonorsValidity(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	p := tr.signcert("org1.example.com", "peer1.org1.example.com")
	old := readCert(t, p)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}})
	require.NoError(t, err)
	n := readCert(t, p)
	assert.Equal(t, old.RawSubject, n.RawSubject)
	assert.Contains(t, n.Subject.String(), "OU=peer")
	assert.Equal(t, old.RawSubjectPublicKeyInfo, n.RawSubjectPublicKeyInfo)
	assert.Equal(t, tr.cas["org1"].cert.RawSubject, n.RawIssuer)
	require.NoError(t, n.CheckSignatureFrom(tr.cas["org1"].cert))
	assert.Equal(t, tr.caExpiry.UTC().Truncate(time.Second), n.NotAfter.UTC())
	assert.NotEqual(t, 0, old.SerialNumber.Cmp(n.SerialNumber))
	bak, err := os.ReadFile(p + ".bak")
	require.NoError(t, err)
	assert.Equal(t, certPEM(old), bak)
}

func TestReissuePreservesExtensionsAndCriticality(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	// A leaf with an unknown critical extension: its raw bytes and flag must
	// come through.
	org := filepath.Join(tr.root, "peerOrganizations", "org1.example.com")
	payload := []byte{0x04, 0x03, 'x', 'y', 'z'}
	leaf := mkLeaf(t, "peer1.org1.example.com", "peer", tr.cas["org1"], time.Now().Add(-time.Hour), nil,
		[]pkix.Extension{{Id: customOID, Critical: true, Value: payload}})
	p := filepath.Join(org, "peers", "peer1.org1.example.com", "msp", "signcerts", "peer1.org1.example.com-cert.pem")
	writeFile(t, p, certPEM(leaf.cert))
	old := readCert(t, p)

	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, Type: "signcert"})
	require.NoError(t, err)
	n := readCert(t, p)
	type ext struct {
		id       string
		critical bool
		value    string
	}
	set := func(c *x509.Certificate) []ext {
		var s []ext
		for _, e := range c.Extensions {
			s = append(s, ext{e.Id.String(), e.Critical, string(e.Value)})
		}
		return s
	}
	assert.Equal(t, set(old), set(n))
	assert.NotEqual(t, x509.KeyUsage(0), n.KeyUsage)
}

func TestReissueKeepsTLSSANs(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	p := filepath.Join(tr.root, "peerOrganizations", "org1.example.com", "peers", "peer1.org1.example.com", "tls", "server.crt")
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, Type: "tls"})
	require.NoError(t, err)
	n := readCert(t, p)
	assert.Contains(t, n.DNSNames, "peer1.org1.example.com")
	assert.Contains(t, n.DNSNames, "localhost")
}

func TestEd25519CASigns(t *testing.T) {
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ca := mkCA(t, "ca.ed.example.com", time.Now().Add(365*24*time.Hour), edKey)
	leaf := mkLeaf(t, "peer0.ed.example.com", "peer", ca, time.Now().Add(-time.Hour), nil, nil)
	b, err := Rebuild(leaf.cert, ca.cert, ca.key, ca.cert.NotAfter)
	require.NoError(t, err)
	c, err := parseCert(b)
	require.NoError(t, err)
	assert.Equal(t, x509.PureEd25519, c.SignatureAlgorithm)
	require.NoError(t, c.CheckSignatureFrom(ca.cert))
}

func TestAllExpiredTouchesOnlyExpired(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	before := snapshot(t, tr.root)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, AllExpired: true})
	require.NoError(t, err)
	after := snapshot(t, tr.root)
	for p, b := range before {
		changed := !bytes.Equal(b, after[p])
		isPeer1 := strings.Contains(p, "peer1.org1.example.com/msp/signcerts") || strings.Contains(p, "peer1.org1.example.com/tls/server.crt")
		assert.Equal(t, isPeer1, changed, p)
	}
	n := readCert(t, tr.signcert("org1.example.com", "peer1.org1.example.com"))
	assert.True(t, n.NotAfter.After(time.Now()))
}

func TestShortNodeNameDoesNotMatchPeer10(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	p1 := tr.signcert("org1.example.com", "peer1.org1.example.com")
	p10 := tr.signcert("org1.example.com", "peer10.org1.example.com")
	b1, _ := os.ReadFile(p1)
	b10, _ := os.ReadFile(p10)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}})
	require.NoError(t, err)
	a1, _ := os.ReadFile(p1)
	a10, _ := os.ReadFile(p10)
	assert.NotEqual(t, b1, a1)
	assert.Equal(t, b10, a10)
}

func TestOrdererTLSAndCrossOrgCA(t *testing.T) {
	tr := buildTree(t, time.Time{}, true)
	tls := filepath.Join(tr.root, "ordererOrganizations", "example.com", "orderers", "orderer0.example.com", "tls", "server.crt")
	before, _ := os.ReadFile(tls)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"orderer0", "peer0.org2.example.com"}})
	require.NoError(t, err)
	after, _ := os.ReadFile(tls)
	assert.NotEqual(t, before, after)
	n := readCert(t, tr.signcert("org2.example.com", "peer0.org2.example.com"))
	assert.Equal(t, tr.cas["org2"].cert.RawSubject, n.RawIssuer)
	require.NoError(t, n.CheckSignatureFrom(tr.cas["org2"].cert))
}

func TestDaysBelowCapAndCapAtCAExpiry(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	p := tr.signcert("org1.example.com", "peer1.org1.example.com")
	now := time.Now()
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, Type: "signcert", Days: 30, DaysSet: true})
	require.NoError(t, err)
	n := readCert(t, p)
	assert.True(t, n.NotAfter.After(now.Add(29*24*time.Hour)))
	assert.True(t, n.NotAfter.Before(now.Add(31*24*time.Hour)))

	out, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, Type: "signcert", Days: 100000, DaysSet: true})
	require.NoError(t, err)
	assert.Contains(t, out, "capped at CA expiry")
	n = readCert(t, p)
	assert.Equal(t, tr.caExpiry.UTC().Truncate(time.Second), n.NotAfter.UTC())
}

func TestDryRunWritesNothing(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	before := snapshot(t, tr.root)
	out, err := run(t, ReissueOptions{CryptoConfig: tr.root, All: true, DryRun: true})
	require.NoError(t, err)
	assert.Contains(t, out, "DRY-RUN")
	assert.Equal(t, before, snapshot(t, tr.root))
}

func TestNoBackupFlag(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	p := tr.signcert("org1.example.com", "peer1.org1.example.com")
	before, _ := os.ReadFile(p)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, NoBackup: true})
	require.NoError(t, err)
	after, _ := os.ReadFile(p)
	assert.NotEqual(t, before, after)
	assert.NoFileExists(t, p+".bak")
}

func TestReissueTwiceKeepsOriginalBackup(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	p := tr.signcert("org1.example.com", "peer1.org1.example.com")
	original, _ := os.ReadFile(p)
	for range 2 {
		_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, Type: "signcert"})
		require.NoError(t, err)
	}
	bak, _ := os.ReadFile(p + ".bak")
	assert.Equal(t, original, bak)
	assert.GreaterOrEqual(t, len(backups(t, tr.root)), 2)
}

func TestRefusesWhenCAExpired(t *testing.T) {
	tr := buildTree(t, time.Now().Add(-time.Hour), false)
	before := snapshot(t, tr.root)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}})
	require.ErrorContains(t, err, "could not be reissued")
	assert.Equal(t, before, snapshot(t, tr.root))
	assert.Empty(t, backups(t, tr.root))
}

func TestRefusesWhenCAKeyMissing(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	require.NoError(t, os.Remove(filepath.Join(tr.root, "peerOrganizations", "org1.example.com", "ca", "priv_sk")))
	p := tr.signcert("org1.example.com", "peer1.org1.example.com")
	before, _ := os.ReadFile(p)
	out, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer1"}, Type: "signcert"})
	require.ErrorContains(t, err, "could not be reissued")
	assert.Contains(t, out, "fabric-ca-client reenroll")
	after, _ := os.ReadFile(p)
	assert.Equal(t, before, after)
	assert.NoFileExists(t, p+".bak")
}

func TestRejectsNonPositiveDays(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	for _, d := range []int{0, -5} {
		_, err := run(t, ReissueOptions{CryptoConfig: tr.root, All: true, Days: d, DaysSet: true})
		require.ErrorContains(t, err, "days")
	}
}

func TestUnknownNodeErrorsAndWritesNothing(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	before := snapshot(t, tr.root)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer9"}})
	require.ErrorContains(t, err, "no nodes matched")
	assert.Equal(t, before, snapshot(t, tr.root))
}

func TestMixedSuccessAndFailure(t *testing.T) {
	tr := buildTree(t, time.Time{}, true)
	require.NoError(t, os.Remove(filepath.Join(tr.root, "peerOrganizations", "org2.example.com", "ca", "priv_sk")))
	good := tr.signcert("org1.example.com", "peer0.org1.example.com")
	bad := tr.signcert("org2.example.com", "peer0.org2.example.com")
	gb, _ := os.ReadFile(good)
	bb, _ := os.ReadFile(bad)
	_, err := run(t, ReissueOptions{CryptoConfig: tr.root, Nodes: []string{"peer0"}, Type: "signcert"})
	require.Error(t, err)
	ga, _ := os.ReadFile(good)
	ba, _ := os.ReadFile(bad)
	assert.NotEqual(t, gb, ga)
	assert.Equal(t, bb, ba)
}

func TestNoSelectionOnlyLists(t *testing.T) {
	tr := buildTree(t, time.Time{}, false)
	before := snapshot(t, tr.root)
	out, err := run(t, ReissueOptions{CryptoConfig: tr.root})
	require.NoError(t, err)
	assert.Contains(t, out, "No targets selected")
	assert.Contains(t, out, "EXPIRED")
	assert.Equal(t, before, snapshot(t, tr.root))
}

func TestUniqueNonClobberingBackups(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cert.pem")
	writeFile(t, p, []byte("x"))
	seen := map[string]bool{}
	var first string
	for i := range 3 {
		b := BackupPath(p)
		assert.NoFileExists(t, b)
		assert.True(t, strings.HasSuffix(b, ".bak"))
		if i == 0 {
			first = b
		}
		seen[b] = true
		writeFile(t, b, []byte("bak"))
	}
	assert.Equal(t, p+".bak", first)
	assert.Len(t, seen, 3)
}

// The Fabric MSP checks a cert's chain at the cert's NotBefore plus one
// second, not at the current time. cryptogen dates a CA from the current
// minute minus 5 minutes, so a leaf reissued soon after generate, dated now
// minus 5 minutes, started before its CA, and the peer refused its own
// identity at start ("certificate has expired or is not yet valid").
func TestRebuildStartsNoEarlierThanItsCA(t *testing.T) {
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "ca.org1.example.com"},
		NotBefore:             time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		NotAfter:              time.Now().Add(3650 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	ca := keyCert{key, caCert}
	leaf := mkLeaf(t, "peer0.org1.example.com", "peer", ca, time.Now().Add(24*time.Hour), nil, nil)

	b, err := Rebuild(leaf.cert, ca.cert, ca.key, time.Now().Add(30*24*time.Hour))
	require.NoError(t, err)
	c, err := parseCert(b)
	require.NoError(t, err)
	assert.False(t, c.NotBefore.Before(ca.cert.NotBefore), "leaf NotBefore %s is before CA NotBefore %s", c.NotBefore, ca.cert.NotBefore)
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	_, err = c.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: c.NotBefore.Add(time.Second),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	require.NoError(t, err)
}
