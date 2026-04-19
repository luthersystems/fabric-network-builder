import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from network import Network  # noqa: E402


def _make_net(dest):
    n = object.__new__(Network)
    repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    n.template_base_path = os.path.join(repo_root, 'template')
    n.destination_path = dest
    n.chown = None
    return n


def _rendered(dest):
    with open(os.path.join(dest, 'docker-compose-ccaas.yaml')) as f:
        return f.read()


class GenerateChaincodesComposeTest(unittest.TestCase):
    def test_default_image_when_no_override(self):
        with tempfile.TemporaryDirectory() as d:
            _make_net(d).generate_chaincodes_compose(['a', 'b'])
            out = _rendered(d)
        self.assertIn('a-peer0:', out)
        self.assertIn('b-peer0:', out)
        self.assertEqual(out.count('image: luthersystems/substrate:$CHAINCODE_VERSION'), 2)

    def test_override_applies_only_to_named_service(self):
        with tempfile.TemporaryDirectory() as d:
            _make_net(d).generate_chaincodes_compose(
                ['a', 'external'],
                ['external=luthersystems/externalcc:$CHAINCODE_VERSION'])
            out = _rendered(d)
        a_block = out.split('a-peer0:', 1)[1].split('external-peer0:', 1)[0]
        ext_block = out.split('external-peer0:', 1)[1]
        self.assertIn('image: luthersystems/substrate:$CHAINCODE_VERSION', a_block)
        self.assertIn('image: luthersystems/externalcc:$CHAINCODE_VERSION', ext_block)
        self.assertNotIn('image: luthersystems/substrate', ext_block.split('\n', 5)[1])

    def test_unknown_name_exits(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(SystemExit) as cm:
                _make_net(d).generate_chaincodes_compose(['a'], ['foo=bar:1'])
            self.assertIn("'foo'", str(cm.exception))

    def test_malformed_pair_exits(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(SystemExit):
                _make_net(d).generate_chaincodes_compose(['a'], ['no-equals-sign'])
            with self.assertRaises(SystemExit):
                _make_net(d).generate_chaincodes_compose(['a'], ['=bar'])
            with self.assertRaises(SystemExit):
                _make_net(d).generate_chaincodes_compose(['a'], ['a='])

    def test_empty_cc_list_skips(self):
        with tempfile.TemporaryDirectory() as d:
            _make_net(d).generate_chaincodes_compose([])
            self.assertFalse(os.path.exists(os.path.join(d, 'docker-compose-ccaas.yaml')))


if __name__ == '__main__':
    unittest.main()
