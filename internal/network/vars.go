// Package network reads the generated project and knows the names, paths
// and addresses of its nodes.
package network

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Vars are the topology values `generate` wrote to scripts/variables.sh.
// The commands that run after generate (up, install) read them there, as
// the shell scripts did, because generate's options are not passed again.
type Vars struct {
	DomainName        string
	OrgIndices        []int
	PeerIndices       []int
	OrdererIndices    []int
	EndorsementPolicy string
}

var (
	scalarRe = regexp.MustCompile(`^([A-Z_]+)="(.*)"$`)
	arrayRe  = regexp.MustCompile(`^([A-Z_]+)=\(\s*(.*?)\s*\)$`)
)

// ReadVars parses <dir>/scripts/variables.sh. It reads only the
// assignments the template writes, not general shell.
func ReadVars(dir string) (*Vars, error) {
	path := filepath.Join(dir, "scripts", "variables.sh")
	b, err := os.ReadFile(path) //nolint:gosec // project file
	if err != nil {
		return nil, fmt.Errorf("read the network topology (run generate first): %w", err)
	}
	v := &Vars{}
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if m := scalarRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "DOMAIN_NAME":
				v.DomainName = m[2]
			case "ENDORSEMENT_POLICY":
				v.EndorsementPolicy = m[2]
			}
			continue
		}
		if m := arrayRe.FindStringSubmatch(line); m != nil {
			var ints []int
			for _, f := range strings.Fields(m[2]) {
				n, err := strconv.Atoi(f)
				if err != nil {
					return nil, fmt.Errorf("%s: %s: %w", path, m[1], err)
				}
				ints = append(ints, n)
			}
			switch m[1] {
			case "ORG_INDICES":
				v.OrgIndices = ints
			case "PEER_INDICES":
				v.PeerIndices = ints
			case "ORDERER_INDICES":
				v.OrdererIndices = ints
			}
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	switch {
	case v.DomainName == "":
		return nil, fmt.Errorf("%s: no DOMAIN_NAME", path)
	case len(v.OrgIndices) == 0 || len(v.PeerIndices) == 0:
		return nil, fmt.Errorf("%s: no ORG_INDICES or PEER_INDICES", path)
	case len(v.OrdererIndices) == 0:
		return nil, fmt.Errorf("%s: no ORDERER_INDICES", path)
	case v.EndorsementPolicy == "":
		return nil, fmt.Errorf("%s: no ENDORSEMENT_POLICY", path)
	}
	return v, nil
}

// Org is the name of org i: org<i>.<domain>.
func (v *Vars) Org(i int) string { return fmt.Sprintf("org%d.%s", i, v.DomainName) }

// MSPID is the MSP ID of org i.
func (v *Vars) MSPID(i int) string { return fmt.Sprintf("Org%dMSP", i) }

// Peer is the host name of peer j of org i.
func (v *Vars) Peer(i, j int) string { return fmt.Sprintf("peer%d.%s", j, v.Org(i)) }

// Orderer is the host name of orderer i.
func (v *Vars) Orderer(i int) string { return fmt.Sprintf("orderer%d.%s", i, v.DomainName) }

// The crypto-config paths below are cryptogen's layout. They are relative
// to the project directory.

// PeerTLSRoot is the TLS CA certificate of peer j of org i.
func (v *Vars) PeerTLSRoot(i, j int) string {
	return filepath.Join("crypto-config", "peerOrganizations", v.Org(i), "peers", v.Peer(i, j), "tls", "ca.crt")
}

// OrgAdminDir is the directory of the admin user of org i.
func (v *Vars) OrgAdminDir(i int) string {
	return filepath.Join("crypto-config", "peerOrganizations", v.Org(i), "users", "Admin@"+v.Org(i))
}

// OrdererTLSRoot is the TLS CA certificate of the orderer org.
func (v *Vars) OrdererTLSRoot() string {
	return filepath.Join("crypto-config", "ordererOrganizations", v.DomainName, "orderers", v.Orderer(0),
		"msp", "tlscacerts", "tlsca."+v.DomainName+"-cert.pem")
}

// OrdererAdminDir is the directory of the admin user of the orderer org.
func (v *Vars) OrdererAdminDir() string {
	return filepath.Join("crypto-config", "ordererOrganizations", v.DomainName, "users", "Admin@"+v.DomainName)
}

// ChannelBlock is the genesis block of channel, which generate writes.
func ChannelBlock(channel string) string {
	return filepath.Join("channel-artifacts", channel+".block")
}
