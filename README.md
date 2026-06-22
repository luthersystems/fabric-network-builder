# Fabric network builder

## Setup

You must have fabric checked out at `$GOPATH/src/github.com/hyperledger/fabric`
and the branch `luthersystems/master` must be present (typically fetched from
the remote `git@github.com:luthersystems/fabric.git`).

Build the luthersystems/fabric-network-builder container.

```bash
make
```

Mount your project directory under an identical path in the
fabric-network-builder container and invoke the container with option --chown
in order to have fabric-network-builder set the file owner/group correctly. In
order for the container to run docker (compose) commands /var/run/docker.sock
also needs to be mounted (for `up`, `down`, and `install`).

```sh
PROJECT_PATH=$(pwd)
docker run --rm -it \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$PROJECT_PATH:$PROJECT_PATH" \
    -w "$PROJECT_PATH" \
    luthersystems/fabric-network-builder \
    --chown $(id -u $USER):$(id -g $USER) \
    COMMAND [OPTIONS]
```

This pattern is best wrapped in a custom script. This project provides such a
script as `fabric-network-builder.sh`. The wrapper script may be installed by
refrencing it with a symlink inside the machine's PATH.

```bash
ln -s $(pwd)/fabric-network-builder.sh /usr/local/bin/fabric-network-builder
```

Usage examples here assume this setup and will invoke the docker container
using the command `fabric-network-builder`.

## Example

Generate artifacts network artifacts (mainly crypto-config)

```sh
fabric-network-builder generate
```

Launch the docker(-compose) network and join the default channel "luther" with
all peers.

```sh
fabric-network-builder up
```

fabric-network-builder expects chaincode source to be placed in the
`chaincodes/` directory as a CAR file when installing. Copy CAR files from
the chaintools `build/` directory

```sh
CHAINCODE_PATH=/path/to/cc/app
CAR=com.luthersystems.chaincode.substrate01-0.0.1-SNAPSHOT.car
cp $CHAINCODE_PATH/build/$CAR ./chaincodes/
fabric-network-builder install substrate01 v0.0.1-SNAPSHOT $CAR
```

Initialize the chaincode using the fabric-client.yaml configuration file
produced by fabric-network-builder to configure fabric-sdk-go.

```sh
shiroclient --config=/path/to/shiroclient.yaml \
    --fabric.client-config=fabric-client.yaml \
    init /path/to/substrate/phylum.zy
```

**NOTE:
See shiroclient for information about proper the contents of shiroclient.yaml**

Terminate the docker network and destroy all containers when running the
containers is no longer necessary.

```sh
fabric-network-builder down
```

Get fabric repos and checkout the v1.0.0 release.
Note that the Github URLs provided are using SSH. Replace git@ with https:// if using https.

```bash
git clone git@github.com:hyperledger/fabric.git $GOPATH/src/github.com/hyperledger/fabric
git clone git@github.com:hyperledger/fabric-ca.git $GOPATH/src/github.com/hyperledger/fabric-ca
cd $GOPATH/src/github.com/hyperledger/fabric
git checkout v1.0.0
cd $GOPATH/src/github.com/hyperledger/fabric-ca
git checkout v1.0.0
```

Build fabric images and utilities.

```bash
cd $GOPATH/src/github.com/hyperledger/fabric
make docker
make release-all
export PATH="$PATH:$GOPATH/src/github.com/hyperledger/fabric/release/$(go env GOOS)-$(go env GOARCH)/bin"
```

Build the fabric-ca image.

```bash
cd $GOPATH/src/github.com/hyperledger/fabric-ca
make docker
```

## Running chaincode

**NOTE**: These instructions are deprecated and no longer directly apply

## Network setup

Run byfn.sh to generate network artifacts and bring the network up.

```bash
./byfn.sh -m generate
./byfn.sh -m up
```

## Chaincode installation

To install chaincode CAR packages copy the files to the chaincode directory and
run byfn.sh to install the chaincode.

```bash
cp /path/to/chaincode.car ./chaincode/chaincode.car
./byfn.sh -m install -C mycc -V v1.0 -P chaincode.car
```

Instead of running byfn.sh the script can be installed by entering the "cli"
container.

```bash
docker exec -it cli bash ./scripts/install.sh mychannel mycc v1.0 chaincode.car
```

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
    mycc v1.0 "a b external" /path/to/chaincode.car
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

## Network teardown

When finished using the network use byfn.sh to stop/remove containers and
delete channel artifacts.

```bash
byfn.sh -m down
```

## Full example

Destroy any existing network and generate a new one

```bash
./byfn.sh -m down -f
./byfn.sh -m generate -f
./byfn.sh -m up -f
```

Install the prop chaincode.

```bash
# optional step if the chaincode has been altered and rebuilt
cp $GOPATH/src/github.com/luthersystems/ProjectProp/Blockchain/prop-chaincodes/prop01/app/build/com.luthersystems.chaincode.prop01-0.0.18-SNAPSHOT.car chaincode/

./byfn.sh -m install -f -C prop01 -V v0.0.18-SNAPSHOT -P com.luthersystems.chaincode.prop01-0.0.18-SNAPSHOT.car
```

Instantiate the chaincode.

**NOTE:** This requires `prop01` cli to be installed which reads `prop01.yml` for configuration.

```bash
prop01 Instantiate '{"metadata":{"timestamp":"2017-08-17T01:05:22Z"}}'
```

Create a location for volume mounts

```bash
mkdir -p dockertmp/msp
mkdir -p dockertmp/enroll_user
chmod -R 777 dockertmp
```

Bring up the application network

```bash
chmod -R a+r crypto-config
docker-compose -f docker-compose-prop.yaml up
```

Tear down the application network

```bash
docker-compose -f docker-compose-prop.yaml down
sudo rm -r dockertmp/*/*
```
