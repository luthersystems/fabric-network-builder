package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
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

// imageTags are the image namespace and tags the compose files read.
type imageTags struct {
	image, tools string
	// ns is the registry and namespace in front of every Fabric image
	// name, "hyperledger" (Docker Hub) by default.
	ns string
	// ca is the fabric-ca tag and base the fabric-couchdb tag. Fabric CA
	// and CouchDB release apart from Fabric, so FNB_IMAGETAG does not
	// move them.
	ca, base string
	// baseos is the fabric-baseos tag the peers run Go chaincode in. It
	// is the two-part Fabric version, as the peer's own default
	// $(TWO_DIGIT_VERSION) is, so the peers keep running the same image;
	// the compose files set it only so that the image takes IMAGE_NS.
	baseos string
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
//
// FNB_IMAGENS replaces the image namespace. A consumer that pulls Fabric
// images through a mirror (an ECR pull-through cache of Docker Hub, as
// luthersystems/acre does) cannot use the fixed "hyperledger" namespace:
// Docker Hub rate limits and refuses pulls from a network without
// internet access. The value is a namespace ("hyperledger") or a registry
// and path ("123456789012.dkr.ecr.us-east-1.amazonaws.com/docker-hub/hyperledger").
// FNB_CAIMAGETAG and FNB_BASEIMAGETAG override the fabric-ca and
// fabric-couchdb tags, because a mirror can hold other tags than the
// image's defaults. With none of them set, the output is the same as
// before (#45).
func resolveImageTags(getenv func(string) string) (imageTags, error) {
	t := imageTags{image: ImageTag, ns: ImageNS, ca: CAImageTag, base: BaseImageTag}
	if v := getenv("FNB_IMAGENS"); v != "" {
		if err := checkImageNS(v); err != nil {
			return t, err
		}
		t.ns = v
	}
	for _, o := range []struct {
		dst  *string
		name string
	}{{&t.ca, "FNB_CAIMAGETAG"}, {&t.base, "FNB_BASEIMAGETAG"}} {
		v := getenv(o.name)
		if v == "" {
			continue
		}
		if !tagRE.MatchString(v) {
			return t, fmt.Errorf("%s %q is not an image tag", o.name, v)
		}
		*o.dst = v
	}
	if v := getenv("FNB_IMAGETAG"); v != "" {
		t.image = v
	}
	if !fabricVersionAtLeast25(t.image) {
		return t, fmt.Errorf("fabric %s is not supported: the builder supports Fabric 2.5 and later", t.image)
	}
	t.baseos = twoDigitVersion(t.image)
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

// twoDigitVersion returns "2.5" for "2.5.16", as Fabric's
// TWO_DIGIT_VERSION does. A tag it cannot read is returned as it is.
func twoDigitVersion(tag string) string {
	parts := strings.SplitN(tag, ".", 3)
	if len(parts) < 2 {
		return tag
	}
	return parts[0] + "." + parts[1]
}

// The parts of an image reference, from the distribution reference
// grammar that Docker uses (github.com/distribution/reference).
var (
	tagRE      = regexp.MustCompile(`^[\w][\w.-]{0,127}$`)
	pathPartRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	hostRE     = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*(?::[0-9]+)?$`)
)

// checkImageNS refuses an FNB_IMAGENS value that cannot be the start of
// an image reference. The compose files put "/fabric-peer:<tag>" after
// it, so a bad value shows up only when Docker pulls, with an error that
// does not name FNB_IMAGENS. The value is path parts, lowercase, with an
// optional registry host first. As Docker does, the first part is a host
// when it has a "." or a port, or is "localhost". A port on a bare name
// ("hyperledger:25") is refused: it reads as a tag, and Docker would
// look for a registry host named "hyperledger".
func checkImageNS(ns string) error {
	parts := strings.Split(ns, "/")
	first := parts[0]
	host := strings.ContainsAny(first, ".:") || first == "localhost"
	if host {
		name, _, _ := strings.Cut(first, ":")
		if !hostRE.MatchString(first) || !strings.Contains(name, ".") && name != "localhost" {
			return fmt.Errorf("FNB_IMAGENS %q: %q is not a registry host (a tag goes in FNB_IMAGETAG)", ns, first)
		}
		parts = parts[1:]
	}
	for _, p := range parts {
		if !pathPartRE.MatchString(p) {
			return fmt.Errorf("FNB_IMAGENS %q: %q is not a lowercase image path part (no tag, no empty part)", ns, p)
		}
	}
	return nil
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
		"CHANNEL_NAME":     channel,
		"IMAGE_NS":         t.ns,
		"CA_IMAGE_TAG":     t.ca,
		"IMAGE_TAG":        t.image,
		"TOOLS_IMAGE_TAG":  t.tools,
		"BASE_IMAGE_TAG":   t.base,
		"BASEOS_IMAGE_TAG": t.baseos,
		"TIMEOUT":          "10000",
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
