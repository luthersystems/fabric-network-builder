package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperledger/fabric-admin-sdk/pkg/chaincode"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	lb "github.com/hyperledger/fabric-protos-go-apiv2/peer/lifecycle"
	"github.com/hyperledger/fabric/common/policydsl"

	cc "github.com/luthersystems/fabric-network-builder/internal/chaincode"
)

// InstallOptions select the chaincodes to install and define.
type InstallOptions struct {
	Channel    string
	Version    string
	PackageDir string
	// Names are the chaincode names; each has a package <label>.tar.gz in
	// PackageDir, which generatecc wrote.
	Names        []string
	InitRequired bool
}

// collections reads collections.json into the definition's collection
// config, as `peer lifecycle ... --collections-config` did.
func (n *Network) collections() (*pb.CollectionConfigPackage, error) {
	b, err := os.ReadFile(n.path("collections.json"))
	if err != nil {
		return nil, err
	}
	var cols []struct {
		Name              string `json:"name"`
		Policy            string `json:"policy"`
		RequiredPeerCount int32  `json:"requiredPeerCount"`
		MaxPeerCount      int32  `json:"maxPeerCount"`
		BlockToLive       uint64 `json:"blockToLive"`
		MemberOnlyRead    bool   `json:"memberOnlyRead"`
		MemberOnlyWrite   bool   `json:"memberOnlyWrite"`
	}
	if err := json.Unmarshal(b, &cols); err != nil {
		return nil, fmt.Errorf("collections.json: %w", err)
	}
	pkg := &pb.CollectionConfigPackage{}
	for _, c := range cols {
		policy, err := policydsl.FromString(c.Policy)
		if err != nil {
			return nil, fmt.Errorf("collections.json: collection %s: %w", c.Name, err)
		}
		pkg.Config = append(pkg.Config, &pb.CollectionConfig{
			Payload: &pb.CollectionConfig_StaticCollectionConfig{StaticCollectionConfig: &pb.StaticCollectionConfig{
				Name: c.Name,
				MemberOrgsPolicy: &pb.CollectionPolicyConfig{
					Payload: &pb.CollectionPolicyConfig_SignaturePolicy{SignaturePolicy: policy},
				},
				RequiredPeerCount: c.RequiredPeerCount,
				MaximumPeerCount:  c.MaxPeerCount,
				BlockToLive:       c.BlockToLive,
				MemberOnlyRead:    c.MemberOnlyRead,
				MemberOnlyWrite:   c.MemberOnlyWrite,
			}},
		})
	}
	return pkg, nil
}

func (n *Network) applicationPolicy() (*pb.ApplicationPolicy, error) {
	policy, err := policydsl.FromString(n.Vars.EndorsementPolicy)
	if err != nil {
		return nil, fmt.Errorf("endorsement policy %q: %w", n.Vars.EndorsementPolicy, err)
	}
	return &pb.ApplicationPolicy{Type: &pb.ApplicationPolicy_SignaturePolicy{SignaturePolicy: policy}}, nil
}

// installedID returns the package ID of the package with label on the
// peer, or "" when none is installed.
// isInstalled reports whether the peer has the package with ID id. It
// matches the ID, not the label: a peer can keep an older package with the
// same label (one the shell builder made with GNU tar, or one generated
// again), and the definition must name the package whose ID generatecc
// wrote to <label>.id, which the CCaaS container runs with.
func isInstalled(ctx context.Context, p *peerConn, id string) (bool, error) {
	res, err := chaincode.NewPeer(p.conn, p.id).QueryInstalled(ctx)
	if err != nil {
		return false, err
	}
	return hasPackage(res.GetInstalledChaincodes(), id), nil
}

func hasPackage(installed []*lb.QueryInstalledChaincodesResult_InstalledChaincode, id string) bool {
	for _, c := range installed {
		if c.GetPackageId() == id {
			return true
		}
	}
	return false
}

// Install installs each chaincode on every peer and defines it on the
// channel: each org approves the definition, then the first org commits
// it. The steps are those of scripts/install.sh, and each skips work
// already done, so a second run after a failure goes on where it stopped.
func (n *Network) Install(ctx context.Context, o InstallOptions) error {
	collections, err := n.collections()
	if err != nil {
		return err
	}
	policy, err := n.applicationPolicy()
	if err != nil {
		return err
	}
	for _, name := range o.Names {
		label := cc.Label(name, o.Version)
		pkgPath := filepath.Join(o.PackageDir, label+".tar.gz")
		pkg, err := os.ReadFile(pkgPath) //nolint:gosec // generatecc output
		if err != nil {
			return err
		}
		installed := pkgFile{label: label, id: cc.PackageID(label, pkg), bytes: pkg}
		if err = n.forEachPeer(func(org, peer int) error { return n.installOn(ctx, org, peer, installed) }); err != nil {
			return err
		}
		seq, err := n.nextSequence(ctx, o.Channel, name)
		if err != nil {
			return err
		}
		n.printf("next sequence number: %d\n", seq)
		def := &chaincode.Definition{
			ChannelName:       o.Channel,
			Name:              name,
			Version:           o.Version,
			Sequence:          seq,
			ApplicationPolicy: policy,
			InitRequired:      o.InitRequired,
			Collections:       collections,
			PackageID:         installed.id,
		}
		if err := n.forEachOrg(func(org, peer int) error { return n.approve(ctx, org, peer, def) }); err != nil {
			return err
		}
		if err := n.forEachOrg(func(org, peer int) error { return n.waitReady(ctx, org, peer, def) }); err != nil {
			return err
		}
		if err := n.commit(ctx, def); err != nil {
			return err
		}
		if err := n.forEachOrg(func(org, peer int) error { return n.waitCommitted(ctx, org, peer, def) }); err != nil {
			return err
		}
	}
	n.printf("All GOOD, chaincode installed on all peers\n")
	return nil
}

// pkgFile is a chaincode package, its label and its package ID.
type pkgFile struct {
	label string
	id    string
	bytes []byte
}

func (n *Network) installOn(ctx context.Context, org, peer int, f pkgFile) error {
	label, pkg := f.label, f.bytes
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	return n.retry(ctx, "install "+label+" on "+p.host, func() error {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ok, err := isInstalled(qctx, p, f.id)
		if err != nil {
			return err
		}
		if ok {
			n.printf("Chaincode %s already installed on %s\n", f.id, p.host)
			return nil
		}
		n.printf("Installing chaincode %s on %s...\n", label, p.host)
		// A Go chaincode is built by the peer during install, which takes
		// minutes.
		ictx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		res, err := chaincode.NewPeer(p.conn, p.id).Install(ictx, bytes.NewReader(pkg))
		if err != nil {
			return err
		}
		n.printf("Chaincode is installed on %s: %s\n", p.host, res.GetPackageId())
		return nil
	})
}

// nextSequence is the committed sequence plus one, or 1 when the chaincode
// is not committed. The shell version always added one, also for a version
// that is already committed, so a repeated install redefines the chaincode.
func (n *Network) nextSequence(ctx context.Context, channelID, name string) (int64, error) {
	p, err := n.dialPeer(n.Vars.OrgIndices[0], n.Vars.PeerIndices[0])
	if err != nil {
		return 0, err
	}
	var seq int64
	err = n.retry(ctx, "query committed "+name+" on "+p.host, func() error {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		res, qerr := chaincode.NewGateway(p.conn, p.id).QueryCommittedWithName(qctx, channelID, name)
		switch {
		case qerr == nil:
			seq = res.GetSequence() + 1
		case committedNotDefined(qerr, name):
			seq = 1
		default:
			return qerr
		}
		return nil
	})
	return seq, err
}

// committedNotDefined reports whether err is the peer's answer for a
// chaincode with no committed definition. The gateway passes the
// _lifecycle error only as text, so match the text Fabric writes
// (lifecycle.ErrNamespaceNotDefined).
func committedNotDefined(err error, name string) bool {
	return strings.Contains(err.Error(), "namespace "+name+" is not defined")
}

func (n *Network) readiness(ctx context.Context, p *peerConn, def *chaincode.Definition) (map[string]bool, error) {
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := chaincode.NewGateway(p.conn, p.id).CheckCommitReadiness(qctx, def)
	if err != nil {
		return nil, err
	}
	return res.GetApprovals(), nil
}

// approve approves def, which names the installed package, for the org of
// peer. It retries: the gateway answers Unavailable while the orderer has no
// Raft leader yet, just after up. Each attempt first asks whether the org
// has approved, so an attempt whose transaction committed although the
// call failed is not approved twice.
func (n *Network) approve(ctx context.Context, org, peer int, def *chaincode.Definition) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	msp := p.id.MspID()
	return n.retry(ctx, "approve for "+msp+" on "+p.host, func() error {
		if approvals, rerr := n.readiness(ctx, p, def); rerr == nil && approvals[msp] {
			n.printf("Chaincode already approved for %s\n", msp)
			return nil
		}
		n.printf("Approving chaincode for %s on %s (package %s)...\n", msp, p.host, def.PackageID)
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if aerr := chaincode.NewGateway(p.conn, p.id).Approve(actx, def); aerr != nil {
			return aerr
		}
		n.printf("Chaincode is approved for %s\n", msp)
		return nil
	})
}

func (n *Network) waitReady(ctx context.Context, org, peer int, def *chaincode.Definition) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	return n.retry(ctx, "commit readiness on "+p.host, func() error {
		approvals, err := n.readiness(ctx, p, def)
		if err != nil {
			return err
		}
		for msp, ok := range approvals {
			if !ok {
				return fmt.Errorf("%w: %s has not approved", errNotYet, msp)
			}
		}
		n.printf("Chaincode commit readiness confirmed on %s\n", p.host)
		return nil
	})
}

// commit commits def. It retries for the same reason as approve. Each
// attempt first asks whether def is committed, so an attempt whose
// transaction committed although the call failed is not committed twice.
func (n *Network) commit(ctx context.Context, def *chaincode.Definition) error {
	p, err := n.dialPeer(n.Vars.OrgIndices[0], n.Vars.PeerIndices[0])
	if err != nil {
		return err
	}
	return n.retry(ctx, "commit "+def.Name+" on "+p.host, func() error {
		qctx, qcancel := context.WithTimeout(ctx, 30*time.Second)
		res, qerr := chaincode.NewGateway(p.conn, p.id).QueryCommittedWithName(qctx, def.ChannelName, def.Name)
		qcancel()
		if qerr == nil && res.GetSequence() == def.Sequence {
			n.printf("Chaincode definition already committed\n")
			return nil
		}
		n.printf("Committing chaincode definition %s %s sequence %d...\n", def.Name, def.Version, def.Sequence)
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if cerr := chaincode.NewGateway(p.conn, p.id).Commit(cctx, def); cerr != nil {
			return cerr
		}
		n.printf("Chaincode definition committed\n")
		return nil
	})
}

func (n *Network) waitCommitted(ctx context.Context, org, peer int, def *chaincode.Definition) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	return n.retry(ctx, "committed definition on "+p.host, func() error {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		res, err := chaincode.NewGateway(p.conn, p.id).QueryCommittedWithName(qctx, def.ChannelName, def.Name)
		if err != nil {
			return err
		}
		if res.GetVersion() != def.Version || res.GetSequence() != def.Sequence {
			return fmt.Errorf("%w: committed %s sequence %d", errNotYet, res.GetVersion(), res.GetSequence())
		}
		n.printf("Query chaincode definition successful on %s\n", p.host)
		return nil
	})
}
