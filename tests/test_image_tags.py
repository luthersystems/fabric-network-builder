import os
import subprocess
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BYFN = os.path.join(REPO, 'byfn.sh')


def _extract_resolve():
    '''Pull just the resolveImageTags function out of byfn.sh (sourcing the
    whole script would run its main dispatch).'''
    lines = open(BYFN).read().splitlines()
    start = next(i for i, l in enumerate(lines)
                 if l.startswith('function resolveImageTags'))
    end = next(i for i in range(start + 1, len(lines)) if lines[i] == '}')
    return '\n'.join(lines[start:end + 1])


FUNC = _extract_resolve()


def _run(baked, **env):
    full = {k: v for k, v in os.environ.items() if not k.startswith('FNB_')}
    full.update(env)
    return subprocess.run(
        ['bash', '-c', 'IMAGETAG=%s\n' % baked + FUNC +
         '\nresolveImageTags || exit 1\necho "$IMAGETAG $TOOLSIMAGETAG"\n'],
        env=full, capture_output=True, text=True)


def _resolve(**env):
    '''Run resolveImageTags with IMAGETAG baked at 2.5.16, as the image's
    byfn-vars.sh sets it, and return (IMAGETAG, TOOLSIMAGETAG).'''
    full = {k: v for k, v in os.environ.items() if not k.startswith('FNB_')}
    full.update(env)
    r = subprocess.run(
        ['bash', '-c', 'IMAGETAG=2.5.16\n' + FUNC +
         '\nresolveImageTags\necho "$IMAGETAG $TOOLSIMAGETAG"\n'],
        env=full, capture_output=True, text=True, check=True)
    return tuple(r.stdout.split())


class ResolveImageTagsTest(unittest.TestCase):
    def test_no_override_uses_the_baked_tag(self):
        self.assertEqual(_resolve(), ('2.5.16', '2.5.16'))

    def test_a_2x_override_moves_the_cli_too(self):
        self.assertEqual(_resolve(FNB_IMAGETAG='2.5.15'), ('2.5.15', '2.5.15'))

    # Fabric 3 publishes no fabric-tools image, so the CLI stays on the baked
    # 2.x tag (substrate#1034).
    def test_a_3x_override_keeps_the_baked_cli(self):
        self.assertEqual(_resolve(FNB_IMAGETAG='3.1.4'), ('3.1.4', '2.5.16'))

    def test_the_cli_tag_can_be_set_outright(self):
        self.assertEqual(
            _resolve(FNB_IMAGETAG='3.1.4', FNB_TOOLSIMAGETAG='2.5.15'),
            ('3.1.4', '2.5.15'))


    # osnadmin and the channel participation API arrived in Fabric 2.3.
    def test_a_release_before_2_3_is_refused(self):
        for tag in ('2.2.0', '2.2', '1.4.12'):
            r = _run('2.5.16', FNB_IMAGETAG=tag)
            self.assertEqual(r.returncode, 1, tag)
            self.assertIn('2.3 or later', r.stderr)

    def test_2_3_is_accepted(self):
        r = _run('2.5.16', FNB_IMAGETAG='2.3.3')
        self.assertEqual((r.returncode, r.stdout.split()), (0, ['2.3.3', '2.3.3']))

    # An image built with a 3.x baked tag has no 2.x CLI to fall back to.
    def test_a_3x_baked_tag_needs_an_explicit_cli_tag(self):
        r = _run('3.1.4')
        self.assertEqual(r.returncode, 1)
        self.assertIn('FNB_TOOLSIMAGETAG', r.stderr)
        r = _run('3.1.4', FNB_TOOLSIMAGETAG='2.5.16')
        self.assertEqual(r.stdout.split(), ['3.1.4', '2.5.16'])


if __name__ == '__main__':
    unittest.main()
