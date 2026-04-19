import os
import sys
import tempfile
import unittest

import yaml

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from network import Network  # noqa: E402

SUBSTRATE_DEFAULT = 'luthersystems/substrate:$CHAINCODE_VERSION'


def _make_net(dest):
    n = object.__new__(Network)
    repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    n.template_base_path = os.path.join(repo_root, 'template')
    n.destination_path = dest
    n.chown = None
    return n


def _render(chaincode_names, image_overrides=None):
    with tempfile.TemporaryDirectory() as d:
        _make_net(d).generate_chaincodes_compose(chaincode_names, image_overrides)
        with open(os.path.join(d, 'docker-compose-ccaas.yaml')) as f:
            return f.read()


class ImageOverrideTest(unittest.TestCase):
    def test_default_image_when_no_override(self):
        parsed = yaml.safe_load(_render(['a', 'b']))
        self.assertEqual(parsed['services']['a-peer0']['image'], SUBSTRATE_DEFAULT)
        self.assertEqual(parsed['services']['b-peer0']['image'], SUBSTRATE_DEFAULT)

    def test_override_applies_only_to_named_service(self):
        parsed = yaml.safe_load(_render(
            ['a', 'external'],
            ['external=luthersystems/externalcc:$CHAINCODE_VERSION']))
        self.assertEqual(parsed['services']['a-peer0']['image'], SUBSTRATE_DEFAULT)
        self.assertEqual(
            parsed['services']['external-peer0']['image'],
            'luthersystems/externalcc:$CHAINCODE_VERSION')

    def test_image_value_containing_equals_sign_preserved(self):
        # Guards against a future `.split('=')` refactor; partition splits on first '='.
        parsed = yaml.safe_load(_render(
            ['a'], ['a=registry.io/img:tag?k=v']))
        self.assertEqual(parsed['services']['a-peer0']['image'], 'registry.io/img:tag?k=v')

    def test_unknown_name_exits(self):
        with self.assertRaises(SystemExit) as cm:
            _render(['a'], ['foo=bar:1'])
        self.assertIn("'foo'", str(cm.exception))
        self.assertIn("cc_variants", str(cm.exception))

    def test_malformed_pair_exits(self):
        for pair in ('no-equals-sign', '=bar', 'a='):
            with self.subTest(pair=pair):
                with self.assertRaises(SystemExit) as cm:
                    _render(['a'], [pair])
                self.assertIn('--image-override expects NAME=IMAGE', str(cm.exception))

    def test_duplicate_name_rejected(self):
        with self.assertRaises(SystemExit) as cm:
            _render(['a'], ['a=one:1', 'a=two:2'])
        self.assertIn("'a'", str(cm.exception))
        self.assertIn('more than once', str(cm.exception))

    def test_empty_cc_list_skips(self):
        with tempfile.TemporaryDirectory() as d:
            _make_net(d).generate_chaincodes_compose([])
            self.assertFalse(os.path.exists(os.path.join(d, 'docker-compose-ccaas.yaml')))

    def test_image_override_requires_ccaas(self):
        import argparse
        args = argparse.Namespace(
            ccaas=False, image_override=['a=foo:1'],
            cc_name='a', cc_version='v1', cc_variants='a', cc_path='/tmp/x')
        with self.assertRaises(SystemExit) as cm:
            _make_net('/tmp').generate_chaincodes(args)
        self.assertIn('--image-override requires --ccaas', str(cm.exception))


if __name__ == '__main__':
    unittest.main()
