#!/usr/bin/env python3
"""Required offline S04 fixture compiler + actual main container gate.
Never installs dependencies or falls back to mocks when a tool/binary is absent.
"""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--evidence', required=True)
    args = parser.parse_args()
    binary = Path(args.binary).resolve()
    if not binary.is_file():
        parser.error('--binary must be an existing actual statically built service ELF')
    with binary.open('rb') as stream:
        if stream.read(4) != b'\x7fELF':
            parser.error('--binary must be an existing actual statically built service ELF')
    evidence = Path(args.evidence).resolve()
    root = Path(__file__).resolve().parents[2]
    env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off', CGO_ENABLED='0', GOMAXPROCS=os.environ.get('GOMAXPROCS', '4'))
    with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-') as owned:
        temp = Path(owned).resolve()
        if temp.parent != Path(tempfile.gettempdir()).resolve() or not temp.name.startswith('tikuhelper-s04-'):
            raise RuntimeError('refused unsafe temporary path')
        probe = temp / 's04-fixture'
        compile_command = [env.get('TIKU_S04_GO_BINARY', 'go'), 'build', '-p=2', '-o', str(probe), './scripts/ci/s04-fixture']
        compile_result = subprocess.run(compile_command, cwd=root, env=env, timeout=240, check=False)
        if compile_result.returncode != 0:
            return compile_result.returncode if compile_result.returncode > 0 else 1
        command = [sys.executable, '-B', str(root / 'scripts/ci/s04-main-flow.py'), '--binary', str(binary), '--probe', str(probe), '--evidence', str(evidence)]
        process = subprocess.Popen(command, cwd=root, env=env)
        try:
            result = process.wait(timeout=300)
        except subprocess.TimeoutExpired:
            # Signal only this owned orchestrator. It retains exact resource
            # handles and runs bounded project-only cleanup before exiting.
            process.terminate()
            try:
                process.wait(timeout=120)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            raise RuntimeError('S04 actual-main orchestrator exceeded its bound; inspect preserved owned-resource ledger')
        return result if result >= 0 else 1


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print('S04 startup integration runner failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
