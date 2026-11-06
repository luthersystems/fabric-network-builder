package admin

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/hyperledger/fabric-admin-sdk/pkg/identity"
	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	ab "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
)

// A network that fabric-network-builder v0.0.10 or earlier generated runs
// its orderers from the system channel genesis block, with no channel
// participation API. Its channel is created the Fabric 2.x way: the
// orderer gets the signed channel creation transaction (channel.tx) and
// makes the channel's genesis block, the peers join with that block, and
// each org sends its anchor peer update (<MSP>anchors.tx). v0.0.10 did this
// with `peer channel create`, `peer channel join` and `peer channel update`
// in the cli container (scripts/create_channel.sh, join_channel.sh).

// sdkSigner adapts a fabric-admin-sdk identity to the protoutil signer.
type sdkSigner struct{ identity.SigningIdentity }

func (s sdkSigner) Serialize() ([]byte, error) {
	return proto.Marshal(&msp.SerializedIdentity{Mspid: s.MspID(), IdBytes: s.Credentials()})
}

// ordererConn is a gRPC connection to the first orderer, as the admin of
// org. The orderer requires a TLS client certificate.
type ordererConn struct {
	conn    *grpc.ClientConn
	signer  sdkSigner
	host    string
	tlsHash []byte
}

func (n *Network) dialOrderer(org int) (*ordererConn, error) {
	v := n.Vars
	host := v.Orderer(v.OrdererIndices[0])
	pool, err := readPool(n.path(v.OrdererTLSRoot()))
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
	creds := credentials.NewTLS(&tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   host,
		MinVersion:   tls.VersionTLS12,
	})
	conn, err := grpc.NewClient(n.Resolve(host, 7050), grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", host, err)
	}
	// Deliver checks that the request is bound to the TLS client cert.
	sum := sha256.Sum256(cert.Certificate[0])
	return &ordererConn{conn: conn, signer: sdkSigner{id}, host: host, tlsHash: sum[:]}, nil
}

// signConfigUpdate adds the signer's signature to the config update in the
// envelope file and wraps it in an envelope the signer signs, as
// `peer channel create -f` and `peer channel update -f` do.
func signConfigUpdate(path, channelID string, s sdkSigner) (*cb.Envelope, error) {
	b, err := os.ReadFile(path) //nolint:gosec // a file of the network project
	if err != nil {
		return nil, err
	}
	env, err := protoutil.UnmarshalEnvelope(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	payload, err := protoutil.UnmarshalPayload(env.GetPayload())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ch, err := protoutil.UnmarshalChannelHeader(payload.GetHeader().GetChannelHeader())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ch.GetType() != int32(cb.HeaderType_CONFIG_UPDATE) {
		return nil, fmt.Errorf("%s: not a config update (type %d)", path, ch.GetType())
	}
	if ch.GetChannelId() != channelID {
		return nil, fmt.Errorf("%s: for channel %q, not %q", path, ch.GetChannelId(), channelID)
	}
	update := &cb.ConfigUpdateEnvelope{}
	err = proto.Unmarshal(payload.GetData(), update)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	header, err := protoutil.NewSignatureHeader(s)
	if err != nil {
		return nil, err
	}
	sig := &cb.ConfigSignature{SignatureHeader: protoutil.MarshalOrPanic(header)}
	sig.Signature, err = s.Sign(slices.Concat(sig.GetSignatureHeader(), update.GetConfigUpdate()))
	if err != nil {
		return nil, err
	}
	update.Signatures = append(update.Signatures, sig)
	return protoutil.CreateSignedEnvelope(cb.HeaderType_CONFIG_UPDATE, channelID, s, update, 0, 0)
}

// broadcast sends env to the orderer and fails unless it answers SUCCESS.
func (o *ordererConn) broadcast(ctx context.Context, env *cb.Envelope) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := ab.NewAtomicBroadcastClient(o.conn).Broadcast(cctx)
	if err != nil {
		return err
	}
	if err = stream.Send(env); err != nil {
		return err
	}
	resp, err := stream.Recv()
	if err != nil {
		return err
	}
	_ = stream.CloseSend()
	if resp.GetStatus() != cb.Status_SUCCESS {
		return fmt.Errorf("%s: %s: %s", o.host, resp.GetStatus(), resp.GetInfo())
	}
	return nil
}

// errNoChannel is the orderer's answer for a channel it does not have.
var errNoChannel = errors.New("channel not found")

// genesisBlock fetches block 0 of channelID. With wait false it returns
// errNoChannel at once if the orderer does not have the channel.
func (o *ordererConn) genesisBlock(ctx context.Context, channelID string, wait bool) (*cb.Block, error) {
	behavior := ab.SeekInfo_FAIL_IF_NOT_READY
	if wait {
		behavior = ab.SeekInfo_BLOCK_UNTIL_READY
	}
	zero := &ab.SeekPosition{Type: &ab.SeekPosition_Specified{Specified: &ab.SeekSpecified{Number: 0}}}
	env, err := protoutil.CreateSignedEnvelopeWithTLSBinding(cb.HeaderType_DELIVER_SEEK_INFO, channelID, o.signer,
		&ab.SeekInfo{Start: zero, Stop: zero, Behavior: behavior}, 0, 0, o.tlsHash)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := ab.NewAtomicBroadcastClient(o.conn).Deliver(cctx)
	if err != nil {
		return nil, err
	}
	if err = stream.Send(env); err != nil {
		return nil, err
	}
	defer func() { _ = stream.CloseSend() }()
	var block *cb.Block
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) && block != nil {
				return block, nil
			}
			return nil, rerr
		}
		switch t := resp.GetType().(type) {
		case *ab.DeliverResponse_Block:
			block = t.Block
		case *ab.DeliverResponse_Status:
			switch {
			case t.Status == cb.Status_SUCCESS && block != nil:
				return block, nil
			case t.Status == cb.Status_NOT_FOUND || t.Status == cb.Status_SERVICE_UNAVAILABLE && !wait:
				return nil, errNoChannel
			default:
				return nil, fmt.Errorf("%s: deliver %s: %s", o.host, channelID, t.Status)
			}
		}
	}
}

// CreateSystemChannel creates channelID on a network whose orderers run
// the system channel, joins every peer, and sends each org's anchor peer
// update. A channel the orderer already has is not created again, and its
// anchor peers are not updated again: a restarted network keeps them.
func (n *Network) CreateSystemChannel(ctx context.Context, channelID string) error {
	first := n.Vars.OrgIndices[0]
	o, err := n.dialOrderer(first)
	if err != nil {
		return err
	}
	defer func() { _ = o.conn.Close() }()

	created := false
	err = n.retry(ctx, "create "+channelID+" on "+o.host, func() error {
		_, gerr := o.genesisBlock(ctx, channelID, false)
		if gerr == nil {
			n.printf("%s already has channel %s\n", o.host, channelID)
			return nil
		}
		if !errors.Is(gerr, errNoChannel) {
			return gerr
		}
		env, serr := signConfigUpdate(n.path(filepath.Join("channel-artifacts", "channel.tx")), channelID, o.signer)
		if serr != nil {
			return serr
		}
		if berr := o.broadcast(ctx, env); berr != nil {
			return berr
		}
		created = true
		return nil
	})
	if err != nil {
		return err
	}
	var block *cb.Block
	err = n.retry(ctx, "fetch the genesis block of "+channelID, func() error {
		var gerr error
		block, gerr = o.genesisBlock(ctx, channelID, true)
		return gerr
	})
	if err != nil {
		return err
	}
	if created {
		n.printf("Channel %q is created\n", channelID)
	}
	if err = n.joinPeers(ctx, channelID, block); err != nil {
		return err
	}
	if !created {
		return nil
	}
	for _, i := range n.Vars.OrgIndices {
		if err = n.updateAnchorPeers(ctx, channelID, i); err != nil {
			return err
		}
	}
	return nil
}

// updateAnchorPeers sends <MSP>anchors.tx, signed by the org's admin.
func (n *Network) updateAnchorPeers(ctx context.Context, channelID string, org int) error {
	o, err := n.dialOrderer(org)
	if err != nil {
		return err
	}
	defer func() { _ = o.conn.Close() }()
	mspID := n.Vars.MSPID(org)
	env, err := signConfigUpdate(n.path(filepath.Join("channel-artifacts", mspID+"anchors.tx")), channelID, o.signer)
	if err != nil {
		return err
	}
	if err = n.retry(ctx, "anchor peers of "+mspID, func() error { return o.broadcast(ctx, env) }); err != nil {
		return err
	}
	n.printf("Anchor peers of %s are updated\n", mspID)
	return nil
}
