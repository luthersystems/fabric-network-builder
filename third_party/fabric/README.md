# Fabric code copied into the builder

The builder runs cryptogen and configtxgen in its own process, so the image
does not need the Fabric tool binaries. Fabric keeps this code in `internal/`
packages, which another module cannot import. So the builder keeps a copy.

## Source

hyperledger/fabric v3.1.4, under the Apache License 2.0 (`LICENSE`).

| Copy | Fabric source |
|---|---|
| `cryptogen/cryptogen.go` | `cmd/cryptogen/main.go` |
| `cryptogen/ca`, `cryptogen/csp`, `cryptogen/msp` | `internal/cryptogen/...` |
| `configtxgen/encoder` | `internal/configtxgen/encoder` |
| `configtxgen/genesisconfig/config.go` | `internal/configtxgen/genesisconfig/config.go` |
| `configtxlator/update` | `internal/configtxlator/update` |
| `pkg/identity` | `internal/pkg/identity` |

## Changes to the copies

`vendor.py` makes every change. Do not edit a copied file by hand.

- The import paths of the copied packages point to this directory.
- The import paths of Fabric's public packages (`common/channelconfig`,
  `msp`, `protoutil` and others) point to
  `github.com/hyperledger/fabric-x-common`, which has the same packages at
  the same paths. The `github.com/hyperledger/fabric` module has no release
  with a Go semver tag, so govulncheck reports GO-2024-3099 for any build
  that imports it (#34). A channel genesis block made this way decodes to
  the same config as one made with the Fabric packages.
- `cryptogen.go` is package `cryptogen`, not `main`. The kingpin flags are
  package variables, and each `os.Exit` is a `panic` with the same message,
  so the builder can report the error and continue.

These files are ours, and `vendor.py` does not touch them:

- `cryptogen/api.go`: `Generate` and `Extend`. They hold a lock, because
  cryptogen keeps its options in package variables, and turn a panic into an
  error.
- `configtxgen/genesisconfig/uncached.go`: `ForgetCache`. `Load` caches each
  configtx.yaml by its path. One builder process can generate more than once
  at the same path, and must read the new file each time.

## Update to a new Fabric release

```bash
git clone --branch vX.Y.Z https://github.com/hyperledger/fabric /tmp/fabric
python3 third_party/fabric/vendor.py /tmp/fabric
go build ./... && go test ./...
```

Then change `TAG` in `vendor.py` and the version above. If `vendor.py` stops
with "changed shape", the upstream `main.go` changed; update its rewrite.
