package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The default image tags. The Dockerfile sets them with -ldflags -X from
// common.config.mk, as it baked them into byfn-vars.sh before.
var (
	ImageTag     = "2.5.16"
	CAImageTag   = "1.5.21"
	BaseImageTag = "0.4.22"
	ImageNS      = "hyperledger"
)

// imageTags are the tags the compose files read.
type imageTags struct {
	image, tools string
}

// fabricVersionAtLeast25 reports whether a Fabric image tag is 2.5 or
// later. A tag it cannot read (a digest, "latest") passes.
func fabricVersionAtLeast25(tag string) bool {
	parts := strings.SplitN(tag, ".", 3)
	if len(parts) < 2 {
		return true
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	return major > 2 || (major == 2 && minor >= 5)
}

// resolveImageTags reads FNB_IMAGETAG, which overrides the Fabric peer,
// orderer and ccenv tag so one builder image runs a network on another
// Fabric release (luthersystems/substrate#1034 runs its e2e networks on 2.5
// and 3.x), and FNB_TOOLSIMAGETAG. The cli container runs fabric-tools,
// which Fabric 3 no longer publishes, so the tools tag keeps a 2.x override
// and otherwise falls back to the default 2.x tag.
func resolveImageTags(getenv func(string) string) (imageTags, error) {
	t := imageTags{image: ImageTag}
	if v := getenv("FNB_IMAGETAG"); v != "" {
		t.image = v
	}
	if !fabricVersionAtLeast25(t.image) {
		return t, fmt.Errorf("fabric %s is not supported: the builder supports Fabric 2.5 and later", t.image)
	}
	if v := getenv("FNB_TOOLSIMAGETAG"); v != "" {
		t.tools = v
		return t, nil
	}
	switch {
	case strings.HasPrefix(t.image, "2."):
		t.tools = t.image
	case strings.HasPrefix(ImageTag, "2."):
		t.tools = ImageTag
	default:
		return t, fmt.Errorf("no fabric-tools image for Fabric %s: set FNB_TOOLSIMAGETAG to a 2.x release", t.image)
	}
	return t, nil
}

// composeEnv is the interpolation environment of the compose files.
func composeEnv(dir, channel, ccVersion string) (map[string]string, error) {
	t, err := resolveImageTags(os.Getenv)
	if err != nil {
		return nil, err
	}
	env, err := chaincodeIDs(dir)
	if err != nil {
		return nil, err
	}
	maps.Copy(env, map[string]string{
		"CHANNEL_NAME":    channel,
		"IMAGE_NS":        ImageNS,
		"CA_IMAGE_TAG":    CAImageTag,
		"IMAGE_TAG":       t.image,
		"TOOLS_IMAGE_TAG": t.tools,
		"BASE_IMAGE_TAG":  BaseImageTag,
		"TIMEOUT":         "10000",
	})
	// byfn.sh always set CHAINCODE_VERSION, empty without -V. Keep a value
	// from the environment or .env when --cc-version is not given.
	if ccVersion != "" {
		env["CHAINCODE_VERSION"] = ccVersion
	}
	return env, nil
}

// chaincodeIDs returns CCID_<NAME> for each chaincodes/*.id file in dir,
// which generatecc writes. The builder now writes each package ID into
// docker-compose-ccaas.yaml; a compose file that v0.0.x wrote has
// "$CCID_<NAME>" instead. NAME is the file name up to its first "-", in
// upper case, as scripts/env.sh made it for the .env file.
func chaincodeIDs(dir string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "chaincodes", "*.id"))
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, f := range files {
		b, rerr := os.ReadFile(f) //nolint:gosec // a file of the network project
		if rerr != nil {
			return nil, rerr
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".id"), "-")
		ids["CCID_"+strings.ToUpper(name)] = strings.TrimSpace(string(b))
	}
	return ids, nil
}
