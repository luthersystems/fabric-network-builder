# Fabric network builder

fabric-network-builder generates a local Hyperledger Fabric network, brings
it up, and installs chaincode on it. It is one Go binary in the
`luthersystems/fabric-network-builder` image. It supports Fabric 2.5 and
later.

The binary does all the Fabric work itself:

| Step | How |
|---|---|
| Crypto material | Fabric's cryptogen code, in process |
| Channel genesis block | Fabric's configtxgen code, in process |
| Start and stop the network | The Docker Compose library, through the Docker API |
| Orderer joins the channel | The orderer's channel participation API (port 7053) |
| Peers join, chaincode install, approve and commit | The peers' gRPC admin and gateway APIs ([fabric-admin-sdk](https://github.com/hyperledger/fabric-admin-sdk)) |

So the image has no Fabric, docker or compose binary, and a project needs no
copy of the peer scripts. `third_party/fabric/README.md` tells why the
builder keeps a copy of some Fabric code.

## Run it

Mount the project directory at the same path in the container, mount the
Docker socket (for `up`, `install` and `down`), and give `--chown` so the
files you get belong to you:

```sh
PROJECT_PATH=$(pwd)
docker run --rm -it \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$PROJECT_PATH:$PROJECT_PATH" \
    -w "$PROJECT_PATH" \
    luthersystems/fabric-network-builder \
    --chown "$(id -u):$(id -g)" \
    COMMAND [OPTIONS]
```

`fabric-network-builder.sh` wraps this command. Link it into your PATH:

```bash
ln -s "$(pwd)/fabric-network-builder.sh" /usr/local/bin/fabric-network-builder
```

The builder reaches the nodes at their container addresses on the network.
When it runs in a container, it connects that container to the network for
the command and disconnects it after. Run it from the image. A binary on a
macOS host cannot reach the container addresses.

`fabric-network-builder --help` and `fabric-network-builder COMMAND --help`
list every option.

## Commands

| Command | What it does |
|---|---|
| `generate` | Writes the compose files, `configtx.yaml` and `crypto-config.yaml`, then the crypto material and the channel genesis block. `--template` writes only the files; `--no-template` makes only the material from the files you have. `--archive FILE` writes a `.tar.xz` and leaves the directory alone. |
| `extend` | Adds the crypto material for new orgs or peers in `crypto-config.yaml` and keeps the rest. |
| `up` | Starts the network, joins each orderer and each peer to the channel. |
| `install NAME VERSION "VARIANTS" /chaincodes/PKG.tar.gz` | Installs each variant's package on every peer, approves it for every org and commits it on the channel, at the next sequence. |
| `generatecc NAME VERSION "VARIANTS" /chaincodes/PKG.tar.gz` | Builds the chaincode packages and their package IDs (`chaincodes/<variant>-<version>.id`). |
| `down` | Stops the network, removes its volumes and the chaincode containers and images. |
| `cert_expiries` | Prints the expiry of each certificate. |
| `reissue` | Renews leaf certificates in place (see below). |

### Example

```sh
fabric-network-builder generate --domain-name luther.systems
fabric-network-builder up
fabric-network-builder install mycc v1.0 mycc /chaincodes/mycc-v1.0.tar.gz
fabric-network-builder down
```

The network has no orderer system channel. `generate` writes the channel's
genesis block, `channel-artifacts/<channel>.block`, with the anchor peers in
it. Only the `etcdraft` orderer type is generated.

### Fabric release

To run the network on another Fabric release than the default of the image,
set `FNB_IMAGETAG` in the container's environment:

```sh
docker run -e FNB_IMAGETAG=3.1.4 ... luthersystems/fabric-network-builder up
```

It sets the peer, orderer and ccenv image tag. A tag before 2.5 is refused.
The `cli` service runs `fabric-tools`, which Fabric 3 does not publish, so
for a 3.x tag it stays on the image's own 2.5 tag. `FNB_TOOLSIMAGETAG` sets
its tag. The builder does not use the `cli` service; it stays for your own
`docker exec` commands.

`tests/network-smoke.sh IMAGE TAG` generates a network, brings it up on that
tag, checks that every node joined the channel and commits a chaincode
definition. CI runs it on 2.5 and 3.1.

## Chaincode as a Service (CCaaS)

`generatecc --ccaas` packages each chaincode variant as a CCaaS stub and emits
`docker-compose-ccaas.yaml` with one service per variant. Every service runs
the `luthersystems/substrate:$CHAINCODE_VERSION` image by default (substrate
phyla use this path).

To run a non-substrate CCaaS container alongside substrate phyla, pass
`--image-override NAME=IMAGE` (repeatable). `NAME` must match a variant listed
in `cc_variants`; an unknown name exits with a non-zero status.

```bash
fabric-network-builder generatecc --ccaas \
    --image-override external=luthersystems/externalcc:$CHAINCODE_VERSION \
    mycc v1.0 "a b external" /chaincodes/mycc-v1.0.tar.gz
```

With that invocation the generated compose file assigns
`luthersystems/externalcc:$CHAINCODE_VERSION` to `external-peer0` and leaves
`a-peer0`/`b-peer0` on the substrate default.

## Renewing certificates

`cert_expiries` prints the expiry of every certificate in a crypto-config tree.
`reissue` renews expiring **leaf** certificates (peer/orderer/user MSP signcerts
and TLS certs) in place, for cryptogen-style trees where the issuing CA key is
on the filesystem.

It re-signs each cert with the on-disk CA, reusing the node's existing key and
copying the original subject and extensions verbatim - only the validity window
and serial number change. The MSP identity is unchanged, so there is no ledger
impact and no channel-config update: replace the files and restart the node.

```sh
# report every leaf cert's expiry and flag the expired ones (no changes)
fabric-network-builder reissue

# renew every already-expired cert, extending to the CA's expiry
fabric-network-builder reissue --all-expired

# renew specific nodes (short names match), TLS only, 2 year validity
fabric-network-builder reissue --node peer0 --node peer1 --type tls --days 730

# preview without writing
fabric-network-builder reissue --all --dry-run
```

New leaf validity is always capped at the issuing CA's expiry. `reissue` refuses
if the CA itself is expired (that needs a CA rotation and channel-config update)
or if the CA private key is not on the filesystem - fabric-ca issued material
must be renewed with `fabric-ca-client reenroll`. Replaced certs are backed up
alongside the original as `*.bak` unless `--no-backup` is given.

## Renewing certificates

`cert_expiries` prints the expiry of every certificate in a crypto-config tree.
`reissue` renews expiring **leaf** certificates (peer, orderer and user MSP
signcerts and TLS certs) in place, for cryptogen trees where the issuing CA
key is on the filesystem.

It re-signs each cert with the CA on disk. It keeps the node's key, the
subject and the extensions. Only the validity and the serial number change.
The MSP identity stays the same, so the ledger and the channel config need no
change: replace the files and restart the node.

```sh
# list each leaf cert's expiry and mark the expired ones (no changes)
fabric-network-builder reissue

# renew each expired cert, up to the CA's expiry
fabric-network-builder reissue --all-expired

# renew some nodes (short names match), TLS only, 2 years
fabric-network-builder reissue --node peer0 --node peer1 --type tls --days 730

# show what would change
fabric-network-builder reissue --all --dry-run
```

The new validity is never past the CA's expiry. `reissue` refuses when the CA
has expired (that needs a CA rotation and a channel config update) or when
the CA key is not on the filesystem (renew fabric-ca material with
`fabric-ca-client reenroll`). It keeps each replaced cert as `*.bak`, unless
you give `--no-backup`.

## Develop

```sh
make test            # go test ./...
make static-checks   # golangci-lint
make                 # build the image
```

## Releasing

Releases are tags `vMAJOR.MINOR.PATCH` cut from `main`. Pushing a tag runs
`.github/workflows/release.yml`, which builds the amd64 and arm64
`luthersystems/fabric-network-builder` images and pushes them and the
multiarch manifests to Docker Hub.

Cut a release with the `Release (tag)` workflow
(`.github/workflows/release-tag.yml`). It checks that the version is strictly
newer than the latest stable tag and that it runs on the latest `main`, writes
notes from the commits since that tag, and runs `gh release create` as the
Claude GitHub App, so the tag starts `release.yml` (a tag made with the default
`GITHUB_TOKEN` would not):

```sh
gh workflow run release-tag.yml -f version=vX.Y.Z -f dry_run=true  # preview
gh workflow run release-tag.yml -f version=vX.Y.Z
```
