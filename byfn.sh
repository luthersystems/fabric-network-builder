#!/bin/bash

# This script will orchestrate a sample end-to-end execution of the Hyperledger
# Fabric network.
#
# The end-to-end verification provisions a sample Fabric network consisting of
# two organizations, each maintaining two peers, and a Raft ordering service.
#
# This verification makes use of two fundamental tools, which are necessary to
# create a functioning transactional network with digital signature validation
# and access control:
#
# * cryptogen - generates the x509 certificates used to identify and
#   authenticate the various components in the network.
# * configtxgen - generates the requisite configuration artifacts for orderer
#   bootstrap and channel creation.
#
# Each tool consumes a configuration yaml file, within which we specify the topology
# of our network (cryptogen) and the location of our certificates for various
# configuration operations (configtxgen).  Once the tools have been successfully run,
# we are able to launch our network.  More detail on the tools and the structure of
# the network will be provided later in this document.  For now, let's get going...

# prepending $PWD/../bin to PATH to ensure we are picking up the correct binaries
# this may be commented out to resolve installed version of tools if desired
export PATH=${PWD}/../bin:${PWD}:$PATH
export FABRIC_CFG_PATH=${PWD}

# Get relevant image tag names
. /var/lib/fabric-network-builder/byfn-vars.sh

# Print the usage message
function printHelp () {
  echo "Usage: "
  echo "  byfn.sh -m up|down|restart|generate [-c <channel name>] [-t <timeout>]"
  echo "  byfn.sh -h|--help (print this message)"
  echo "    -m <mode> - one of 'up', 'down', 'restart' or 'generate'"
  echo "      - 'up'           - bring up the network with docker-compose up"
  echo "      - 'down'        - bring down the network with docker-compose down"
  echo "      - 'restart'     - restart the network"
  echo "      - 'generate'    - generate required certificates and genesis block"
  echo "      - 'extend'      - extend existing certificates in updated crypto-config.yaml"
  echo "      - 'install'     - install chaincode archives"
  echo "      - 'generatecc'  - generate chaincode archives"
  echo "    -f                      - force operation without confirmation"
  echo "    -s <dbtype>             - the database backend to use: goleveldb (default) or couchdb"
  echo "    -c <channel name>       - channel name to use (defaults to \"mychannel\")"
  echo "    -C <chaincode name>     - chaincode name to use for \"install\""
  echo "    -K <chaincode pkg name> - chaincode package name to use for \"install\""
  echo "    -V <chaincode version>  - chaincode version to use for \"install\""
  echo "    -P <chaincode path>     - chaincode path to use for \"install\" (relative to chaincode/)"
  echo "    -t <timeout>            - CLI timeout duration in microseconds (defaults to 10000)"
  echo
  echo "Typically, one would first generate the required certificates and "
  echo "genesis block, then bring up the network. e.g.:"
  echo
  echo "	byfn.sh -m generate -c <channelname>"
  echo "	byfn.sh -m up -c <channelname>"
  echo "	byfn.sh -m down -c <channelname>"
  echo
  echo "Taking all defaults:"
  echo "	byfn.sh -m generate"
  echo "	byfn.sh -m up"
  echo "	byfn.sh -m down"
  echo
  echo "Updating crypto-config.yaml to add a new peer, then extending certs:"
  echo "	byfn.sh -m extend"
  echo
}

# Ask user for confirmation to proceed
function askProceed () {
  if [ "$FORCE" == "true" ]; then
      return 0
  fi
  read -p "Continue (y/n)? " ans
  case "$ans" in
    y|Y )
      echo "proceeding ..."
    ;;
    n|N )
      echo "exiting..."
      exit 1
    ;;
    * )
      echo "invalid response"
      askProceed
    ;;
  esac
}

# Obtain CONTAINER_IDS and remove them
# TODO Might want to make this optional - could clear other containers
function clearContainers () {
  CONTAINER_IDS=$(docker ps -a | awk '($2 ~ /dev-peer.*/) {print $1}')
  if [ -z "$CONTAINER_IDS" -o "$CONTAINER_IDS" == " " ]; then
    echo "---- No containers available for deletion ----"
  else
    docker rm -f $CONTAINER_IDS
  fi
}

# Delete any images that were generated as a part of this setup
# specifically the following images are often left behind:
# TODO list generated image naming patterns
function removeUnwantedImages() {
  DOCKER_IMAGE_IDS=$(docker images | awk '($1 ~ /dev-peer.*/) {print $3}')
  if [ -z "$DOCKER_IMAGE_IDS" -o "$DOCKER_IMAGE_IDS" == " " ]; then
    echo "---- No images available for deletion ----"
  else
    docker rmi -f $DOCKER_IMAGE_IDS
  fi
}

# Versions of fabric known not to work with this release of first-network
BLACKLISTED_VERSIONS="^1\.0\. ^1\.1\.0-preview ^1\.1\.0-alpha"

# Do some basic sanity checking to make sure that the appropriate versions of fabric
# binaries/images are available.  In the future, additional checking for the presence
# of go or other items could be added.
function checkPrereqs() {
  # Note, we check configtxlator externally because it does not require a config file, and peer in the
  # docker image because of FAB-8551 that makes configtxlator return 'development version' in docker
  LOCAL_VERSION=$(configtxlator version | sed -ne 's/ Version: //p')
  DOCKER_IMAGE_VERSION=$(docker run --rm $IMAGENS/fabric-tools:$IMAGETAG peer version | sed -ne 's/ Version: //p'|head -1)

  echo "LOCAL_VERSION=$LOCAL_VERSION"
  echo "DOCKER_IMAGE_VERSION=$DOCKER_IMAGE_VERSION"

  if [ "$LOCAL_VERSION" != "$DOCKER_IMAGE_VERSION" ] ; then
     echo "=================== WARNING ==================="
     echo "  Local fabric binaries and docker images are  "
     echo "  out of  sync. This may cause problems.       "
     echo "==============================================="
  fi

  VERSION_ERRORED=""
  for UNSUPPORTED_VERSION in $BLACKLISTED_VERSIONS ; do
     echo "$LOCAL_VERSION" | grep -q $UNSUPPORTED_VERSION
     if [ $? -eq 0 ] ; then
       echo "ERROR! Local Fabric binary version of $LOCAL_VERSION does not match this newer version of BYFN and is unsupported. Either move to a later version of Fabric or checkout an earlier version of fabric-samples."
       VERSION_ERRORED="true"
     fi

     echo "$DOCKER_IMAGE_VERSION" | grep -q $UNSUPPORTED_VERSION
     if [ $? -eq 0 ] ; then
       echo "ERROR! Fabric Docker image version of $DOCKER_IMAGE_VERSION does not match this newer version of BYFN and is unsupported. Either move to a later version of Fabric or checkout an earlier version of fabric-samples."
       VERSION_ERRORED="true"
     fi
  done

  if [ -n "$VERSION_ERRORED" ] ; then
    exit 1
  fi
}

# Generate the needed certificates, the genesis block and start the network.
function networkUp () {
  checkPrereqs
  if [ ! -d "crypto-config" ]; then
    echo "ERROR !!!! Missing crypto material"
    exit 1
  fi
  CHANNEL_NAME=$CHANNEL_NAME DOCKER_PROJECT_DIR=$DOCKER_PROJECT_DIR TIMEOUT=$CLI_TIMEOUT IMAGE_NS=$IMAGENS CA_IMAGE_TAG=$CAIMAGETAG IMAGE_TAG=$IMAGETAG BASE_IMAGE_TAG=$BASEIMAGETAG CHAINCODE_VERSION=$CHAINCODE_VERSION docker-compose $COMPOSE_FILE_ARGS up -d 2>&1
  if [ $? -ne 0 ]; then
    echo "ERROR !!!! Unable to start network"
    docker logs -f cli
    exit 1
  fi
}

function createChannel () {
  docker exec -it cli /scripts/create_channel.sh $CHANNEL_NAME
  if [ $? -ne 0 ]; then
    echo "ERROR !!!! Unable to create channel $CHANNEL_NAME"
    exit 1
  fi
  echo "Network created channel $CHANNEL_NAME"
}

function joinChannel () {
  docker exec -it cli /scripts/join_channel.sh $CHANNEL_NAME
  if [ $? -ne 0 ]; then
    echo "ERROR !!!! Unable to join channel $CHANNEL_NAME"
    exit 1
  fi
  echo "Network joined channel $CHANNEL_NAME and is ready for chaincode installation"
}

function installChaincode () {
  INSTALL_CHAINCODE_ERRORED=""

  if [ -z "$CHAINCODE_NAME" ]; then
    echo "ERROR !!!! No chaincode name"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -z "$CHAINCODE_VERSION" ]; then
    echo "ERROR !!!! No chaincode version"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -z "$CHAINCODE_NAMES" ]; then
    echo "ERROR !!!! No chaincode names"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -z "$CHAINCODE_PATH" ]; then
    echo "ERROR !!!! No chaincode path"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -n "$INSTALL_CHAINCODE_ERRORED" ]; then
    exit 1
  fi

  initopt=""
  if [ "$INIT_REQUIRED" == "true" ]; then
      initopt="--init-required"
  fi

  docker exec -it cli /scripts/install.sh \
         "$CHANNEL_NAME" \
         "$CHAINCODE_VERSION" \
         "$CHAINCODE_NAMES" \
         "$CHAINCODE_PATH" \
         "$initopt"

  if [ $? -ne 0 ]; then
    echo "ERROR !!!! Install failed"
    exit 1
  fi
}

function generateChaincode () {
  INSTALL_CHAINCODE_ERRORED=""

  if [ -z "$CHAINCODE_NAME" ]; then
    echo "ERROR !!!! No chaincode name"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -z "$CHAINCODE_VERSION" ]; then
    echo "ERROR !!!! No chaincode version"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -z "$CHAINCODE_NAMES" ]; then
    echo "No chaincodes to generate, skipping..."
    exit 0 # nothing to do
  fi

  if [ -z "$CHAINCODE_PATH" ]; then
    echo "ERROR !!!! No chaincode path"
    INSTALL_CHAINCODE_ERRORED="true"
  fi

  if [ -n "$INSTALL_CHAINCODE_ERRORED" ]; then
    exit 1
  fi

  opt=""
  if [ "$CCAAS_REQUIRED" == "true" ]; then
      opt="--ccaas"
  fi

  set -x

  # NOTE: we need to generate the chaincode packages (and the pkg ID) prior
  # to running docker compose, since CCaaS requires the pkg ID up front.
  # So, we run the fabric-tools container as a one-off, instead of the cli.
  #docker run --rm -it $IMAGENS/fabric-tools:$IMAGETAG \
  #       -v ./chaincodes:/chaincodes \
  #       -v ./scripts:/scripts \
         ./scripts/generatecc.sh \
         "$CHAINCODE_VERSION" \
         "$CHAINCODE_NAMES" \
         "$CHAINCODE_PATH" \
         "$opt"

  if [ $? -ne 0 ]; then
    echo "ERROR !!!! Chaincode generate failed"
    exit 1
  fi
}


# Tear down running network
function networkDown () {
  IMAGE_NS=$IMAGENS CA_IMAGE_TAG=$CAIMAGETAG IMAGE_TAG=$IMAGETAG BASE_IMAGE_TAG=$BASEIMAGETAG docker-compose $COMPOSE_FILE_ARGS down --volumes
  if [ "$MODE" != "restart" ]; then
    # Bring down the network, deleting the volumes
    #Delete any ledger backups
    docker run -v $PWD:/tmp/first-network --rm $IMAGENS/fabric-tools:$IMAGETAG rm -Rf /tmp/first-network/ledgers-backup
    #Cleanup the chaincode containers
    clearContainers
    #Cleanup images
    removeUnwantedImages
  fi
}

# Using docker-compose-e2e-template.yaml, replace constants with private key file names
# generated by the cryptogen tool and output a docker-compose.yaml specific to this
# configuration
function replacePrivateKey () {
  # sed on MacOSX does not support -i flag with a null extension. We will use
  # 't' for our back-up's extension and depete it at the end of the function
  ARCH=`uname -s | grep Darwin`
  if [ "$ARCH" == "Darwin" ]; then
    OPTS="-it"
  else
    OPTS="-i"
  fi

  # Copy the template to the file that will be modified to add the private key
  cp docker-compose-e2e-template.yaml docker-compose-e2e.yaml

  # The next steps will replace the template's contents with the
  # actual values of the private key file names for the CAs.
  CURRENT_DIR=$PWD
  for i in $(seq 1 "$ORG_COUNT")
  do
    cd crypto-config/peerOrganizations/org"$i"."$DOMAIN_NAME"/ca/
    PRIV_KEY=$(ls *_sk)
    cd "$CURRENT_DIR"
    cp docker-compose-e2e.yaml /tmp/docker-compose-e2e.yaml
    sed $OPTS "s/CA${i}_PRIVATE_KEY/${PRIV_KEY}/g" /tmp/docker-compose-e2e.yaml
    cp /tmp/docker-compose-e2e.yaml docker-compose-e2e.yaml
  done

  # If MacOSX, remove the temporary backup of the docker-compose file
  if [ "$ARCH" == "Darwin" ]; then
    rm docker-compose-e2e.yamlt
  fi
}

# We will use the cryptogen tool to generate the cryptographic material (x509 certs)
# for our various network entities.  The certificates are based on a standard PKI
# implementation where validation is achieved by reaching a common trust anchor.
#
# Cryptogen consumes a file - ``crypto-config.yaml`` - that contains the network
# topology and allows us to generate a library of certificates for both the
# Organizations and the components that belong to those Organizations.  Each
# Organization is provisioned a unique root certificate (``ca-cert``), that binds
# specific components (peers and orderers) to that Org.  Transactions and communications
# within Fabric are signed by an entity's private key (``keystore``), and then verified
# by means of a public key (``signcerts``).  You will notice a "count" variable within
# this file.  We use this to specify the number of peers per Organization; in our
# case it's two peers per Org.  The rest of this template is extremely
# self-explanatory.
#
# After we run the tool, the certs will be parked in a folder titled ``crypto-config``.

# Generates Org certs using cryptogen tool
function generateCerts (){
  GEN_CERTS_ERRORED=""

  which cryptogen
  if [ "$?" -ne 0 ]; then
    echo "cryptogen tool not found. exiting"
    GEN_CERTS_ERRORED="true"
  fi

  if [ -d crypto-config ]; then
    echo "crypto-config certificate tree already exists"
    if [ ! "$FORCE" == "true" ]; then
      GEN_CERTS_ERRORED="true"
    fi
  fi

  if [ -n "$GEN_CERTS_ERRORED" ]; then
    exit 1
  fi

  echo
  echo "##########################################################"
  echo "##### Generate certificates using cryptogen tool #########"
  echo "##########################################################"

  if [ -d "crypto-config" ]; then
    rm -Rf crypto-config
  fi
  set -x
  cryptogen version
  cryptogen generate --config=./crypto-config.yaml
  res=$?
  set +x
  if [ $res -ne 0 ]; then
    echo "Failed to generate certificates..."
    exit 1
  fi
  echo
}

# Extend Org certs using cryptogen tool
function extendCerts (){
  EXTEND_CERTS_ERRORED=""

  which cryptogen
  if [ "$?" -ne 0 ]; then
    echo "cryptogen tool not found. exiting"
    EXTEND_CERTS_ERRORED="true"
  fi

  if [ ! -d crypto-config ]; then
    echo "crypto-config certificate tree does not exist"
    EXTEND_CERTS_ERRORED="true"
  fi

  if [ -n "$EXTEND_CERTS_ERRORED" ]; then
    exit 1
  fi

  echo
  echo "##########################################################"
  echo "#####  Extend certificates using cryptogen tool  #########"
  echo "##########################################################"

  set -x
  cryptogen version
  cryptogen extend --config=./crypto-config.yaml
  res=$?
  set +x
  if [ $res -ne 0 ]; then
    echo "Failed to generate certificates..."
    exit 1
  fi
  echo
}


# The `configtxgen tool is used to create four artifacts: orderer **bootstrap
# block**, fabric **channel configuration transaction**, and two **anchor
# peer transactions** - one for each Peer Org.
#
# The orderer block is the genesis block for the ordering service, and the
# channel transaction file is broadcast to the orderer at channel creation
# time.  The anchor peer transactions, as the name might suggest, specify each
# Org's anchor peer on this channel.
#
# Configtxgen consumes a file - ``configtx.yaml`` - that contains the definitions
# for the sample network. There are three members - one Orderer Org (``OrdererOrg``)
# and two Peer Orgs (``Org1`` & ``Org2``) each managing and maintaining two peer nodes.
# This file also specifies a consortium - ``SampleConsortium`` - consisting of our
# two Peer Orgs.  Pay specific attention to the "Profiles" section at the top of
# this file.  You will notice that we have two unique headers. One for the orderer genesis
# block - ``TwoOrgsOrdererGenesis`` - and one for our channel - ``TwoOrgsChannel``.
# These headers are important, as we will pass them in as arguments when we create
# our artifacts.  This file also contains two additional specifications that are worth
# noting.  Firstly, we specify the anchor peers for each Peer Org
# (``peer0.org1.example.com`` & ``peer0.org2.example.com``).  Secondly, we point to
# the location of the MSP directory for each member, in turn allowing us to store the
# root certificates for each Org in the orderer genesis block.  This is a critical
# concept. Now any network entity communicating with the ordering service can have
# its digital signature verified.
#
# This function will generate the crypto material and our four configuration
# artifacts, and subsequently output these files into the ``channel-artifacts``
# folder.
#
# If you receive the following warning, it can be safely ignored:
#
# [bccsp] GetDefault -> WARN 001 Before using BCCSP, please call InitFactories(). Falling back to bootBCCSP.
#
# You can ignore the logs regarding intermediate certs, we are not using them in
# this crypto implementation.

# Generate orderer genesis block, channel configuration transaction and
# anchor peer update transactions
function generateChannelArtifacts() {
  GEN_ARTIFACTS_ERRORED=""

  which configtxgen
  if [ "$?" -ne 0 ]; then
    echo "configtxgen tool not found. exiting"
    GEN_ARTIFACTS_ERRORED=""
  fi

  if [ -d channel-artifacts ]; then
    echo "channel-artifacts directory already exists"
    GEN_ARTIFACTS_ERRORED=""
  fi

  if [ -n "$GEN_ARTIFACTS_ERRORED" ]; then
    exit 1
  fi

  mkdir channel-artifacts

  echo "##########################################################"
  echo "#########  Generating Orderer Genesis block ##############"
  echo "##########################################################"
  # Note: For some unknown reason (at least for now) the block file can't be
  # named orderer.genesis.block or the orderer will fail to launch!
  set -x
  configtxgen -profile AnyOrgsOrdererGenesis -channelID byfn-sys-channel -outputBlock ./channel-artifacts/genesis.block
  res=$?
  set +x
  if [ $res -ne 0 ]; then
    echo "Failed to generate orderer genesis block..."
    exit 1
  fi
  echo
  echo "#################################################################"
  echo "### Generating channel configuration transaction 'channel.tx' ###"
  echo "#################################################################"
  set -x
  configtxgen -profile AnyOrgsChannel -outputCreateChannelTx ./channel-artifacts/channel.tx -channelID $CHANNEL_NAME
  res=$?
  set +x
  if [ $res -ne 0 ]; then
    echo "Failed to generate channel configuration transaction..."
    exit 1
  fi

  for i in $(seq 1 $ORG_COUNT)
  do
    echo
    echo "#################################################################"
    echo "#######    Generating anchor peer update for Org${i}MSP   ##########"
    echo "#################################################################"
    set -x
    configtxgen -profile AnyOrgsChannel -outputAnchorPeersUpdate ./channel-artifacts/Org${i}MSPanchors.tx -channelID $CHANNEL_NAME -asOrg Org${i}MSP
    res=$?
    set +x
    if [ $res -ne 0 ]; then
      echo "Failed to generate anchor peer update for Org${i}MSP..."
      exit 1
    fi
  done
}

# timeout duration - the duration the CLI should wait for a response from
# another container before giving up
CLI_TIMEOUT=10000
# channel name defaults to "mychannel"
CHANNEL_NAME="mychannel"
# use this as the default docker-compose yaml definition
COMPOSE_FILE_CLI=docker-compose-cli.yaml
COMPOSE_FILE_CCAAS=docker-compose-ccaas.yaml
COMPOSE_FILE_COUCH=docker-compose-couch.yaml

COMPOSE_FILE_ARGS="-f ${COMPOSE_FILE_CLI}"
if [ -f "$COMPOSE_FILE_CCAAS" ]; then
    COMPOSE_FILE_ARGS+=" -f ${COMPOSE_FILE_CCAAS}"
fi

CHAINCODE_NAME=""
CHAINCODE_PKG_NAME=""
CHAINCODE_VERSION=""
CHAINCODE_PATH=""
CHAINCODE_LANG=golang

DOMAIN_NAME=example.com
ORG_COUNT=2

# Parse commandline args
while getopts "h?fixm:s:c:t:C:K:V:W:P:d:n:l:" opt; do
  case "$opt" in
    h|\?)
      printHelp
      exit 0
    ;;
    m)  MODE=$OPTARG
    ;;
    f)
        FORCE=true
    ;;
    i)  INIT_REQUIRED=true
    ;;
    x)  CCAAS_REQUIRED=true
    ;;
    s)
        DBTYPE=$OPTARG
    ;;
    c)  CHANNEL_NAME=$OPTARG
    ;;
    C)  CHAINCODE_NAME=$OPTARG
    ;;
    K)  CHAINCODE_PKG_NAME=$OPTARG
    ;;
    V)  CHAINCODE_VERSION=$OPTARG
    ;;
    W)  CHAINCODE_NAMES=$OPTARG
    ;;
    P)  CHAINCODE_PATH=$OPTARG
    ;;
    t)  CLI_TIMEOUT=$OPTARG
    ;;
    d)  DOMAIN_NAME=$OPTARG
    ;;
    n)  ORG_COUNT=$OPTARG
    ;;
  esac
done

# Determine whether starting, stopping, restarting or generating for announce
if [ "$MODE" == "up" ]; then
  EXPMODE="Starting"
elif [ "$MODE" == "down" ]; then
  EXPMODE="Stopping"
elif [ "$MODE" == "restart" ]; then
  EXPMODE="Restarting"
elif [ "$MODE" == "extend" ]; then
  EXPMODE="Extending"
elif [ "$MODE" == "generate" ]; then
  EXPMODE="Generating certs and genesis block for"
elif [ "$MODE" == "install" ]; then
  EXPMODE="Installing chaincode tar.gz package"
elif [ "$MODE" == "generatecc" ]; then
  EXPMODE="Generating chaincode package"
else
  printHelp
  exit 1
fi

msg="${EXPMODE} with channel '${CHANNEL_NAME}' and CLI timeout of '${CLI_TIMEOUT}'"
if [ "$DBTYPE" == "couchdb" ]; then
    msg="${msg} and using couchdb"
    COMPOSE_FILE_ARGS="${COMPOSE_FILE_ARGS} -f $COMPOSE_FILE_COUCH"
fi
# Announce what was requested
echo "$msg"

# ask for confirmation to proceed
askProceed

#Create the network using docker compose
if [ "${MODE}" == "up" ]; then
  networkUp
  createChannel
  joinChannel
elif [ "${MODE}" == "install" ]; then
  installChaincode
elif [ "${MODE}" == "generatecc" ]; then
  generateChaincode
elif [ "${MODE}" == "down" ]; then ## Clear the network
  networkDown
elif [ "${MODE}" == "generate" ]; then ## Generate Artifacts
  checkPrereqs
  generateCerts
  replacePrivateKey
  generateChannelArtifacts
elif [ "${MODE}" == "extend" ]; then ## Extend Artifacts
  checkPrereqs
  extendCerts
  replacePrivateKey
elif [ "${MODE}" == "restart" ]; then ## Restart the network
  networkDown
  networkUp
  createChannel
  joinChannel
else
  printHelp
  exit 1
fi
