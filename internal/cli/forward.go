package cli

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

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
