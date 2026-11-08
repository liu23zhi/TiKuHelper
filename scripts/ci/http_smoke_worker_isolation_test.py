import contextlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest import mock


class HTTPMainWorkerIsolation(unittest.TestCase):
    def test_actual_binary_branch_disables_extension_before_popen(self):
        script = Path(__file__).with_name('http-smoke.py')
        spec = importlib.util.spec_from_file_location('http_smoke_worker_probe', script)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        captured = []

        def stop_before_start(*args, **kwargs):
            captured.append(kwargs['env'])
            raise RuntimeError('synthetic stop before process start')

        with tempfile.TemporaryDirectory(prefix='tiku-http-worker-contract-') as directory:
            root = Path(directory)
            binary = root / 'synthetic-not-executed'
            binary.write_bytes(b'NOT AN EXECUTABLE; Popen IS INTERCEPTED')
            dist = root / 'dist'
            dist.mkdir()
            argv = [str(script), '--binary', str(binary), '--dist', str(dist), '--log-dir', str(root / 'logs')]
            with mock.patch.object(module.sys, 'argv', argv), mock.patch.object(module.socket, 'socket'), mock.patch.object(module.subprocess, 'Popen', side_effect=stop_before_start), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(module.main(), 2)
        self.assertEqual(len(captured), 1)
        environment = captured[0]
        self.assertEqual(environment.get('EXTENSION_UPDATE_ENABLED'), 'false')
        self.assertEqual(environment.get('EXTENSION_EDGE_ID'), '')
        self.assertEqual(environment.get('EXTENSION_CHROME_ID'), '')
        self.assertEqual(environment['TRUSTED_PROXIES'], '127.0.0.1,::1')


if __name__ == '__main__':
    unittest.main()
