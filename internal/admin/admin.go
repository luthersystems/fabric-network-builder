// Package admin creates the channel and installs chaincode on a running
// network: the work scripts/create_channel.sh, join_channel.sh and
// install.sh did with osnadmin and the peer CLI inside the cli container.
//
// It talks to the nodes directly: the orderers' channel participation API
// over HTTPS, and the peers over gRPC with fabric-admin-sdk. This removes
// the cli container from the builder's own flow and the copies of these
// scripts that each consumer kept.
package admin

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hyperledger/fabric-admin-sdk/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/luthersystems/fabric-network-builder/internal/network"
)

// Resolver gives the address to dial for a node's host name and port. The
// TLS server name stays the host name, so a resolver may return an IP.
type Resolver func(host string, port int) string

// Network is a running network and the material to administer it.
type Network struct {
	Out     io.Writer
	Vars    *network.Vars
	Resolve Resolver
	// Dir is the project directory.
	Dir string
	// Delay is the wait between retries.
	Delay time.Duration
	// Retries is the number of attempts of each retried step.
	Retries int
}

// New returns the Network of the project in dir.
func New(dir string, resolve Resolver, out io.Writer) (*Network, error) {
	v, err := network.ReadVars(dir)
	if err != nil {
		return nil, err
	}
	if resolve == nil {
		resolve = func(host string, port int) string { return fmt.Sprintf("%s:%d", host, port) }
	}
	return &Network{Dir: dir, Vars: v, Resolve: resolve, Out: out, Delay: 3 * time.Second, Retries: 5}, nil
}

func (n *Network) path(rel string) string { return filepath.Join(n.Dir, rel) }

func (n *Network) printf(format string, args ...any) { _, _ = fmt.Fprintf(n.Out, format, args...) }

// retry runs f until it succeeds or Retries attempts fail, waiting Delay
// between attempts. The nodes take a few seconds to listen after their
// containers start, as the shell scripts' retries allowed for.
func (n *Network) retry(ctx context.Context, what string, f func() error) error {
	var err error
	for attempt := 1; attempt <= n.Retries; attempt++ {
		if err = f(); err == nil {
			return nil
		}
		if attempt < n.Retries {
			n.printf("%s failed (attempt %d of %d), retry after %s: %v\n", what, attempt, n.Retries, n.Delay, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(n.Delay):
			}
		}
	}
	return fmt.Errorf("%s: after %d attempts: %w", what, n.Retries, err)
}

func readPool(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // project file
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s: no PEM certificate", path)
	}
	return pool, nil
}

func clientCert(dir string) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(filepath.Join(dir, "tls", "client.crt"), filepath.Join(dir, "tls", "client.key"))
}

func onlyFile(pattern string) (string, error) {
	m, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(m) != 1 {
		return "", fmt.Errorf("want one file matching %s, found %d", pattern, len(m))
	}
	return m[0], nil
}

// signer is the MSP identity of the admin user in adminDir.
func signer(mspID, adminDir string) (identity.SigningIdentity, error) {
	certPath, err := onlyFile(filepath.Join(adminDir, "msp", "signcerts", "*.pem"))
	if err != nil {
		return nil, err
	}
	keyPath, err := onlyFile(filepath.Join(adminDir, "msp", "keystore", "*_sk"))
	if err != nil {
		return nil, err
	}
	cert, err := identity.ReadCertificate(certPath)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(keyPath) //nolint:gosec // project file
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM key", keyPath)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyPath, err)
	}
	if _, ok := key.(crypto.Signer); !ok {
		return nil, fmt.Errorf("%s: unsupported key type %T", keyPath, key)
	}
	return identity.NewPrivateKeySigningIdentity(mspID, cert, key)
}

// peerConn is a client connection to peer j of org i, as that org's admin.
// The peers require a TLS client certificate from their own org's TLS CA,
// so the connection uses the org admin's TLS client certificate.
type peerConn struct {
	id   identity.SigningIdentity
	conn *grpc.ClientConn
	host string
}

func (n *Network) dialPeer(org, peer int) (*peerConn, error) {
	v := n.Vars
	pool, err := readPool(n.path(v.PeerTLSRoot(org, peer)))
	if err != nil {
		return nil, err
	}
	adminDir := n.path(v.OrgAdminDir(org))
	cert, err := clientCert(adminDir)
	if err != nil {
		return nil, err
	}
	id, err := signer(v.MSPID(org), adminDir)
	if err != nil {
		return nil, err
	}
	host := v.Peer(org, peer)
	creds := credentials.NewTLS(&tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   host,
		MinVersion:   tls.VersionTLS12,
	})
	conn, err := grpc.NewClient(n.Resolve(host, 7051), grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", host, err)
	}
	return &peerConn{host: host, conn: conn, id: id}, nil
}

func (p *peerConn) Close() { _ = p.conn.Close() }

// forEachPeer runs f on every peer, org by org, as the shell forEachPeer did.
func (n *Network) forEachPeer(f func(org, peer int) error) error {
	for _, i := range n.Vars.OrgIndices {
		for _, j := range n.Vars.PeerIndices {
			if err := f(i, j); err != nil {
				return err
			}
		}
	}
	return nil
}

// forEachOrg runs f on the first peer of every org.
func (n *Network) forEachOrg(f func(org, peer int) error) error {
	for _, i := range n.Vars.OrgIndices {
		if err := f(i, n.Vars.PeerIndices[0]); err != nil {
			return err
		}
	}
	return nil
}

var errNotYet = errors.New("not yet")
