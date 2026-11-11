package cli

import (
	"fmt"
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// DefaultPublishAddr is the host address the network's published ports bind
// to (FNB_PUBLISH_ADDR). The nodes have test credentials and the network
// has no real authentication, so by default only this machine reaches
// them. Containers on the byfn network reach the nodes by name, not
// through these ports.
const DefaultPublishAddr = "127.0.0.1"

// publishAddr returns FNB_PUBLISH_ADDR, or DefaultPublishAddr when it is
// unset. FNB_PUBLISH_ADDR=0.0.0.0 publishes on every interface, as
// v0.1.1 and earlier did.
func publishAddr(getenv func(string) string) (string, error) {
	v := getenv("FNB_PUBLISH_ADDR")
	if v == "" {
		return DefaultPublishAddr, nil
	}
	if net.ParseIP(v) == nil {
		return "", fmt.Errorf("FNB_PUBLISH_ADDR %q is not an IP address", v)
	}
	return v, nil
}

// ccaasServices returns the services of a docker-compose-ccaas.yaml: the
// chaincode containers, which get the CHAINCODE_*, SUBSTRATE_* and OTEL_*
// variables of the builder's environment (compose.ForwardEnvPrefixes).
func ccaasServices(path string) ([]string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // a file of the network project
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err = yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	names := make([]string, 0, len(f.Services))
	for n := range f.Services {
		names = append(names, n)
	}
	return names, nil
}
