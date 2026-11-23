""" End-to-end tests: run the real pirsyncd binary with the real rsync.

Build the binary and run the tests in docker:
    docker compose run --rm build
    docker compose run --rm e2e

The tests only use the command line, so they work for any pirsyncd
(set PIRSYNCD=/path/to/pirsyncd to test another one).
"""

import os
import shutil
import subprocess
import tempfile
import time
import unittest

# The pirsyncd binary to test
HERE = os.path.dirname(os.path.abspath(__file__))
PIRSYNCD = os.environ.get("PIRSYNCD",
                          os.path.join(HERE, "..", "dist", "pirsyncd"))


def wait_for(check, timeout=15):
    """ Wait until check() is True, returns the last result """
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if check():
            return True
        time.sleep(0.1)
    return check()


def tree(top):
    """ All files & directories under top, with the file contents """
    result = {}
    for root, dirs, files in os.walk(top):
        for name in dirs:
            result[os.path.relpath(os.path.join(root, name), top)] = "<dir>"
        for name in files:
            path = os.path.join(root, name)
            try:
                with open(path) as pfile:
                    result[os.path.relpath(path, top)] = pfile.read()
            except FileNotFoundError:
                # rsync renamed its temp file while we were looking
                pass
    return result


def write(path, text):
    """ Create (or change) a file """
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as pfile:
        pfile.write(text)


class TestDaemon(unittest.TestCase):
    """ Run the real pirsyncd with the real rsync """

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.src = os.path.join(self.tmp.name, "src")
        self.dst = os.path.join(self.tmp.name, "dst")
        self.pid = os.path.join(self.tmp.name, "pirsyncd.pid")
        self.log = os.path.join(self.tmp.name, "pirsyncd.log")
        os.mkdir(self.src)
        os.mkdir(self.dst)
        self.process = None

    def tearDown(self):
        if self.process and self.process.poll() is None:
            self.process.kill()
            self.process.wait()
        self.tmp.cleanup()

    def args(self, *extra):
        return [PIRSYNCD, "-s", self.src, "-d", self.dst,
                "-p", self.pid, "--debug", self.log, "--nolog",
                "--delay", "0.2", "--verbose"] + list(extra)

    def in_sync(self):
        return tree(self.src) == tree(self.dst)

    def assert_in_sync(self):
        if not wait_for(self.in_sync):
            with open(self.log) as pfile:
                print(pfile.read())
        self.assertEqual(tree(self.src), tree(self.dst))

    def test_foreground(self):
        write(os.path.join(self.src, "before.txt"), "was here before start")
        self.process = subprocess.Popen(self.args("-f"),
                                        stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL)
        # first rsync, without any event
        self.assert_in_sync()
        self.assertTrue(wait_for(lambda: os.path.exists(self.pid)))

        # new files & a deep new directory tree
        write(os.path.join(self.src, "a.txt"), "a")
        write(os.path.join(self.src, "x", "y", "z", "deep.txt"), "deep")
        self.assert_in_sync()

        # a file inside a directory that was created after the start
        write(os.path.join(self.src, "x", "y", "z", "deeper.txt"), "deeper")
        self.assert_in_sync()

        # change the same file twice (pirsyncd 1.x missed the second one)
        write(os.path.join(self.src, "a.txt"), "changed")
        self.assert_in_sync()
        write(os.path.join(self.src, "a.txt"), "changed again")
        self.assert_in_sync()

        # rename a directory, then create a file in it
        os.rename(os.path.join(self.src, "x"), os.path.join(self.src, "w"))
        self.assert_in_sync()
        write(os.path.join(self.src, "w", "y", "new.txt"), "new")
        self.assert_in_sync()

        # delete
        os.remove(os.path.join(self.src, "before.txt"))
        self.assert_in_sync()

        # stop: SIGTERM, a clean exit without pidfile
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=15), 0)
        self.assertFalse(os.path.exists(self.pid))

    def test_many_files(self):
        self.process = subprocess.Popen(self.args("-f"),
                                        stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL)
        self.assertTrue(wait_for(lambda: os.path.exists(self.pid)))
        for i in range(2000):
            write(os.path.join(self.src, f"d{i % 20:02d}", f"f{i}"),
                  str(i))
        self.assert_in_sync()
        # 2000 files must not mean 2000 rsync runs
        with open(self.log) as pfile:
            runs = pfile.read().count("rsync done")
        self.assertLess(runs, 50)

    def test_changes_before_stop_are_synced(self):
        self.process = subprocess.Popen(self.args("-f", "--delay", "30"),
                                        stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL)
        self.assertTrue(wait_for(lambda: os.path.exists(self.pid)))
        time.sleep(0.5)
        write(os.path.join(self.src, "last.txt"), "last")
        time.sleep(1.5)  # events read, but rsync waits for --delay 30
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=15), 0)
        self.assertEqual(tree(self.src), tree(self.dst))

    def test_background_status_stop(self):
        result = subprocess.run(self.args(), capture_output=True, text=True,
                                timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("runs in background", result.stdout)
        self.assertTrue(wait_for(lambda: os.path.exists(self.pid)))

        status = subprocess.run([PIRSYNCD, "-p", self.pid,
                                 "-k", "status"], capture_output=True,
                                text=True)
        self.assertEqual(status.returncode, 0)
        self.assertIn("running instance", status.stdout)

        # a second instance with the same pidfile must refuse to start
        second = subprocess.run(self.args(), capture_output=True, text=True)
        self.assertEqual(second.returncode, 1)

        write(os.path.join(self.src, "bg.txt"), "background")
        self.assert_in_sync()

        stop = subprocess.run([PIRSYNCD, "-p", self.pid,
                               "-k", "stop"], capture_output=True, text=True)
        self.assertEqual(stop.returncode, 0, stop.stdout)
        self.assertFalse(os.path.exists(self.pid))

        status = subprocess.run([PIRSYNCD, "-p", self.pid,
                                 "-k", "status"], capture_output=True,
                                text=True)
        self.assertEqual(status.returncode, 1)

    def test_source_removed(self):
        self.process = subprocess.Popen(self.args("-f"),
                                        stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL)
        self.assertTrue(wait_for(lambda: os.path.exists(self.pid)))
        os.rmdir(self.src)
        self.assertEqual(self.process.wait(timeout=15), 1)
        self.assertFalse(os.path.exists(self.pid))

    @unittest.skipUnless(os.geteuid() == 0, "rsync daemon needs root (port 873)")
    def test_rsync_daemon(self):
        # a real rsync server (rsync --daemon) with a password
        conf = os.path.join(self.tmp.name, "rsyncd.conf")
        secrets = os.path.join(self.tmp.name, "rsyncd.secrets")
        password = os.path.join(self.tmp.name, "password")
        write(conf, "pid file = " + self.tmp.name + "/rsyncd.pid\n"
                    "use chroot = no\n"
                    "[data]\n"
                    "path = " + self.dst + "\n"
                    "read only = no\n"
                    "uid = root\n"
                    "gid = root\n"
                    "auth users = pirsyncd\n"
                    "secrets file = " + secrets + "\n")
        write(secrets, "pirsyncd:secret\n")
        write(password, "secret\n")
        os.chmod(secrets, 0o600)
        os.chmod(password, 0o600)
        server = subprocess.Popen([shutil.which("rsync"), "--daemon",
                                   "--no-detach", "--config=" + conf])
        self.addCleanup(server.wait)
        self.addCleanup(server.terminate)

        write(os.path.join(self.src, "remote.txt"), "over rsync protocol")
        self.process = subprocess.Popen(
            self.args("-f", "--rsync_daemon", "pirsyncd@localhost::data",
                      "--rsync_pass_file", password),
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.assert_in_sync()
        write(os.path.join(self.src, "dir", "more.txt"), "more")
        self.assert_in_sync()

    def test_bad_config(self):
        result = subprocess.run(self.args("-s", "/does/not/exist"),
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn("source directory", result.stderr)


if __name__ == "__main__":
    unittest.main()
