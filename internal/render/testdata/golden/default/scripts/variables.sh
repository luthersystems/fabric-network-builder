#!/usr/bin/env false

DOMAIN_NAME="example.com"
ORG_INDICES=( 1 2 )
PEER_INDICES=( 0 1 )
ORDERER_INDICES=( 0 )
ENDORSEMENT_POLICY="OR('Org1MSP.member', 'Org2MSP.member')"
