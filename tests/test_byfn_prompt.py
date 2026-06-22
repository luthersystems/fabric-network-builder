import os
import subprocess
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BYFN = os.path.join(REPO, 'byfn.sh')


def _extract_askproceed():
    '''Pull just the askProceed function out of byfn.sh so it can be exercised
    in isolation (sourcing the whole script would run its main dispatch).'''
    lines = open(BYFN).read().splitlines()
    start = next(i for i, l in enumerate(lines)
                 if l.startswith('function askProceed'))
    end = next(i for i in range(start + 1, len(lines)) if lines[i] == '}')
    return '\n'.join(lines[start:end + 1])


FUNC = _extract_askproceed()


def _run(stdin, force=None):
    env = dict(os.environ)
    if force is not None:
        env['FORCE'] = force
    return subprocess.run(
        ['bash', '-c', FUNC + '\naskProceed\n'],
        input=stdin, env=env, capture_output=True, text=True)


class AskProceedTest(unittest.TestCase):
    # Regression guard: askProceed used to recurse on invalid input, so a
    # closed/non-interactive stdin recursed until the stack overflowed (SIGSEGV).
    def test_no_stdin_aborts_cleanly_not_segfault(self):
        r = _run(stdin='')  # EOF immediately
        self.assertEqual(r.returncode, 1)        # clean abort, not -11 (SIGSEGV)
        self.assertIn('no input on stdin', r.stdout + r.stderr)

    def test_flood_of_bad_input_then_eof_does_not_overflow(self):
        r = _run(stdin='x\n' * 5000)  # 5000 invalid lines then EOF
        self.assertEqual(r.returncode, 1)        # loops, then EOF-aborts cleanly
        self.assertNotIn(r.returncode, (-11, 139))

    def test_yes_proceeds(self):
        r = _run(stdin='y\n')
        self.assertEqual(r.returncode, 0)
        self.assertIn('proceeding', r.stdout)

    def test_no_exits(self):
        r = _run(stdin='n\n')
        self.assertEqual(r.returncode, 1)
        self.assertIn('exiting', r.stdout)

    def test_force_skips_prompt(self):
        r = _run(stdin='', force='true')  # no stdin, but FORCE short-circuits
        self.assertEqual(r.returncode, 0)


if __name__ == '__main__':
    unittest.main()
