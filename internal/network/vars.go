// Package network reads the generated project and knows the names, paths
// and addresses of its nodes.
package network

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// File is the file, relative to the project directory, where `generate`
// writes the topology.
const File = "fabric-network.json"

// legacyFile is where the builder before #35 wrote the topology, as the
// shell assignments of the v0.0.x scripts. A network generated then has no
// File.
var legacyFile = filepath.Join("scripts", "variables.sh")

// Vars are the topology values that `generate` writes to File. The
// commands that run after generate (up, install) read them there, because
// generate's options are not passed again.
type Vars struct {
	DomainName        string `json:"domain_name"`
	EndorsementPolicy string `json:"endorsement_policy"`
	OrgIndices        []int  `json:"org_indices"`
	PeerIndices       []int  `json:"peer_indices"`
	OrdererIndices    []int  `json:"orderer_indices"`
}

// JSON is the content of File for v.
func (v *Vars) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

var (
	scalarRe = regexp.MustCompile(`^([A-Z_]+)="(.*)"$`)
	arrayRe  = regexp.MustCompile(`^([A-Z_]+)=\(\s*(.*?)\s*\)$`)
)

// ReadVars reads the topology of the network in dir from File. A network
// that has no File, made by v0.0.x or by v0.1.x before #35, has its
// topology in scripts/variables.sh, so ReadVars reads that instead.
func ReadVars(dir string) (*Vars, error) {
	path := filepath.Join(dir, File)
	b, err := os.ReadFile(path) //nolint:gosec // project file
	var v *Vars
	switch {
	case err == nil:
		v = &Vars{}
		if jerr := json.Unmarshal(b, v); jerr != nil {
			return nil, fmt.Errorf("%s: %w", path, jerr)
		}
	case errors.Is(err, os.ErrNotExist):
		path = filepath.Join(dir, legacyFile)
		b, err = os.ReadFile(path) //nolint:gosec // project file
		if err != nil {
			return nil, fmt.Errorf("read the network topology (run generate first): %w", err)
		}
		if v, err = parseLegacy(path, b); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("read the network topology: %w", err)
	}
	return v, v.check(path)
}

// parseLegacy parses the scripts/variables.sh of an older network. It reads
// only the assignments the template wrote, not general shell.
func parseLegacy(path string, b []byte) (*Vars, error) {
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
	if len(v.OrdererIndices) == 0 {
		// A project generated before the builder wrote ORDERER_INDICES had
		// one orderer, orderer0, and still commits that variables.sh.
		v.OrdererIndices = []int{0}
	}
	return v, nil
}

// check reports a topology that lacks a value the commands need.
func (v *Vars) check(path string) error {
	switch {
	case v.DomainName == "":
		return fmt.Errorf("%s: no domain name", path)
	case len(v.OrgIndices) == 0 || len(v.PeerIndices) == 0:
		return fmt.Errorf("%s: no org or peer indices", path)
	case len(v.OrdererIndices) == 0:
		return fmt.Errorf("%s: no orderer indices", path)
	case v.EndorsementPolicy == "":
		return fmt.Errorf("%s: no endorsement policy", path)
	}
	return nil
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
