#!/usr/bin/env false

DOMAIN_NAME="example.org"
ORG_INDICES=( 1 2 3 )
PEER_INDICES=( 0 )
ORDERER_INDICES=( 0 1 2 )
ENDORSEMENT_POLICY="OR('Org1MSP.member', 'Org2MSP.member', 'Org3MSP.member')"
