package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyperledger/fabric-admin-sdk/pkg/chaincode"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/common/policydsl"

	cc "github.com/luthersystems/fabric-network-builder/internal/chaincode"
)

// InstallOptions select the chaincodes to install and define.
type InstallOptions struct {
	Channel string
	// Names are the chaincode names; each has a package <label>.tar.gz in
	// PackageDir, which generatecc wrote.
	Names        []string
	Version      string
	PackageDir   string
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
func installedID(ctx context.Context, p *peerConn, label string) (string, error) {
	res, err := chaincode.NewPeer(p.conn, p.id).QueryInstalled(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range res.GetInstalledChaincodes() {
		if c.GetLabel() == label {
			return c.GetPackageId(), nil
		}
	}
	return "", nil
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
		if err := n.forEachPeer(func(org, peer int) error { return n.installOn(ctx, org, peer, label, pkg) }); err != nil {
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
		}
		if err := n.forEachOrg(func(org, peer int) error { return n.approve(ctx, org, peer, label, def) }); err != nil {
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

func (n *Network) installOn(ctx context.Context, org, peer int, label string, pkg []byte) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	defer p.Close()
	return n.retry(ctx, "install "+label+" on "+p.host, func() error {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		id, err := installedID(qctx, p, label)
		if err != nil {
			return err
		}
		if id != "" {
			n.printf("Chaincode %s already installed on %s\n", label, p.host)
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
	defer p.Close()
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := chaincode.NewGateway(p.conn, p.id).QueryCommittedWithName(qctx, channelID, name)
	if err != nil {
		// The peer answers an error for a chaincode that is not defined.
		return 1, nil //nolint:nilerr // not defined: the first sequence
	}
	return res.GetSequence() + 1, nil
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

func (n *Network) approve(ctx context.Context, org, peer int, label string, def *chaincode.Definition) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	defer p.Close()
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id, err := installedID(qctx, p, label)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%s: chaincode %s is not installed", p.host, label)
	}
	d := *def
	d.PackageID = id
	if approvals, err := n.readiness(ctx, p, &d); err == nil && approvals[p.id.MspID()] {
		n.printf("Chaincode already approved for %s\n", p.id.MspID())
		return nil
	}
	n.printf("Approving chaincode for %s on %s (package %s)...\n", p.id.MspID(), p.host, id)
	actx, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	if err := chaincode.NewGateway(p.conn, p.id).Approve(actx, &d); err != nil {
		return fmt.Errorf("%s: %w", p.host, err)
	}
	n.printf("Chaincode is approved for %s\n", p.id.MspID())
	return nil
}

func (n *Network) waitReady(ctx context.Context, org, peer int, def *chaincode.Definition) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	defer p.Close()
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

func (n *Network) commit(ctx context.Context, def *chaincode.Definition) error {
	p, err := n.dialPeer(n.Vars.OrgIndices[0], n.Vars.PeerIndices[0])
	if err != nil {
		return err
	}
	defer p.Close()
	n.printf("Committing chaincode definition %s %s sequence %d...\n", def.Name, def.Version, def.Sequence)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := chaincode.NewGateway(p.conn, p.id).Commit(cctx, def); err != nil {
		return fmt.Errorf("commit %s: %w", def.Name, err)
	}
	n.printf("Chaincode definition committed\n")
	return nil
}

func (n *Network) waitCommitted(ctx context.Context, org, peer int, def *chaincode.Definition) error {
	p, err := n.dialPeer(org, peer)
	if err != nil {
		return err
	}
	defer p.Close()
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
