#!/usr/bin/env false

DOMAIN_NAME="example.com"
ORG_INDICES=( 1 2 3 4 )
PEER_INDICES=( 0 )
ORDERER_INDICES=( 0 )
ENDORSEMENT_POLICY="Or('Org1MSP.member', OutOf(2, 'Org2MSP.member', 'Org3MSP.member', 'Org4MSP.member'))"
