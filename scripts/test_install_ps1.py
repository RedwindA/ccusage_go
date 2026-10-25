"""Exercise install.ps1 offline with real archives, stubbing only the downloads."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile

INSTALLER = Path(__file__).resolve().parents[1] / 'install.ps1'

# Invoke-WebRequest is the installer's only network access; a function of the
# same name takes precedence over the cmdlet.
STUB = r'''
function Invoke-WebRequest {
  param([string]$Uri, [string]$OutFile, [string]$Method, [switch]$UseBasicParsing)
  if ($env:TEST_DOWNLOAD_FAIL -eq '1') { throw "HTTP 404: $Uri" }
  if ($Uri -like '*/releases/latest') {
    $tag = [uri]'https://github.com/RedwindA/ccusage_go/releases/tag/v0.15.0'
    if ($env:TEST_PS7_RESPONSE -eq '1') {
      return [pscustomobject]@{ BaseResponse = [pscustomobject]@{ RequestMessage = [pscustomobject]@{ RequestUri = $tag } } }
    }
    return [pscustomobject]@{ BaseResponse = [pscustomobject]@{ ResponseUri = $tag } }
  }
  if ($Uri -like 'https://github.com/RedwindA/ccusage_go/releases/download/v0.15.0/*') {
    Copy-Item -LiteralPath (Join-Path $env:TEST_ASSETS ($Uri -split '/')[-1]) -Destination $OutFile
    return
  }
  throw "HTTP 404: $Uri"
}
if ($env:TEST_STRICT -eq '1') { Set-StrictMode -Version Latest }
& $env:TEST_INSTALLER @args
'''


class PowerShellInstallerTest:
    shell = None

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.assets = self.root / 'assets'
        self.assets.mkdir()
        self.wrapper = self.root / 'run.ps1'
        self.wrapper.write_text(STUB)
        self.destination = self.root / 'install dir'
        self.destination.mkdir()
        self.target = self.destination / 'ccusage_go.exe'
        self.target.write_bytes(b'old binary')
        self.payload = b'MZ fixture-v0.15.0'
        # The installer picks the host's architecture, so serve both.
        self.write_archives(self.payload)
        self.env = dict(os.environ, OS='Windows_NT', INSTALL_DIR=str(self.destination),
                        TEST_ASSETS=str(self.assets), TEST_INSTALLER=str(INSTALLER),
                        CCUSAGE_GO_NO_MODIFY_PATH='1', PROCESSOR_ARCHITECTURE='AMD64')
        for name in ('VERSION', 'TEST_DOWNLOAD_FAIL', 'TEST_PS7_RESPONSE', 'TEST_STRICT'):
            self.env.pop(name, None)

    def write_archives(self, payload, name=None):
        sums = ''
        for arch in ('amd64', 'arm64'):
            binary = f'ccusage_go-windows-{arch}.exe'
            path = self.assets / f'{binary}.zip'
            with zipfile.ZipFile(path, 'w') as archive:
                archive.writestr(name or binary, payload)
                archive.writestr('LICENSE', 'MIT')
            sums += f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n'
        (self.assets / 'checksums.txt').write_text(sums)

    def run_installer(self, *args):
        return subprocess.run([self.shell, '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
                               '-File', str(self.wrapper), *args],
                              env=self.env, text=True, capture_output=True)

    def assert_installed(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.target.read_bytes(), self.payload)
        self.assertEqual(sorted(p.name for p in self.destination.iterdir()), ['ccusage_go.exe'])

    def assert_preserved(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.target.read_bytes(), b'old binary')
        self.assertEqual(sorted(p.name for p in self.destination.iterdir()), ['ccusage_go.exe'])

    def test_pinned_install(self):
        self.assert_installed(self.run_installer('v0.15.0'))

    def test_version_from_environment(self):
        self.env['VERSION'] = 'v0.15.0'
        self.assert_installed(self.run_installer())

    def test_latest_install(self):
        self.assert_installed(self.run_installer())

    def test_latest_install_powershell7_response(self):
        self.env['TEST_PS7_RESPONSE'] = '1'
        self.assert_installed(self.run_installer())

    def test_latest_install_under_callers_strict_mode(self):
        self.env.update(TEST_STRICT='1', TEST_PS7_RESPONSE='1')
        self.assert_installed(self.run_installer())

    @unittest.skipIf(os.name == 'nt', 'needs a registry that rejects the PATH edit')
    def test_path_failure_keeps_new_install(self):
        # Outside Windows the registry API throws, like a locked-down HKCU.
        del self.env['CCUSAGE_GO_NO_MODIFY_PATH']
        result = self.run_installer('v0.15.0')
        self.assert_installed(result)
        self.assertIn('Could not add', result.stdout + result.stderr)

    def test_fresh_install_creates_directory(self):
        shutil.rmtree(self.destination)
        result = self.run_installer('v0.15.0')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.target.read_bytes(), self.payload)
        self.assertIn('Add this directory to your PATH', result.stdout)

    def test_checksum_mismatch_preserves_existing_install(self):
        for archive in self.assets.glob('*.zip'):
            archive.write_bytes(archive.read_bytes() + b'corrupt')
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_missing_checksum_preserves_existing_install(self):
        (self.assets / 'checksums.txt').write_text('')
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_missing_binary_preserves_existing_install(self):
        self.write_archives(self.payload, name='unexpected.exe')
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_download_failure_preserves_existing_install(self):
        self.env['TEST_DOWNLOAD_FAIL'] = '1'
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_empty_binary_preserves_existing_install(self):
        self.write_archives(b'')
        self.assert_preserved(self.run_installer('v0.15.0'))

    def test_invalid_version(self):
        self.assert_preserved(self.run_installer('v0.15.0/../../bad'))

    def test_rejects_other_systems(self):
        del self.env['OS']
        result = self.run_installer('v0.15.0')
        self.assert_preserved(result)
        self.assertIn('install.sh', result.stdout + result.stderr)

    @unittest.skipUnless(os.name == 'nt', 'edits the Windows user PATH')
    def test_adds_install_dir_to_user_path_once(self):
        import winreg

        def environment():
            return winreg.OpenKey(winreg.HKEY_CURRENT_USER, 'Environment', 0, winreg.KEY_ALL_ACCESS)
        with environment() as key:
            try:
                saved = winreg.QueryValueEx(key, 'Path')
            except FileNotFoundError:
                saved = None
            winreg.SetValueEx(key, 'Path', 0, winreg.REG_EXPAND_SZ, r'%USERPROFILE%\fixture-bin')

        def restore():
            with environment() as key:
                if saved is None:
                    winreg.DeleteValue(key, 'Path')
                else:
                    winreg.SetValueEx(key, 'Path', 0, saved[1], saved[0])
        self.addCleanup(restore)
        del self.env['CCUSAGE_GO_NO_MODIFY_PATH']
        for _ in range(2):
            self.assert_installed(self.run_installer('v0.15.0'))
        with environment() as key:
            value, kind = winreg.QueryValueEx(key, 'Path')
        self.assertEqual(kind, winreg.REG_EXPAND_SZ)
        self.assertEqual(value, str(self.destination) + r';%USERPROFILE%\fixture-bin')


for _shell in ('pwsh', 'powershell'):
    if shutil.which(_shell):
        globals()[f'{_shell.capitalize()}InstallerTest'] = type(
            f'{_shell.capitalize()}InstallerTest', (PowerShellInstallerTest, unittest.TestCase), {'shell': _shell})


if __name__ == '__main__':
    unittest.main()
