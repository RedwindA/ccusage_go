"""Exercise installer downloads offline using real archives and checksum tools."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

INSTALLER = Path(__file__).resolve().parents[1] / 'install.sh'


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.assets = self.root / 'assets'
        self.assets.mkdir()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.destination = self.root / 'install dir'
        self.destination.mkdir()
        self.target = self.destination / 'ccusage_go'
        self.target.write_text('old binary')
        self.payload = b'#!/bin/sh\necho fixture-v0.15.0\n'
        self.archive = self.assets / 'ccusage_go-linux-amd64.tar.gz'
        self.write_archive('ccusage_go-linux-amd64', self.payload)
        self.stub('uname', '#!/bin/sh\ncase "$1" in -s) echo "${TEST_OS:-Linux}" ;; -m) echo "${TEST_ARCH:-x86_64}" ;; esac\n')
        self.stub('curl', '''#!/bin/sh
set -eu
url= out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    --proto|-w|--retry) shift 2 ;;
    --tlsv1.2) shift ;;
    https://*) url=$1; shift ;;
    *) shift ;;
  esac
done
if [ "${TEST_DOWNLOAD_FAIL:-}" = 1 ]; then exit 22; fi
case "$url" in
  */releases/latest) printf 'https://github.com/RedwindA/ccusage_go/releases/tag/v0.15.0' ;;
  */releases/download/v0.15.0/*) cp "$TEST_ASSETS/${url##*/}" "$out" ;;
  *) exit 22 ;;
esac
''')
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        INSTALL_DIR=str(self.destination), TEST_ASSETS=str(self.assets))
        self.env.pop('VERSION', None)

    def stub(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def write_archive(self, name, payload):
        with tarfile.open(self.archive, 'w:gz') as archive:
            entry = tarfile.TarInfo(name)
            entry.size = len(payload)
            archive.addfile(entry, io.BytesIO(payload))
        digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        (self.assets / 'checksums.txt').write_text(f'{digest}  {self.archive.name}\n')

    def run_installer(self, *args):
        return subprocess.run(['sh', str(INSTALLER), *args], env=self.env,
                              text=True, capture_output=True)

    def assert_preserved(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.target.read_text(), 'old binary')
        self.assertEqual(list(self.destination.glob('.ccusage_go.*')), [])

    def test_pinned_install(self):
        result = self.run_installer('v0.15.0')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.target.read_bytes(), self.payload)
        self.assertTrue(os.access(self.target, os.X_OK))

    def test_latest_install(self):
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.target.read_bytes(), self.payload)

    def test_checksum_mismatch_preserves_existing_install(self):
        self.archive.write_bytes(self.archive.read_bytes() + b'corrupt')
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_missing_checksum_preserves_existing_install(self):
        (self.assets / 'checksums.txt').write_text('')
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_missing_binary_preserves_existing_install(self):
        self.write_archive('unexpected', self.payload)
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_download_failure_preserves_existing_install(self):
        self.env['TEST_DOWNLOAD_FAIL'] = '1'
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_unsupported_architecture(self):
        self.env['TEST_ARCH'] = 'mips'
        self.assert_preserved(self.run_installer())

    def test_macos_arm64(self):
        self.env.update(TEST_OS='Darwin', TEST_ARCH='arm64')
        self.archive = self.assets / 'ccusage_go-darwin-arm64.tar.gz'
        self.write_archive('ccusage_go-darwin-arm64', self.payload)
        result = self.run_installer('v0.15.0')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_invalid_version(self):
        self.assert_preserved(self.run_installer('v0.15.0/../../bad'))


if __name__ == '__main__':
    unittest.main()
