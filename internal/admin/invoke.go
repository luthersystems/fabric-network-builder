package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	gp "github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/protobuf/proto"
)

// PeerRef names peer Peer of org Org.
type PeerRef struct{ Org, Peer int }

var peerName = regexp.MustCompile(`^peer(\d+)\.org(\d+)(\..+)?$`)

// ParsePeer reads a peer name such as peer0.org1 or
// peer0.org1.example.com.
func ParsePeer(s string) (PeerRef, error) {
	m := peerName.FindStringSubmatch(s)
	if m == nil {
		return PeerRef{}, fmt.Errorf("peer %q: want peerJ.orgI", s)
	}
	j, _ := strconv.Atoi(m[1])
	i, _ := strconv.Atoi(m[2])
	return PeerRef{Org: i, Peer: j}, nil
}

// ParseCtor reads the peer CLI's -c argument: {"Args":[...]}, with an
// optional "Function" that comes before the arguments.
func ParseCtor(s string) ([][]byte, error) {
	var c struct {
		Function string   `json:"function"`
		Args     []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil, fmt.Errorf("constructor %q: %w", s, err)
	}
	var args [][]byte
	if c.Function != "" {
		args = append(args, []byte(c.Function))
	}
	for _, a := range c.Args {
		args = append(args, []byte(a))
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("constructor %q: no function or arguments", s)
	}
	return args, nil
}

// InvokeOptions say which transaction to submit.
type InvokeOptions struct {
	Channel   string
	Chaincode string
	// Peers endorse the transaction. The first peer's org admin signs it.
	// Empty means the first peer of each org, as scripts/init.sh did.
	Peers []PeerRef
	Args  [][]byte
	// Init marks the transaction as the chaincode's Init (--isInit).
	Init bool
}

// Invoke submits a chaincode transaction and waits until it is committed
// and valid, as `peer chaincode invoke --waitForEvent` did. It returns the
// chaincode's response payload.
func (n *Network) Invoke(ctx context.Context, o InvokeOptions) ([]byte, error) {
	peers := o.Peers
	if len(peers) == 0 {
		for _, i := range n.Vars.OrgIndices {
			peers = append(peers, PeerRef{Org: i, Peer: n.Vars.PeerIndices[0]})
		}
	}
	first, err := n.dialPeer(peers[0].Org, peers[0].Peer)
	if err != nil {
		return nil, err
	}
	s := sdkSigner{first.id}
	creator, err := s.Serialize()
	if err != nil {
		return nil, err
	}
	cis := &pb.ChaincodeInvocationSpec{ChaincodeSpec: &pb.ChaincodeSpec{
		Type:        pb.ChaincodeSpec_GOLANG,
		ChaincodeId: &pb.ChaincodeID{Name: o.Chaincode},
		Input:       &pb.ChaincodeInput{Args: o.Args, IsInit: o.Init},
	}}
	prop, txID, err := protoutil.CreateChaincodeProposal(cb.HeaderType_ENDORSER_TRANSACTION, o.Channel, cis, creator)
	if err != nil {
		return nil, err
	}
	signed, err := protoutil.GetSignedProposal(prop, s)
	if err != nil {
		return nil, err
	}

	var responses []*pb.ProposalResponse
	for _, r := range peers {
		p, derr := n.dialPeer(r.Org, r.Peer)
		if derr != nil {
			return nil, derr
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		resp, perr := pb.NewEndorserClient(p.conn).ProcessProposal(cctx, signed)
		cancel()
		if perr != nil {
			return nil, fmt.Errorf("endorse on %s: %w", p.host, perr)
		}
		if st := resp.GetResponse().GetStatus(); st < 200 || st >= 400 || resp.GetEndorsement() == nil {
			return nil, fmt.Errorf("endorse on %s: status %d: %s", p.host, st, resp.GetResponse().GetMessage())
		}
		responses = append(responses, resp)
	}
	env, err := protoutil.CreateSignedTx(prop, s, responses...)
	if err != nil {
		return nil, err
	}

	o2, err := n.dialOrderer(peers[0].Org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = o2.conn.Close() }()
	if err = o2.broadcast(ctx, env); err != nil {
		return nil, err
	}

	req, err := proto.Marshal(&gp.CommitStatusRequest{TransactionId: txID, ChannelId: o.Channel, Identity: creator})
	if err != nil {
		return nil, err
	}
	sig, err := s.Sign(req)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	status, err := gp.NewGatewayClient(first.conn).CommitStatus(cctx, &gp.SignedCommitStatusRequest{Request: req, Signature: sig})
	if err != nil {
		return nil, fmt.Errorf("commit status of %s: %w", txID, err)
	}
	if status.GetResult() != pb.TxValidationCode_VALID {
		return nil, fmt.Errorf("transaction %s is %s", txID, status.GetResult())
	}
	n.printf("Transaction %s committed in block %d\n", txID, status.GetBlockNumber())
	return responses[0].GetResponse().GetPayload(), nil
}
