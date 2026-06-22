import argparse
import os
import sys
import tempfile
import unittest
from datetime import datetime, timezone, timedelta
from pathlib import Path

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec, ed25519
from cryptography.x509.oid import NameOID, ExtendedKeyUsageOID

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from network import (  # noqa: E402
    Network,
    build_reissued_cert,
    discover_leaf_certs,
    resolve_ca,
    _backup_path,
)

UTC = timezone.utc


# --------------------------------------------------------------------------
# fixture helpers - build real on-disk cryptogen-style trees with cryptography
# --------------------------------------------------------------------------

def _name(cn, ou=None):
    attrs = []
    if ou:
        attrs.append(x509.NameAttribute(NameOID.ORGANIZATIONAL_UNIT_NAME, ou))
    attrs.append(x509.NameAttribute(NameOID.COMMON_NAME, cn))
    return x509.Name(attrs)


def _pem_cert(cert):
    return cert.public_bytes(serialization.Encoding.PEM)


def _pem_key(key):
    return key.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.PKCS8,
        serialization.NoEncryption())


def _write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def _mk_ca(cn, not_after, key=None):
    key = key or ec.generate_private_key(ec.SECP256R1())
    subject = _name(cn)
    not_before = not_after - timedelta(days=3650)
    hash_alg = None if isinstance(key, ed25519.Ed25519PrivateKey) else hashes.SHA256()
    cert = (
        x509.CertificateBuilder()
        .subject_name(subject)
        .issuer_name(subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(not_before)
        .not_valid_after(not_after)
        .add_extension(x509.BasicConstraints(ca=True, path_length=None), True)
        .add_extension(
            x509.SubjectKeyIdentifier.from_public_key(key.public_key()), False)
        .sign(key, hash_alg)
    )
    return key, cert


# A representative leaf extension profile: KeyUsage is critical (Fabric leaves
# carry a critical KeyUsage), EKU is non-critical - so tests can prove both the
# value and the critical flag survive reissue.
def _mk_leaf(cn, ou, ca_key, ca_cert, not_after, sans=None, not_before=None):
    key = ec.generate_private_key(ec.SECP256R1())
    not_before = not_before or (datetime.now(UTC) - timedelta(days=400))
    builder = (
        x509.CertificateBuilder()
        .subject_name(_name(cn, ou))
        .issuer_name(ca_cert.subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(not_before)
        .not_valid_after(not_after)
        .add_extension(x509.BasicConstraints(ca=False, path_length=None), True)
        .add_extension(
            x509.KeyUsage(
                digital_signature=True, key_encipherment=True,
                content_commitment=False, data_encipherment=False,
                key_agreement=False, key_cert_sign=False, crl_sign=False,
                encipher_only=False, decipher_only=False), True)
        .add_extension(
            x509.ExtendedKeyUsage(
                [ExtendedKeyUsageOID.SERVER_AUTH,
                 ExtendedKeyUsageOID.CLIENT_AUTH]), False)
        .add_extension(
            x509.SubjectKeyIdentifier.from_public_key(key.public_key()), False)
        .add_extension(
            x509.AuthorityKeyIdentifier.from_issuer_public_key(
                ca_cert.public_key()), False)
    )
    if sans:
        builder = builder.add_extension(
            x509.SubjectAlternativeName([x509.DNSName(s) for s in sans]), False)
    return key, builder.sign(ca_key, hashes.SHA256())


def _add_ca(org_dir, subdir, cn, not_after):
    key, cert = _mk_ca(cn, not_after)
    _write(org_dir / subdir / (cn + '-cert.pem'), _pem_cert(cert))
    _write(org_dir / subdir / 'priv_sk', _pem_key(key))
    return key, cert


def _add_node(org_dir, parent, name, ou, ca, tlsca, not_after, with_tls=True):
    '''ca/tlsca are (key, cert) tuples. Writes signcert (+ optional tls).'''
    ndir = org_dir / parent / name
    sk, sc = _mk_leaf(name, ou, ca[0], ca[1], not_after)
    _write(ndir / 'msp' / 'signcerts' / (name + '-cert.pem'), _pem_cert(sc))
    _write(ndir / 'msp' / 'keystore' / 'priv_sk', _pem_key(sk))
    if with_tls:
        tk, tc = _mk_leaf(name, None, tlsca[0], tlsca[1], not_after,
                          sans=[name, name.split('.')[0], 'localhost'])
        _write(ndir / 'tls' / 'server.crt', _pem_cert(tc))
        _write(ndir / 'tls' / 'server.key', _pem_key(tk))


def _build_tree(root, *, ca_not_after=None, peer1_not_after=None,
                peer0_not_after=None, orderer_not_after=None,
                second_org=False):
    '''
    A realistic multi-org cryptogen-style tree:
      org1 peers:  peer0 (valid), peer1 (expired by default), peer10 (valid)
      org1 users:  Admin@org1 (valid)
      orderer org: orderer0 (valid by default)
      org2 peer0   (valid)  - only when second_org=True
    '''
    now = datetime.now(UTC)
    ca_na = ca_not_after or now + timedelta(days=365 * 5)
    peer0_na = peer0_not_after or now + timedelta(days=365 * 4)
    peer1_na = peer1_not_after or now - timedelta(days=1)
    orderer_na = orderer_not_after or now + timedelta(days=365 * 4)

    info = {'ca_not_after': ca_na, 'orgs': {}}

    org1 = root / 'peerOrganizations' / 'org1.example.com'
    ca1 = _add_ca(org1, 'ca', 'ca.org1.example.com', ca_na)
    tca1 = _add_ca(org1, 'tlsca', 'tlsca.org1.example.com', ca_na)
    _add_node(org1, 'peers', 'peer0.org1.example.com', 'peer', ca1, tca1, peer0_na)
    _add_node(org1, 'peers', 'peer1.org1.example.com', 'peer', ca1, tca1, peer1_na)
    _add_node(org1, 'peers', 'peer10.org1.example.com', 'peer', ca1, tca1, peer0_na)
    _add_node(org1, 'users', 'Admin@org1.example.com', 'admin', ca1, tca1, peer0_na)
    info['orgs']['org1.example.com'] = {'dir': org1, 'ca': ca1[1], 'tlsca': tca1[1]}

    oorg = root / 'ordererOrganizations' / 'example.com'
    oca = _add_ca(oorg, 'ca', 'ca.example.com', ca_na)
    otca = _add_ca(oorg, 'tlsca', 'tlsca.example.com', ca_na)
    _add_node(oorg, 'orderers', 'orderer0.example.com', 'orderer', oca, otca, orderer_na)
    info['orgs']['example.com'] = {'dir': oorg, 'ca': oca[1], 'tlsca': otca[1]}

    if second_org:
        org2 = root / 'peerOrganizations' / 'org2.example.com'
        ca2 = _add_ca(org2, 'ca', 'ca.org2.example.com', ca_na)
        tca2 = _add_ca(org2, 'tlsca', 'tlsca.org2.example.com', ca_na)
        _add_node(org2, 'peers', 'peer0.org2.example.com', 'peer', ca2, tca2, peer0_na)
        info['orgs']['org2.example.com'] = {'dir': org2, 'ca': ca2[1], 'tlsca': tca2[1]}

    return info


def _ns(crypto_config, **over):
    base = dict(crypto_config=str(crypto_config), type='both', node=[],
                all_expired=False, all=False, days=None, dry_run=False,
                no_backup=False)
    base.update(over)
    return argparse.Namespace(**base)


def _run(crypto_config, **over):
    object.__new__(Network).reissue(_ns(crypto_config, **over))


def _entry(entries, node_prefix, kind):
    # match the short node name at a dotted boundary so 'peer1' != 'peer10'
    return next(e for e in entries
               if e['node'].startswith(node_prefix + '.') and e['kind'] == kind)


def _ec_verify(cert, ca_cert):
    '''Raise if cert's signature does not verify against ca_cert (EC CAs).'''
    ca_cert.public_key().verify(
        cert.signature, cert.tbs_certificate_bytes,
        ec.ECDSA(cert.signature_hash_algorithm))


def _glob_baks(root):
    return sorted(Path(root).glob('**/*.bak'))


# --------------------------------------------------------------------------


class DiscoveryTest(unittest.TestCase):
    def test_discovers_peers_orderers_and_users(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            entries = discover_leaf_certs(str(root), ['signcert', 'tls'])
            found = {(e['node'], e['kind']) for e in entries}
            self.assertIn(('peer0.org1.example.com', 'signcert'), found)
            self.assertIn(('peer0.org1.example.com', 'tls'), found)
            self.assertIn(('orderer0.example.com', 'tls'), found)      # orderer org
            self.assertIn(('Admin@org1.example.com', 'signcert'), found)  # user

    def test_flags_expired(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            entries = discover_leaf_certs(str(root), ['signcert', 'tls'])
            now = datetime.now(UTC)
            self.assertTrue(_entry(entries, 'peer1', 'signcert')['expiry'] <= now)
            self.assertFalse(_entry(entries, 'peer0', 'signcert')['expiry'] <= now)

    def test_type_filter_signcert_only(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            entries = discover_leaf_certs(str(root), ['signcert'])
            self.assertEqual({e['kind'] for e in entries}, {'signcert'})


class BuildReissuedTest(unittest.TestCase):
    def test_preserves_identity_chains_and_honors_validity(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            entries = discover_leaf_certs(str(root), ['signcert'])
            e = _entry(entries, 'peer1', 'signcert')
            old = e['cert']
            ca_cert, ca_key = resolve_ca(e['ca_dir'], old)
            target = datetime.now(UTC) + timedelta(days=1000)

            new = x509.load_pem_x509_certificate(
                build_reissued_cert(old, ca_cert, ca_key, target))

            # identity preserved
            self.assertEqual(new.subject, old.subject)
            self.assertIn('OU=peer', new.subject.rfc4514_string())
            self.assertEqual(
                new.public_key().public_numbers(),
                old.public_key().public_numbers())
            self.assertEqual(new.issuer, ca_cert.subject)
            # validity is exactly the requested value (below the CA cap), not maxed out
            self.assertEqual(new.not_valid_after_utc, target.replace(microsecond=0))
            self.assertNotEqual(new.serial_number, old.serial_number)
            # signature verifies against the CA
            _ec_verify(new, ca_cert)

    def test_extensions_and_criticality_preserved(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            e = _entry(discover_leaf_certs(str(root), ['signcert']), 'peer1', 'signcert')
            ca_cert, ca_key = resolve_ca(e['ca_dir'], e['cert'])
            new = x509.load_pem_x509_certificate(
                build_reissued_cert(e['cert'], ca_cert, ca_key,
                                    datetime.now(UTC) + timedelta(days=100)))
            old_exts = {ext.oid: ext.critical for ext in e['cert'].extensions}
            new_exts = {ext.oid: ext.critical for ext in new.extensions}
            self.assertEqual(new_exts, old_exts)  # same OIDs AND critical flags
            ku = new.extensions.get_extension_for_class(x509.KeyUsage)
            self.assertTrue(ku.critical)  # critical KeyUsage stays critical

    def test_tls_san_preserved(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            e = _entry(discover_leaf_certs(str(root), ['tls']), 'peer1', 'tls')
            ca_cert, ca_key = resolve_ca(e['ca_dir'], e['cert'])
            new = x509.load_pem_x509_certificate(
                build_reissued_cert(e['cert'], ca_cert, ca_key,
                                    datetime.now(UTC) + timedelta(days=100)))
            sans = new.extensions.get_extension_for_class(
                x509.SubjectAlternativeName).value.get_values_for_type(x509.DNSName)
            self.assertIn('peer1.org1.example.com', sans)
            self.assertIn('localhost', sans)

    def test_ed25519_ca_signs_with_no_hash(self):
        # exercises _sig_hash returning None for Edwards keys
        ca_key, ca_cert = _mk_ca('ca.ed.example.com',
                                 datetime.now(UTC) + timedelta(days=3650),
                                 key=ed25519.Ed25519PrivateKey.generate())
        leaf_key = ec.generate_private_key(ec.SECP256R1())
        old = (
            x509.CertificateBuilder()
            .subject_name(_name('peer0.ed.example.com', 'peer'))
            .issuer_name(ca_cert.subject)
            .public_key(leaf_key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(datetime.now(UTC) - timedelta(days=1))
            .not_valid_after(datetime.now(UTC) + timedelta(days=1))
            .add_extension(x509.BasicConstraints(ca=False, path_length=None), True)
            .sign(ca_key, None)
        )
        new = x509.load_pem_x509_certificate(
            build_reissued_cert(old, ca_cert, ca_key,
                                datetime.now(UTC) + timedelta(days=500)))
        # ed25519 verify takes (signature, data) and no hash algorithm
        ca_cert.public_key().verify(new.signature, new.tbs_certificate_bytes)


class ReissueCommandTest(unittest.TestCase):
    def _bytes(self, path):
        return Path(path).read_bytes()

    def _snapshot(self, root, kinds=('signcert', 'tls')):
        entries = discover_leaf_certs(str(root), list(kinds))
        return {(e['node'], e['kind']): e['cert_path'] for e in entries}

    def test_all_expired_only_touches_expired_and_stays_legit(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            info = _build_tree(root)
            paths = self._snapshot(root)
            before = {k: self._bytes(p) for k, p in paths.items()}

            _run(root, all_expired=True)

            after = {k: self._bytes(p) for k, p in paths.items()}
            for k in paths:
                if k[0].startswith('peer1.'):  # only peer1 is expired (not peer10)
                    self.assertNotEqual(before[k], after[k], k)
                else:
                    self.assertEqual(before[k], after[k], k)
            # the reissued cert, reloaded from disk, is genuinely legit:
            sc = paths[('peer1.org1.example.com', 'signcert')]
            new = x509.load_pem_x509_certificate(self._bytes(sc))
            old = x509.load_pem_x509_certificate(self._bytes(Path(str(sc) + '.bak')))
            _ec_verify(new, info['orgs']['org1.example.com']['ca'])  # chains to CA
            self.assertEqual(new.public_key().public_numbers(),
                             old.public_key().public_numbers())      # same key
            self.assertEqual(new.subject, old.subject)               # same subject
            self.assertGreater(new.not_valid_after_utc, datetime.now(UTC))
            # backup byte-matches the pre-reissue original
            self.assertEqual(self._bytes(Path(str(sc) + '.bak')),
                             before[('peer1.org1.example.com', 'signcert')])

    def test_node_short_name_does_not_match_peer10(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            paths = self._snapshot(root, kinds=['signcert'])
            before = {k: self._bytes(p) for k, p in paths.items()}

            _run(root, node=['peer1'], type='signcert')

            after = {k: self._bytes(p) for k, p in paths.items()}
            self.assertNotEqual(before[('peer1.org1.example.com', 'signcert')],
                                after[('peer1.org1.example.com', 'signcert')])
            # the dotted-boundary guard must keep peer10 and peer0 untouched
            self.assertEqual(before[('peer10.org1.example.com', 'signcert')],
                             after[('peer10.org1.example.com', 'signcert')])
            self.assertEqual(before[('peer0.org1.example.com', 'signcert')],
                             after[('peer0.org1.example.com', 'signcert')])

    def test_orderer_tls_renewed(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            info = _build_tree(root)
            tls = self._snapshot(root)[('orderer0.example.com', 'tls')]
            before = self._bytes(tls)

            _run(root, node=['orderer0'], type='tls')

            new = x509.load_pem_x509_certificate(self._bytes(tls))
            self.assertNotEqual(before, self._bytes(tls))
            _ec_verify(new, info['orgs']['example.com']['tlsca'])  # orderer TLS CA

    def test_cross_org_ca_resolution(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            info = _build_tree(root, second_org=True)

            _run(root, all=True, type='signcert')

            # org2's peer must be reissued under org2's CA, never org1's
            p2 = self._snapshot(root, kinds=['signcert'])[
                ('peer0.org2.example.com', 'signcert')]
            new = x509.load_pem_x509_certificate(self._bytes(p2))
            self.assertEqual(new.issuer, info['orgs']['org2.example.com']['ca'].subject)
            _ec_verify(new, info['orgs']['org2.example.com']['ca'])

    def test_days_below_cap_is_honored(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            # CA far in the future so the 30-day request is not capped
            _build_tree(root, ca_not_after=datetime.now(UTC) + timedelta(days=3650))
            _run(root, node=['peer1'], type='signcert', days=30)
            new = _entry(discover_leaf_certs(str(root), ['signcert']),
                         'peer1', 'signcert')['cert']
            now = datetime.now(UTC)
            self.assertGreater(new.not_valid_after_utc, now + timedelta(days=29))
            self.assertLess(new.not_valid_after_utc, now + timedelta(days=31))

    def test_caps_at_ca_expiry(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            info = _build_tree(root)
            _run(root, node=['peer1'], type='signcert', days=365 * 100)
            new = _entry(discover_leaf_certs(str(root), ['signcert']),
                         'peer1', 'signcert')['cert']
            self.assertEqual(new.not_valid_after_utc,
                             info['ca_not_after'].replace(microsecond=0))

    def test_dry_run_writes_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            paths = self._snapshot(root)
            before = {k: self._bytes(p) for k, p in paths.items()}

            _run(root, all=True, dry_run=True)

            after = {k: self._bytes(p) for k, p in paths.items()}
            self.assertEqual(before, after)
            self.assertEqual(_glob_baks(root), [])  # no backups during dry-run

    def test_no_backup_flag(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            sc = self._snapshot(root, kinds=['signcert'])[
                ('peer1.org1.example.com', 'signcert')]
            before = self._bytes(sc)
            _run(root, node=['peer1'], type='signcert', no_backup=True)
            self.assertNotEqual(before, self._bytes(sc))  # reissued
            self.assertFalse(Path(str(sc) + '.bak').exists())  # but no backup

    def test_reissue_twice_preserves_original_backup(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root)
            sc = self._snapshot(root, kinds=['signcert'])[
                ('peer1.org1.example.com', 'signcert')]
            original = self._bytes(sc)

            _run(root, node=['peer1'], type='signcert', days=100)
            _run(root, node=['peer1'], type='signcert', days=200)

            # the canonical .bak must still hold the ORIGINAL cert, not the
            # first-reissue output (second run side-steps to a timestamped name)
            self.assertEqual(self._bytes(Path(str(sc) + '.bak')), original)
            self.assertGreaterEqual(len(_glob_baks(root)), 2)

    def test_refuses_when_ca_expired_and_writes_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            _build_tree(root, ca_not_after=datetime.now(UTC) - timedelta(days=1))
            paths = self._snapshot(root)
            before = {k: self._bytes(p) for k, p in paths.items()}

            with self.assertRaisesRegex(SystemExit, 'could not be reissued'):
                _run(root, all_expired=True)

            after = {k: self._bytes(p) for k, p in paths.items()}
            self.assertEqual(before, after)        # nothing written
            self.assertEqual(_glob_baks(root), [])  # no backups written

    def test_refuses_when_ca_key_missing_and_writes_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            info = _build_tree(root)
            for sk in (info['orgs']['org1.example.com']['dir'] / 'ca').glob('*_sk'):
                sk.unlink()  # CA cert present, key absent (fabric-ca style)
            sc = self._snapshot(root, kinds=['signcert'])[
                ('peer1.org1.example.com', 'signcert')]
            before = self._bytes(sc)

            with self.assertRaisesRegex(SystemExit, 'could not be reissued'):
                _run(root, node=['peer1'], type='signcert')

            self.assertEqual(before, self._bytes(sc))
            self.assertFalse(Path(str(sc) + '.bak').exists())

    def test_mixed_success_and_failure(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'crypto-config'
            info = _build_tree(root, second_org=True)
            # break org1's CA key only; org2 stays healthy
            for sk in (info['orgs']['org1.example.com']['dir'] / 'ca').glob('*_sk'):
                sk.unlink()
            paths = self._snapshot(root, kinds=['signcert'])
            good = paths[('peer0.org2.example.com', 'signcert')]
            bad = paths[('peer1.org1.example.com', 'signcert')]
            good_before, bad_before = self._bytes(good), self._bytes(bad)

            with self.assertRaises(SystemExit):
                _run(root, all=True, type='signcert')

            self.assertNotEqual(good_before, self._bytes(good))  # healthy org reissued
            self.assertEqual(bad_before, self._bytes(bad))       # broken org untouched


class BackupPathTest(unittest.TestCase):
    def test_timestamped_when_bak_exists(self):
        with tempfile.TemporaryDirectory() as d:
            cert = Path(d) / 'c.pem'
            cert.write_bytes(b'x')
            first = _backup_path(cert)
            self.assertEqual(first, Path(str(cert) + '.bak'))
            first.write_bytes(b'orig')          # occupy the canonical name
            second = _backup_path(cert)
            self.assertNotEqual(second, first)   # falls back to a timestamped name
            self.assertTrue(str(second).endswith('.bak'))


if __name__ == '__main__':
    unittest.main()
