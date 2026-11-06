// This file is not a Fabric copy; vendor.py does not touch it.

package genesisconfig

// ForgetCache drops the parsed configtx.yaml files. Load caches each file by
// the path viper reports, which is relative when the config path is. The
// configtxgen command loads one file per process, so the cache never goes
// stale there. The builder runs many generates in one process (tests, and a
// regenerate after --force), so a cached file at the same path would give
// the old topology.
func ForgetCache() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.cache = make(map[string][]byte)
}
