// Package topology computes the shape of a generated network (orgs, peers,
// orderers, ports, policies and private data collections) from the generate
// options.
//
// It exists apart from the templates so the rules that derive one value from
// another (port numbers, the majority endorser count, the nchoose2 collection
// set) live in Go code with unit tests, not inside template expressions. The
// Python builder computed these inline in network.py; the values here must
// stay byte-identical to what it produced, because consumers commit the
// rendered files and a changed port or policy is a changed network.
package topology

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Options are the generate options that shape the network.
type Options struct {
	CCName            string
	DomainName        string
	ConnectDomainName string
	PrivateStructure  string
	OrdererType       string
	OrdererSANDomains []string
	PeerSANDomains    []string
	OrgCount          int
	PeerCount         int
	MinEndorsers      int
	ReqPeerCount      int
	MaxPeerCount      int
	ExecuteTimeout    int
	OrdererCount      int
	EnableNodeOUs     bool
}

// DefaultOptions returns the defaults of `generate`, the same as the Python
// builder's.
func DefaultOptions() Options {
	return Options{
		CCName:           "com_luthersystems_chaincode_substrate01",
		DomainName:       "example.com",
		OrgCount:         2,
		PeerCount:        2,
		MinEndorsers:     0,
		PrivateStructure: "shared",
		ReqPeerCount:     -1,
		MaxPeerCount:     -1,
		ExecuteTimeout:   30,
		OrdererType:      "etcdraft",
		OrdererCount:     1,
	}
}

// Orderer is one orderer node.
type Orderer struct {
	// Host is the name peers and clients connect to (the connect domain).
	Host string
	// Dir is the node's directory name under crypto-config (the domain).
	Dir string
	// Port is the port the orderer listens on in its container (7050).
	Port string
	// HostPort is the orderer's port on the host: 7050, 8050, 9050, ...
	HostPort string
	// Index is the orderer's number, from 0.
	Index int
}

// Org is one peer organization.
type Org struct {
	// Index is the org's number, from 1.
	Index string
	// CAPort is the host port of the org's CA: 7054, 8054, ...
	CAPort string
	// SANs are the peer SAN domain suffixes with {i} replaced by Index.
	SANs []string
}

// PeerPort is one peer and the port prefix of its host ports: the peer
// publishes <Prefix>51 and <Prefix>53.
type PeerPort struct {
	Org       string
	Peer      string
	Bootstrap string
	Prefix    string
}

// Collection is one private data collection, in the field order of
// collections.json.
type Collection struct {
	Name              string `json:"name"`
	Policy            string `json:"policy"`
	RequiredPeerCount int    `json:"requiredPeerCount"`
	MaxPeerCount      int    `json:"maxPeerCount"`
	BlockToLive       int    `json:"blockToLive"`
	MemberOnlyRead    bool   `json:"memberOnlyRead"`
	MemberOnlyWrite   bool   `json:"memberOnlyWrite"`
}

// Network is the computed network: the data the templates render.
type Network struct {
	EndorsementPolicy string
	Orgs              []Org
	PeerIndices       []string
	Orderers          []Orderer
	PeerPorts         []PeerPort
	Collections       []Collection
	Options
}

// New computes the network for opts.
func New(opts Options) (*Network, error) {
	if opts.OrgCount < 1 {
		return nil, errors.New("--org-count must be at least 1")
	}
	if opts.PeerCount < 1 {
		return nil, errors.New("--peer-count must be at least 1")
	}
	if opts.OrdererCount < 1 {
		return nil, errors.New("--orderer-count must be at least 1")
	}
	if opts.OrdererType != "etcdraft" {
		// Only etcdraft: the network has no system channel, and the channel
		// participation API that replaces it does not support solo (Fabric 3
		// removed solo outright).
		return nil, fmt.Errorf("--orderer-type %q is not supported: only etcdraft", opts.OrdererType)
	}
	if opts.ConnectDomainName == "" {
		opts.ConnectDomainName = opts.DomainName
	}
	n := &Network{Options: opts}
	for i := range opts.OrdererCount {
		n.Orderers = append(n.Orderers, Orderer{
			Index:    i,
			Host:     fmt.Sprintf("orderer%d.%s", i, opts.ConnectDomainName),
			Dir:      fmt.Sprintf("orderer%d.%s", i, opts.DomainName),
			Port:     "7050",
			HostPort: strconv.Itoa(7050 + 1000*i),
		})
	}
	for i := 1; i <= opts.OrgCount; i++ {
		org := Org{Index: strconv.Itoa(i), CAPort: strconv.Itoa(1000*i + 6054)}
		for _, d := range opts.PeerSANDomains {
			// The Python builder used str.format(i=i): {i} is the only field.
			org.SANs = append(org.SANs, strings.ReplaceAll(d, "{i}", org.Index))
		}
		n.Orgs = append(n.Orgs, org)
	}
	for j := range opts.PeerCount {
		n.PeerIndices = append(n.PeerIndices, strconv.Itoa(j))
	}
	// Each peer takes the next port prefix: 70, 80, 90, 100, ... in org
	// then peer order. The bootstrap peer is always peer0 of the org.
	p := 70
	for _, org := range n.Orgs {
		for _, j := range n.PeerIndices {
			n.PeerPorts = append(n.PeerPorts, PeerPort{Org: org.Index, Peer: j, Bootstrap: "0", Prefix: strconv.Itoa(p)})
			p += 10
		}
	}
	if err := n.computePolicies(); err != nil {
		return nil, err
	}
	// --min-endorsers=-1 asks for a majority of all peers.
	if n.MinEndorsers == -1 {
		n.MinEndorsers = opts.OrgCount*opts.PeerCount/2 + 1
	}
	return n, nil
}

func (n *Network) computePolicies() error {
	total := n.OrgCount * n.PeerCount
	req := n.ReqPeerCount
	if req == -1 {
		req = total / 2
	}
	maxPeers := n.MaxPeerCount
	if maxPeers == -1 {
		maxPeers = total - 1
	}
	members := func(from int) string {
		var s []string
		for i := from; i <= n.OrgCount; i++ {
			s = append(s, fmt.Sprintf("'Org%dMSP.member'", i))
		}
		return strings.Join(s, ", ")
	}
	collection := func(name, policy string) Collection {
		return Collection{Name: name, Policy: policy, RequiredPeerCount: req, MaxPeerCount: maxPeers}
	}
	switch {
	case strings.HasPrefix(n.PrivateStructure, "nchoose2common,"):
		// Org1 shares a collection with each pair of the other orgs; each
		// org has a vanity name for collection names.
		vanity := strings.Split(n.PrivateStructure, ",")[1:]
		if len(vanity) != n.OrgCount {
			return errors.New("improper length of vanity list")
		}
		n.EndorsementPolicy = fmt.Sprintf("Or('Org1MSP.member', OutOf(2, %s))", members(2))
		for i := 2; i <= n.OrgCount; i++ {
			for j := i + 1; j <= n.OrgCount; j++ {
				n.Collections = append(n.Collections, collection(
					vanity[i-1]+"_"+vanity[j-1],
					fmt.Sprintf("OR('Org%dMSP.member','Org%dMSP.member','Org%dMSP.member')", 1, i, j)))
			}
		}
	case n.PrivateStructure == "nchoose2":
		n.EndorsementPolicy = fmt.Sprintf("OutOf(2, %s)", members(1))
		for i := 1; i <= n.OrgCount; i++ {
			for j := i + 1; j <= n.OrgCount; j++ {
				n.Collections = append(n.Collections, collection(
					fmt.Sprintf("org%dorg%d", i, j),
					fmt.Sprintf("OR('Org%dMSP.member', 'Org%dMSP.member')", i, j)))
			}
		}
	default:
		n.EndorsementPolicy = fmt.Sprintf("OR(%s)", members(1))
		n.Collections = []Collection{collection("private", fmt.Sprintf("OR(%s)", members(1)))}
	}
	return nil
}

// CollectionsJSON is collections.json: the collections indented by four
// spaces, as Python's json.dumps(indent=4) wrote them.
func (n *Network) CollectionsJSON() (string, error) {
	cs := n.Collections
	if cs == nil {
		cs = []Collection{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(cs); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// OrdererAddresses is the channel-level orderer list as a JSON array, as
// Python's json.dumps wrote it (", " between items).
func (n *Network) OrdererAddresses() string {
	var s []string
	for _, o := range n.Orderers {
		s = append(s, strconv.Quote(o.Host+":"+o.Port))
	}
	return "[" + strings.Join(s, ", ") + "]"
}

// OrgIndices are the org numbers.
func (n *Network) OrgIndices() []string {
	var s []string
	for _, o := range n.Orgs {
		s = append(s, o.Index)
	}
	return s
}
