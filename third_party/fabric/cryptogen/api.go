// Copyright © 2026 Luther Systems, Ltd. All right reserved.
// SPDX-License-Identifier: Apache-2.0

package cryptogen

import (
	"fmt"
	"sync"
)

// The copied cryptogen reads its options from package variables (they were
// command line flags) and stops on the first error with a panic (it was
// os.Exit). mu serializes calls and run turns the panic back into an error,
// so the copy stays close to upstream and easy to diff on a Fabric bump.
var mu sync.Mutex

func run(config []byte, dir string, set *string, f func()) (err error) {
	mu.Lock()
	defer mu.Unlock()
	configData = string(config)
	*set = dir
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(error)
			if !ok {
				e = fmt.Errorf("%v", r)
			}
			err = fmt.Errorf("cryptogen: %w", e)
		}
	}()
	f()
	return nil
}

// Generate does what `cryptogen generate --config <config> --output <dir>`
// does.
func Generate(config []byte, outputDir string) error {
	return run(config, outputDir, &outputDirFlag, generate)
}

// Extend does what `cryptogen extend --config <config> --input <dir>` does:
// it adds the nodes and users that config has and dir does not, with the
// existing CAs.
func Extend(config []byte, inputDir string) error {
	return run(config, inputDir, &inputDirFlag, extend)
}
