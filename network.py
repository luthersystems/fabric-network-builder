#!/usr/bin/env python3
'''
network.py creates crypto artifacts, config files, scripts, and docker-compose files to run a fabric
network.  This script wraps byfn.sh and aims to replace it with improved functionality to generate
networks with custom network topologies.
'''

from datetime import datetime
from glob import glob
from itertools import groupby
from pathlib import Path
from tempfile import TemporaryDirectory
import argparse
import hashlib
import json
import os
import os.path
import shlex
import subprocess

from jinja2 import Template
from OpenSSL.crypto import load_certificate, FILETYPE_PEM

class Network(object):

    def __init__(self):
        script_dir = os.path.dirname(os.path.realpath(__file__))
        self.template_base_path = os.path.join(script_dir, 'template')
        self.chown = None
        self.byfn_path = os.path.join(script_dir, 'byfn.sh')
        self.channel = 'luther'
        self.destination_path = '.'
        self.force = False
        self.storage = None
        self.log_spec = None
        self.domain_name = 'example.com'
        self.enable_node_ous=False
        self.org_count = 2
        self.peer_count = 2
        self.min_endorsers = 0
        self.sidedb_req_peer_count = -1
        self.sidedb_max_peer_count = -1
        self.sidedb_structure = 'shared'
        self.execute_timeout = 30
        self.compose_project_name = os.environ.get('COMPOSE_PROJECT_NAME', 'fnb')

    def generate(self, args):
        if args.archive_path is not None:
            self._generate_archive(args)
        else:
            self._generate(args)

    def _generate(self, args):
        self._check_gen_dest()
        # render templates unless --no-template prevents it.
        if args.template is not False:
            self._render_template(args)
        # run cryptogen unless --template prevents it.
        if not args.template:
            byfn_cmd = self._byfn_cmd('generate')
            run((byfn_cmd + [ '-d', args.domain_name, '-n', str(args.org_count) ]), chdir=self.destination_path)
            if self.chown is not None:
                cmd = ['chown', '-R', self.chown]
                cmd.extend(self._crypto_gen_assets())
                run(cmd, chdir=self.destination_path)

    def _generate_archive(self, args):
        if args.template is not None:
            raise argparse.ArgumentError('cannot combine --[no-]template and --archive')
        with TemporaryDirectory(prefix='fabric-network', dir=self.destination_path) as d:
            orig_destination_path = self.destination_path
            self.destination_path = d
            try:
                self._generate(args)
            finally:
                self.destination_path = orig_destination_path
            cmd = ["tar", "-cJf", 'archive.tar.xz']
            cmd.extend(self._archive_filenames())
            run(cmd, chdir=d)
            mv = ['mv', os.path.join(d, 'archive.tar.xz'), args.archive_path]
            run(mv, chdir=self.destination_path)
            if self.chown is not None:
                cmd = ['chown', '-R', self.chown]
                cmd.append(args.archive_path)
                run(cmd, chdir=self.destination_path)

    def extend(self, args):
        if args.archive_path is not None:
            self._extend_archive(args)
        else:
            self._extend(args)

    def _extend(self, args):
        byfn_cmd = self._byfn_cmd('extend') + [ '-d', args.domain_name ]
        run((byfn_cmd), chdir=self.destination_path)
        if self.chown is not None:
            cmd = ['chown', '-R', self.chown]
            cmd.extend(self._crypto_gen_assets())
            run(cmd, chdir=self.destination_path)

    def _extend_archive(self, args):
        with TemporaryDirectory(prefix='fabric-network', dir=self.destination_path) as d:
            orig_destination_path = self.destination_path
            self.destination_path = d
            try:
                self._extend(args)
            finally:
                self.destination_path = orig_destination_path
            cmd = ["tar", "-cJf", 'archive.tar.xz']
            cmd.extend(self._archive_filenames())
            run(cmd, chdir=d)
            mv = ['mv', os.path.join(d, 'archive.tar.xz'), args.archive_path]
            run(mv, chdir=self.destination_path)
            if self.chown is not None:
                cmd = ['chown', '-R', self.chown]
                cmd.append(args.archive_path)
                run(cmd, chdir=self.destination_path)

    def _archive_filenames(self):
        return ['crypto-config', 'crypto-config.yaml',
                'configtx.yaml', 'channel-artifacts',
                'scripts']

    def _check_gen_dest(self):
        if self.force:
            return

        check_files = ['crypto-config.yaml', 'configtx.yaml', 'crypto-config', 'channel-artifacts']
        for fname in check_files:
            dest_path = os.path.join(self.destination_path, fname)
            if os.path.exists(dest_path):
                # TODO: This probably should prompt to continue and wipe things out if so
                raise Exception('generated artifacts already exist')

    def _rendered_path(self, path):
        return os.path.join(self.destination_path, path)

    def _chown_maybe(self, fn):
        if self.chown is not None:
            run(['chown', self.chown, fn])

    def _render_template(self, args):
        def mkdir(name):
            path = os.path.join(self.destination_path, name)
            run(['mkdir', '-p', path])
            self._chown_maybe(path)
        dirs = ['base', 'chaincodes', 'couchdb', 'scripts']
        for basename in dirs:
            mkdir(basename)
        connect_domain_name = args.connect_domain_name or args.domain_name
        def make_orderer(i):
            return {'host': 'orderer{}.{}'.format(i, connect_domain_name),
                    'dir': 'orderer{}.{}'.format(i, args.domain_name),
                    'port': '7050',
                    'server_cert_path': '',
                    'client_cert_path': ''}
        orderers = [make_orderer(i) for i in range(0, args.orderer_count)]
        orderer_addresses = [('{}:{}').format(o['host'], o['port']) for o in orderers]
        org_indices = list(map(str, range(1, (args.org_count + 1))))
        ca_ports = list(map((lambda x : str((1000 * x) + 6054)), range(1, (args.org_count + 1))))
        peer_indices = list(map(str, range(0, args.peer_count)))
        orderer_indices = [i for i in range(0, args.orderer_count)]
        orderer_ports = [ str(int(orderers[i]['port'])+(1000 * i)) for i in orderer_indices ]
        # ijbp: i = index of org, j = index of peer, b = bootstrap peer, p = port prefix
        # ijbp is used in docker-compose-base
        ijbp = []
        p = 70
        for i in range(1, (args.org_count + 1)):
            for j in range(0, args.peer_count):
                b = '0' # TODO: analyze whether there is a benefit to setting the bootstrap peer differently
                ijbp.append([str(i), str(j), str(b), str(p)])
                p += 10
        sidedb_req_peer_count = args.req_peer_count
        if sidedb_req_peer_count == -1:
            sidedb_req_peer_count = ((args.org_count * args.peer_count) // 2)
        sidedb_max_peer_count = args.max_peer_count
        if sidedb_max_peer_count == -1:
            sidedb_max_peer_count = ((args.org_count * args.peer_count) - 1)
        policy_other_users = ["'Org{}MSP.member'".format(i) for i in range(2, (args.org_count + 1))]
        policy_other_users_str = ", ".join(policy_other_users)
        policy_users = ["'Org{}MSP.member'".format(i) for i in range(1, (args.org_count + 1))]
        policy_users_str = ", ".join(policy_users)
        if args.private_structure.startswith("nchoose2common,"):
            vanity = args.private_structure.split(",")[1:]
            if len(vanity) != args.org_count:
                raise Exception("improper length of vanity list")
            endorsement_policy = "Or('Org1MSP.member', OutOf(2, {}))".format(policy_other_users_str)
            collections = []
            for i in range(2, (args.org_count + 1)):
                for j in range((i + 1), (args.org_count + 1)):
                    collection = _private_collection(
                        "{}_{}".format(vanity[i-1], vanity[j-1]),
                        "OR('Org{}MSP.member','Org{}MSP.member','Org{}MSP.member')".format(1, i, j),
                        sidedb_req_peer_count,
                        sidedb_max_peer_count
                    )
                    collections.append(collection)
        elif args.private_structure == "nchoose2":
            endorsement_policy = "OutOf(2, {})".format(policy_users_str)
            collections = []
            for i in range(1, (args.org_count + 1)):
                for j in range((i + 1), (args.org_count + 1)):
                    policy_users = ["'Org{}MSP.member'".format(i) for i in (i, j)]
                    policy_users_str = ", ".join(policy_users)
                    collection = _private_collection(
                        "org{}org{}".format(i, j),
                        "OR({})".format(policy_users_str),
                        sidedb_req_peer_count,
                        sidedb_max_peer_count
                    )
                    collections.append(collection)
        else:
            endorsement_policy = "OR({})".format(policy_users_str)
            collection = _private_collection(
                "private",
                "OR({})".format(policy_users_str),
                sidedb_req_peer_count,
                sidedb_max_peer_count
            )
            collections = [collection]
        collections_json = json.dumps(collections, indent=4)
        # use --min-endorsers=-1 for automatic majority calculation
        if args.min_endorsers == -1:
            args.min_endorsers = (((args.org_count * args.peer_count) // 2) + 1)
        jinja_files = [
            'crypto-config.yaml', 'configtx.yaml',
            'shiroclient.yaml', 'shiroclient_fast.yaml',
            'fabric-client.yaml', 'fabric-client_fast.yaml', 'fabric-client_template.yaml',
            'docker-compose-e2e-template.yaml',
            'docker-compose-cli.yaml',
            'docker-compose-couch.yaml',
            'base/docker-compose-base.yaml',
            'scripts/variables.sh',
            'collections.json',
            'core.yaml',
        ]
        for jinja_file in jinja_files:
            template_file = jinja_file + '.j2'
            print("rendering template {}".format(template_file))
            with open(os.path.join(self.template_base_path, template_file)) as src_file:
                template = Template(src_file.read())
            with open(os.path.join(self.destination_path, jinja_file), 'w') as dst_file:
                dst_file.write(template.render(CC_NAME=args.cc_name,
                                               DOMAIN_NAME=args.domain_name,
                                               CONNECT_DOMAIN_NAME=connect_domain_name,
                                               ENABLE_NODE_OUS=args.enable_node_ous,
                                               ORG_COUNT=str(args.org_count),
                                               ORG_INDICES=org_indices,
                                               ZIP_ORG_INDICES_CA_PORTS=zip(org_indices, ca_ports),
                                               PEER_COUNT=str(args.peer_count),
                                               PEER_INDICES=peer_indices,
                                               ORDERER_COUNT=str(args.orderer_count),
                                               ORDERER_INDICES=orderer_indices,
                                               ZIP_ORDERER_INDICES_ORDERER_PORTS=zip(orderer_indices, orderer_ports),
                                               ORDERERORGS_TEMPLATE_COUNT=str(args.orderer_count),
                                               ORDERERS=orderers,
                                               ORDERER_TYPE=args.orderer_type,
                                               ORDERER_ADDRESSES=json.dumps(orderer_addresses),
                                               IJBP=ijbp,
                                               ENDORSEMENT_POLICY=endorsement_policy,
                                               COLLECTIONS_JSON=collections_json,
                                               MIN_ENDORSERS=str(args.min_endorsers),
                                               EXECUTE_TIMEOUT=(str(args.execute_timeout)+"s"),
                                               ORDERER_SAN_DOMAINS=args.orderer_san_domains,
                                               PEER_SAN_DOMAINS=args.peer_san_domains,
                               ) + "\n")
                self._chown_maybe(os.path.join(self.destination_path, jinja_file))
        nonjinja_files = [ 'base/peer-base.yaml',
                           'couchdb/local.ini',
                           'scripts/channel.sh',
                           'scripts/create_channel.sh',
                           'scripts/init.sh',
                           'scripts/install.sh',
                           'scripts/generatecc.sh',
                           'scripts/env.sh',
                           'scripts/join_channel.sh',
                           'scripts/luther_utils.sh' ]
        for nonjinja_file in nonjinja_files:
            run(['cp', os.path.join(self.template_base_path, nonjinja_file), os.path.join(self.destination_path, nonjinja_file)])
            self._chown_maybe(os.path.join(self.destination_path, nonjinja_file))
        executable_files = [ 'scripts/channel.sh',
                             'scripts/create_channel.sh',
                             'scripts/init.sh',
                             'scripts/install.sh',
                             'scripts/generatecc.sh',
                             'scripts/env.sh',
                             'scripts/join_channel.sh', ]
        for executable_file in executable_files:
            run(['chmod', '+x', os.path.join(self.destination_path, executable_file)])

    def _crypto_gen_assets(self):
        return ['crypto-config', 'channel-artifacts', 'docker-compose-e2e.yaml']

    def up(self, args):
        byfn_cmd = self._byfn_cmd('up')
        append_opt(byfn_cmd, '-V', args.cc_version)
        if args.log_spec:
            append_opt(byfn_cmd, '-l', args.log_spec)
        run(byfn_cmd, chdir=self.destination_path, setenv=self._compose_setenv())

    def install(self, args):
        '''
        params:
            init_required: bool
            cc_pkg_name: string
            cc_name: string
            cc_version: string
            cc_variants: string
            cc_path: string
        '''
        byfn_cmd = self._byfn_cmd('install')
        append_opt(byfn_cmd, '-C', args.cc_name)
        append_opt(byfn_cmd, '-K', args.cc_pkg_name)
        append_opt(byfn_cmd, '-V', args.cc_version)
        append_opt(byfn_cmd, '-W', args.cc_variants)
        append_opt(byfn_cmd, '-P', args.cc_path)
        if args.init_required:
            byfn_cmd.append('-i')
        run(byfn_cmd, chdir=self.destination_path, setenv=self._compose_setenv())

    def generate_chaincodes(self, args):
        '''
        params:
            ccaas: bool
            cc_name: string
            cc_version: string
            cc_variants: string
            cc_path: string
        '''
        byfn_cmd = self._byfn_cmd('generatecc')
        append_opt(byfn_cmd, '-C', args.cc_name)
        append_opt(byfn_cmd, '-V', args.cc_version)
        append_opt(byfn_cmd, '-W', args.cc_variants)
        append_opt(byfn_cmd, '-P', args.cc_path)
        if args.ccaas:
            byfn_cmd.append('-x')
        run(byfn_cmd, chdir=self.destination_path, setenv=self._compose_setenv())
        self.generate_chaincodes_compose(args.cc_variants.split())

    def generate_chaincodes_compose(self, chaincode_names):
        if len(chaincode_names) == 0:
            print("skipping ccaas compose file...")
            return
        # Define the base port for the external chaincodes
        base_port = 9080

        # Prepare the data for the template
        chaincodes_data = []
        for idx, cc_name in enumerate(chaincode_names):
            chaincodes_data.append({
                'service_name': f"{cc_name}-peer0",
                'ccid_env_var': f"CCID_{cc_name.upper()}",
                'port': base_port + idx
            })

        # Load and render the Jinja template
        template_file = 'docker-compose-ccaas.yaml.j2'
        with open(os.path.join(self.template_base_path, template_file)) as src_file:
            template = Template(src_file.read())

        docker_compose_content = template.render(chaincodes=chaincodes_data)

        # Write the rendered content to a file
        compose_file_path = os.path.join(self.destination_path, 'docker-compose-ccaas.yaml')
        with open(compose_file_path, 'w') as dst_file:
            dst_file.write(docker_compose_content)

        self._chown_maybe(compose_file_path)

    def down(self, args):
        byfn_cmd = self._byfn_cmd('down')
        run(byfn_cmd, chdir=self.destination_path, setenv=self._compose_setenv())
        containers = capture(['bash', '-c',
                              """docker ps -a | grep dev-peer[0-9] | awk '{print $1}'"""])
        if containers:
            run(['docker', 'rm', '--force', '--volumes'] + containers.decode('utf-8').split())
        images = capture(['bash', '-c',
                          """docker images \\
                          | grep "dev\\|none\\|test-vp\\|peer[0-9]-" \\
                          | awk '{print $3}'"""])
        if images:
            run(['docker', 'rmi'] + images.decode('utf-8').split())

    def cert_expiries(self, args):
        for e, p in cert_expiries('crypto-config'):
            print('{}\t{}'.format(e, p))

    def config_parameters(self):
        return ['channel']

    def _compose_setenv(self):
        '''
        The way byfn.sh gets invokes appears to leave docker-compose unable to determine a proper
        COMPOSE_PROEJECT_NAME so we must make sure to set it explicitly in all processes which will
        exec docker-compose.
        '''
        return {'COMPOSE_PROJECT_NAME': self.compose_project_name}

    def _byfn_cmd(self, name):
        return self._byfn_base_cmd() + ['-m', name]

    def _byfn_base_cmd(self):
        cmd = ['bash', self.byfn_path, '-c', self.channel]
        if self.force:
            cmd.append('-f')
        if self.storage:
            cmd.append('-s')
            cmd.append(self.storage)
        return cmd

    def main(self):
        parser = argparse.ArgumentParser()
        # TODO: read a yaml config
        parser.add_argument('--config', help='configuration file path')
        parser.add_argument('--channel', help='the channel used on the network',
                            default=self.channel)
        parser.add_argument('--chown', help='set the user:group of generated files',
                            default=self.chown)
        parser.add_argument('--force', '-f', help='force the operation without confirmation',
                            action='store_true',
                            default=self.force)
        parser.add_argument('--storage', '-s', help='set the database backend to use',
                            default=self.storage)
        subparsers = parser.add_subparsers(dest='command')
        subparsers.required = True
        parser_gen = subparsers.add_parser('generate', help='generate a new network')
        parser_gen.add_argument('--cc-name', help='chaincode name',
                                default="com_luthersystems_chaincode_substrate01")
        parser_gen.add_argument('--domain-name', help='infrastructure domain name',
                                default=self.domain_name)
        parser_gen.add_argument('--connect-domain-name',
                                help='configured domain name when connecting to peers and orderers')
        parser_gen.add_argument('--node-ous', help='enable node OUs (disabled by default)',
                                action='store_true',
                                dest='enable_node_ous',
                                default=self.enable_node_ous)
        parser_gen.add_argument('--no-node-ous', help='disable node OUs (disabled by default)',
                                action='store_false',
                                dest='enable_node_ous',
                                default=self.enable_node_ous)
        parser_gen.add_argument('--org-count', help='number of organizations',
                                type=int,
                                default=self.org_count)
        parser_gen.add_argument('--peer-count', help='number of peers per organization',
                                type=int,
                                default=self.peer_count)
        parser_gen.add_argument('--min-endorsers', help='minimum number of transaction endorsers',
                                type=int,
                                default=self.min_endorsers)
        parser_gen.add_argument('--private-structure', help='structure of private collections set',
                                type=str,
                                default=self.sidedb_structure)
        parser_gen.add_argument('--req-peer-count', help='minimum number of private data dissemination peers',
                                type=int,
                                default=self.sidedb_req_peer_count)
        parser_gen.add_argument('--max-peer-count', help='maximum number of private data dissemination peers',
                                type=int,
                                default=self.sidedb_max_peer_count)
        parser_gen.add_argument('--execute-timeout', help='chaincode execute timeout',
                                type=int,
                                default=self.execute_timeout)
        parser_gen.add_argument('--orderer-type', help='orderer cluster type (etcdraft or solo) ',
                                default='etcdraft')
        parser_gen.add_argument('--orderer-count', help='number of orderer servers to generate config for',
                                type=int,
                                default=1)
        parser_gen.add_argument('--template', help='only render the network template. do not generate crypto assets',
                                action='store_true',
                                default=None,
                                dest='template')
        parser_gen.add_argument('--no-template', help='do not render a template. only generate crypto assets',
                                action='store_false',
                                default=None,
                                dest='template')
        parser_gen.add_argument('--archive', '-a', help='generate a tar.xz for distribution',
                                dest='archive_path')
        parser_gen.add_argument('--orderer-san-domains', nargs='+',
                                help='domain suffixes to add to SAN field of orderer certificates')
        parser_gen.add_argument('--peer-san-domains', nargs='+',
                                help='domain suffixes to add to SAN field of orderer certificates')
        parser_gen.set_defaults(func=self.generate)

        parser_ext = subparsers.add_parser('extend', help='extend an existing network')
        parser_ext.add_argument('--archive', '-a', help='generate a tar.xz for distribution',
                                dest='archive_path')
        parser_ext.add_argument('--domain-name', help='infrastructure domain name',
                                default=self.domain_name)
        parser_ext.set_defaults(func=self.extend)

        parser_up = subparsers.add_parser('up', help='launch a network')
        parser_up.add_argument('--log-spec', help='set FABRIC_LOGGING_SPEC value',
                               type=str, default=self.log_spec)
        parser_up.add_argument('--cc-version', help='chaincode version (for CCAAS)')
        parser_up.set_defaults(func=self.up)
        parser_install = subparsers.add_parser('install', help='install a chaincode archive (.tar.gz)')
        parser_install.add_argument('--init-required', help='set chaincode to require init',
                                    action='store_true')
        parser_install.add_argument('--cc-pkg-name', help='chaincode package name (part of label)',
                                    default="com_luthersystems_chaincode_substrate01")
        parser_install.add_argument('cc_name', help='chaincode name used to invoke its methods')
        parser_install.add_argument('cc_version', help='deployment version')
        parser_install.add_argument('cc_variants', help='deployment variants')
        parser_install.add_argument('cc_path', help='path to the packaged chaincode tarball')
        parser_install.set_defaults(func=self.install)

        parser_generatecc = subparsers.add_parser('generatecc', help='generate chaincode archives (.tar.gz)')
        parser_generatecc.add_argument('--ccaas', help='use chaincode as a service',
                                    action='store_true')
        parser_generatecc.add_argument('cc_name', help='chaincode name used to invoke its methods')
        parser_generatecc.add_argument('cc_version', help='deployment version')
        parser_generatecc.add_argument('cc_variants', help='deployment variants')
        parser_generatecc.add_argument('cc_path', help='path to the packaged chaincode tarball')
        parser_generatecc.set_defaults(func=self.generate_chaincodes)

        parser_down = subparsers.add_parser('down', help='teardown network containers')
        parser_down.set_defaults(func=self.down)
        parser_cert_expiries = subparsers.add_parser('cert_expiries', help='print expiration values for certs')
        parser_cert_expiries.set_defaults(func=self.cert_expiries)

        args = parser.parse_args()
        for k, v in vars(args).items():
            if k in vars(self):
                vars(self)[k] = v
        args.func(args)


def envsubst(in_path, out_path, submap):
    cmd = ['bash', '-c', 'envsubst ' + ("'" + (" ".join(map((lambda x: ("$" + x)), submap.keys()))) + "'") + ' < ' + shlex.quote(in_path)]
    output = capture(cmd, setenv=submap)
    with open(out_path, 'w') as f:
        f.truncate()
        f.write(output.decode('utf-8'))


def run(cmd, chdir=None, env=None, setenv=None):
    if env is None:
        env = os.environ.copy()
    if setenv is not None:
        for k, v in setenv.items():
            env[k] = v
    print(' '.join(map(shlex.quote, cmd)))
    subprocess.check_call(cmd, cwd=chdir, env=env)


def capture(cmd, chdir=None, env=None, setenv=None):
    if env is None:
        env = os.environ.copy()
    if setenv is not None:
        for k, v in setenv.items():
            env[k] = v
    print(' '.join(map(shlex.quote, cmd)))
    return subprocess.check_output(cmd, cwd=chdir, env=env)


def append_opt(cmd, opt, value):
    cmd.extend((opt, value))

def _private_collection(name, policy, sidedb_req_peer_count, sidedb_max_peer_count):
    return {
        "name": name,
        "policy": policy,
        "requiredPeerCount": sidedb_req_peer_count,
        "maxPeerCount": sidedb_max_peer_count,
        "blockToLive": 0,
        "memberOnlyRead": False,
        "memberOnlyWrite": False,
    }

def cert_expiries(path):
    p = Path(path)
    certs = list(p.glob('**/*.pem')) + list(p.glob('**/*.crt'))

    date_in_fmt = '%Y%m%d%H%M%SZ'
    date_out_fmt = '%Y-%m-%dT%H:%M:%SZ'

    # group cert files by their md5 hash
    certs_by_hashes = dict()
    for c in certs:
        cert_bytes = c.read_bytes()
        h = hashlib.md5()
        h.update(cert_bytes)
        md5 = h.hexdigest()
        x509 = load_certificate(FILETYPE_PEM, cert_bytes)
        cert_expiry = x509.get_notAfter().decode()
        if md5 not in certs_by_hashes:
            certs_by_hashes[md5] = {
                'paths': list(),
                'expiry': datetime.strptime(cert_expiry, date_in_fmt).strftime(date_out_fmt)
            }
            certs_by_hashes[md5]['paths'].append(c)

    k = lambda p: len(p.parts)

    # for each cert hash, take the matching paths with shortest component length
    # and store them as a tuple with the cert's expiration date
    certs_info = []
    for certs_group in certs_by_hashes.values():
        paths = sorted(certs_group['paths'], key=k)
        shortest = list(next(groupby(paths, k))[1])
        expiry = certs_group['expiry']
        for c in shortest:
            certs_info.append([expiry, str(c)])

    return sorted(certs_info)

if __name__ == '__main__':
    Network().main()
