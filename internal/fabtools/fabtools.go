// Package fabtools runs cryptogen and configtxgen in process.
//
// The builder used to exec the cryptogen and configtxgen binaries from the
// Fabric tools image. Fabric keeps their logic in internal packages, so
// third_party/fabric holds a copy of it (see its README). Running it in
// process removes the tools from the image and pins their behaviour to the
// Go module, not to whichever tools release the image had.
package fabtools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyperledger/fabric/protoutil"

	"github.com/luthersystems/fabric-network-builder/third_party/fabric/configtxgen/encoder"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/configtxgen/genesisconfig"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/cryptogen"
)

// GenesisProfile is the configtx.yaml profile of the application channel.
const GenesisProfile = "AnyOrgsApplicationGenesis"

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
func ChannelGenesisBlock(configDir, channelID, outputBlock string) (err error) {
	// genesisconfig and encoder report a bad config with a panic, as the
	// configtxgen command does.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("configtxgen: %v", r)
		}
	}()
	genesisconfig.ForgetCache()
	profile := genesisconfig.Load(GenesisProfile, configDir)
	if profile.Orderer == nil {
		return errors.New("configtxgen: profile has no orderer section")
	}
	if profile.Application == nil {
		return errors.New("configtxgen: profile has no application section")
	}
	bs, err := encoder.NewBootstrapper(profile)
	if err != nil {
		return fmt.Errorf("configtxgen: %w", err)
	}
	block := bs.GenesisBlockForChannel(channelID)
	b, err := protoutil.Marshal(block)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputBlock), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputBlock, b, 0o640) //nolint:gosec // same mode as configtxgen
}
