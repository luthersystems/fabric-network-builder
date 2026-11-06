// Package fabtools runs cryptogen and configtxgen in process.
//
// The builder used to exec the cryptogen and configtxgen binaries from the
// Fabric tools image. Fabric keeps their logic in internal packages, so
// third_party/fabric holds a copy of it (see its README). Running it in
// process removes the tools from the image and pins their behaviour to the
// Go module, not to whichever tools release the image had.
package fabtools

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	cb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/common/channelconfig"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/protobuf/proto"

	"github.com/luthersystems/fabric-network-builder/third_party/fabric/configtxgen/encoder"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/configtxgen/genesisconfig"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/configtxlator/update"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/cryptogen"
)

// GenesisProfile is the configtx.yaml profile of the application channel.
const GenesisProfile = "AnyOrgsApplicationGenesis"

// The Fabric 2.5 orderer system channel flow, which the mars Kubernetes
// roles use for a new 2.5 network. v0.0.10 wrote these files with
// configtxgen, and fnb-gen.sh ships channel-artifacts to mars.
const (
	// SystemProfile is the configtx.yaml profile of the system channel.
	SystemProfile = "AnyOrgsOrdererGenesis"
	// ChannelProfile is the configtx.yaml profile of the channel create
	// transaction and of the anchor peer updates.
	ChannelProfile = "AnyOrgsChannel"
	// SystemChannelID is the ID of the orderer system channel.
	SystemChannelID = "byfn-sys-channel"
	// SystemChannelBlock is the file name of the system channel genesis block.
	SystemChannelBlock = "genesis.block"
	// ChannelCreateTx is the file name of the channel create transaction.
	ChannelCreateTx = "channel.tx"
)

// GenerateCrypto writes the key material that configPath (a
// crypto-config.yaml) describes to outputDir.
func GenerateCrypto(configPath, outputDir string) error {
	b, err := os.ReadFile(configPath) //nolint:gosec // operator-supplied project file
	if err != nil {
		return err
	}
	return cryptogen.Generate(b, outputDir)
}

// ExtendCrypto adds to inputDir the key material for the orgs, nodes and
// users of configPath that it does not have yet, signed by the existing CAs.
func ExtendCrypto(configPath, inputDir string) error {
	b, err := os.ReadFile(configPath) //nolint:gosec // operator-supplied project file
	if err != nil {
		return err
	}
	return cryptogen.Extend(b, inputDir)
}

// ChannelGenesisBlock writes the genesis block of channelID to outputBlock,
// from the GenesisProfile of the configtx.yaml in configDir. It is
// `configtxgen -profile AnyOrgsApplicationGenesis -outputBlock <out>
// -channelID <id>` with FABRIC_CFG_PATH=<configDir>.
func ChannelGenesisBlock(configDir, channelID, outputBlock string) error {
	var b []byte
	err := recoverPanic(func() error {
		var err error
		b, err = genesisBlock(configDir, channelID)
		return err
	})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(outputBlock), 0o755); err != nil { //nolint:gosec // channel-artifacts, read by the node containers
		return err
	}
	return os.WriteFile(outputBlock, b, 0o640) //nolint:gosec // same mode as configtxgen
}

// SystemChannelArtifacts writes to outDir the files of the Fabric 2.5
// orderer system channel flow, from the configtx.yaml in configDir:
//
//   - genesis.block: configtxgen -profile AnyOrgsOrdererGenesis
//     -channelID byfn-sys-channel -outputBlock
//   - channel.tx: configtxgen -profile AnyOrgsChannel -channelID <channelID>
//     -outputCreateChannelTx
//   - <MSP>anchors.tx for each org: configtxgen -profile AnyOrgsChannel
//     -channelID <channelID> -outputAnchorPeersUpdate -asOrg <MSP>
//     (Fabric 2.5; Fabric 3 removed the flag)
func SystemChannelArtifacts(configDir, channelID, outDir string) error {
	var files map[string][]byte
	err := recoverPanic(func() error {
		var err error
		files, err = systemChannelArtifacts(configDir, channelID)
		return err
	})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(outDir, 0o755); err != nil { //nolint:gosec // channel-artifacts, read by the node containers
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), b, 0o640); err != nil { //nolint:gosec // same mode as configtxgen
			return err
		}
	}
	return nil
}

func systemChannelArtifacts(configDir, channelID string) (map[string][]byte, error) {
	files := map[string][]byte{}
	genesisconfig.ForgetCache()
	system := genesisconfig.Load(SystemProfile, configDir)
	bs, err := encoder.NewBootstrapper(system)
	if err != nil {
		return nil, fmt.Errorf("configtxgen: %w", err)
	}
	block := bs.GenesisBlockForChannel(SystemChannelID)
	block.Header.DataHash = fabricDataHash(block.Data.Data)
	if files[SystemChannelBlock], err = protoutil.Marshal(block); err != nil {
		return nil, err
	}

	channel := genesisconfig.Load(ChannelProfile, configDir)
	tx, err := encoder.MakeChannelCreationTransaction(channelID, nil, channel)
	if err != nil {
		return nil, fmt.Errorf("configtxgen: %w", err)
	}
	if files[ChannelCreateTx], err = protoutil.Marshal(tx); err != nil {
		return nil, err
	}
	for _, org := range channel.Application.Organizations {
		tx, err := anchorPeersUpdate(channel, channelID, org.Name)
		if err != nil {
			return nil, err
		}
		if files[org.Name+"anchors.tx"], err = protoutil.Marshal(tx); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// anchorPeersUpdate is doOutputAnchorPeersUpdate of Fabric v2.5.16
// cmd/configtxgen/main.go: a config update that adds the anchor peers of
// asOrg to the channel that conf creates.
func anchorPeersUpdate(conf *genesisconfig.Profile, channelID, asOrg string) (*cb.Envelope, error) {
	original, err := encoder.NewChannelGroup(conf)
	if err != nil {
		return nil, fmt.Errorf("configtxgen: parse profile as channel group: %w", err)
	}
	original.Groups[channelconfig.ApplicationGroupKey].Version = 1
	updated, ok := proto.Clone(original).(*cb.ConfigGroup)
	if !ok {
		return nil, errors.New("configtxgen: clone channel group")
	}
	originalOrg, ok := original.Groups[channelconfig.ApplicationGroupKey].Groups[asOrg]
	if !ok {
		return nil, fmt.Errorf("configtxgen: org %q is not in the config", asOrg)
	}
	if _, ok = originalOrg.Values[channelconfig.AnchorPeersKey]; !ok {
		return nil, fmt.Errorf("configtxgen: org %q has no anchor peers", asOrg)
	}
	delete(originalOrg.Values, channelconfig.AnchorPeersKey)
	updt, err := update.Compute(&cb.Config{ChannelGroup: original}, &cb.Config{ChannelGroup: updated})
	if err != nil {
		return nil, fmt.Errorf("configtxgen: compute anchor peer update: %w", err)
	}
	updt.ChannelId = channelID
	b, err := protoutil.Marshal(updt)
	if err != nil {
		return nil, err
	}
	return protoutil.CreateSignedEnvelope(cb.HeaderType_CONFIG_UPDATE, channelID, nil, &cb.ConfigUpdateEnvelope{ConfigUpdate: b}, 0, 0)
}

// recoverPanic runs f and returns a panic in it as an error. genesisconfig
// and encoder report a bad config with a panic, as the configtxgen command
// does, and the builder must report it and go on.
func recoverPanic(f func() error) error {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("configtxgen: %v", r)
			}
		}()
		err = f()
	}()
	return err
}

func genesisBlock(configDir, channelID string) ([]byte, error) {
	genesisconfig.ForgetCache()
	profile := genesisconfig.Load(GenesisProfile, configDir)
	if profile.Orderer == nil {
		return nil, errors.New("configtxgen: profile has no orderer section")
	}
	if profile.Application == nil {
		return nil, errors.New("configtxgen: profile has no application section")
	}
	bs, err := encoder.NewBootstrapper(profile)
	if err != nil {
		return nil, fmt.Errorf("configtxgen: %w", err)
	}
	block := bs.GenesisBlockForChannel(channelID)
	block.Header.DataHash = fabricDataHash(block.Data.Data)
	return protoutil.Marshal(block)
}

// fabricDataHash is the block data hash of Fabric 2.5 and 3.x: the SHA-256
// of the joined data. The genesis factory in fabric-x-common uses the
// Fabric-X hash, which puts a length before each item. A Fabric orderer
// refuses a block with that hash ("Header.DataHash is different from
// Hash(block.Data)").
func fabricDataHash(data [][]byte) []byte {
	h := sha256.New()
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}
