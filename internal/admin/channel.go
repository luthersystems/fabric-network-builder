package admin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/hyperledger/fabric-admin-sdk/pkg/channel"
	pb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/protoutil"

	"github.com/luthersystems/fabric-network-builder/internal/network"
)

// osnClient is an HTTPS client for the orderers' channel participation API
// (port 7053), authenticated with the orderer org admin's TLS client
// certificate.
func (n *Network) osnClient(host string) (*http.Client, error) {
	pool, err := readPool(n.path(n.Vars.OrdererTLSRoot()))
	if err != nil {
		return nil, err
	}
	cert, err := clientCert(n.path(n.Vars.OrdererAdminDir()))
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{cert},
			ServerName:   host,
			MinVersion:   tls.VersionTLS12,
		}},
	}, nil
}

func (n *Network) osnURL(host, channelID string) string {
	return fmt.Sprintf("https://%s/participation/v1/channels/%s", n.Resolve(host, 7053), channelID)
}

// ordererActive reports whether the orderer is already an active member of
// the channel. A restarted network keeps the orderer's ledger volume, and a
// second join fails with 405.
func (n *Network) ordererActive(ctx context.Context, c *http.Client, host, channelID string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.osnURL(host, channelID), nil)
	if err != nil {
		return false, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("list channel: HTTP %d", resp.StatusCode)
	}
	var info struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return false, err
	}
	return info.Status == "active", nil
}

// ordererJoin posts the genesis block; the block names the channel.
func (n *Network) ordererJoin(ctx context.Context, c *http.Client, host string, block []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("config-block", "config.block")
	if err != nil {
		return err
	}
	if _, err = part.Write(block); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	url := fmt.Sprintf("https://%s/participation/v1/channels", n.Resolve(host, 7053))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// Only 201 Created is a join. osnadmin exited 0 on any reply, so the
	// shell version had to check the status line itself.
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("join: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}

// CreateChannel joins every orderer to channelID with the channel's genesis
// block. There is no system channel and no channel creation transaction:
// Fabric 3 removed both. The anchor peers are in the genesis block.
func (n *Network) CreateChannel(ctx context.Context, channelID string) error {
	block, err := os.ReadFile(n.path(network.ChannelBlock(channelID)))
	if err != nil {
		return err
	}
	for _, i := range n.Vars.OrdererIndices {
		host := n.Vars.Orderer(i)
		c, err := n.osnClient(host)
		if err != nil {
			return err
		}
		joined := false
		err = n.retry(ctx, host+" join "+channelID, func() error {
			active, aerr := n.ordererActive(ctx, c, host, channelID)
			if aerr != nil {
				return aerr
			}
			if active {
				n.printf("%s is already a member of %s\n", host, channelID)
				return nil
			}
			if jerr := n.ordererJoin(ctx, c, host, block); jerr != nil {
				return jerr
			}
			joined = true
			return nil
		})
		if err != nil {
			return err
		}
		if joined {
			n.printf("%s joined %s\n", host, channelID)
		}
	}
	n.printf("Channel %q is created\n", channelID)
	return nil
}

// JoinPeers joins every peer to channelID. A peer that is already a
// member (a restarted network keeps its ledger) is skipped, where the shell
// version failed after its retries.
func (n *Network) JoinPeers(ctx context.Context, channelID string) error {
	b, err := os.ReadFile(n.path(network.ChannelBlock(channelID)))
	if err != nil {
		return err
	}
	block, err := protoutil.UnmarshalBlock(b)
	if err != nil {
		return err
	}
	return n.forEachPeer(func(org, peer int) error {
		p, err := n.dialPeer(org, peer)
		if err != nil {
			return err
		}
		defer p.Close()
		return n.retry(ctx, p.host+" join "+channelID, func() error {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			chans, err := channel.ListChannelOnPeer(cctx, p.conn, p.id)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(chans, func(c *pb.ChannelInfo) bool { return c.GetChannelId() == channelID }) {
				n.printf("%s is already a member of %s\n", p.host, channelID)
				return nil
			}
			if err := channel.JoinChannel(cctx, p.conn, p.id, block); err != nil {
				return err
			}
			n.printf("%s joined %s\n", p.host, channelID)
			return nil
		})
	})
}
