"""Exercise the real bumpversion command and Git pushes in disposable repos."""

from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from version import ROOT


class ReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="termfana-release-test-")
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name) / "repo"
        self.remote = Path(self.temp.name) / "remote.git"
        self.repo.mkdir()
        self.git("init", "--initial-branch=master")
        for key, value in (("user.name", "Release Test"), ("user.email", "test@example.invalid"),
                           ("commit.gpgsign", "false"), ("tag.gpgsign", "false"),
                           ("core.hooksPath", "/dev/null")):
            self.git("config", key, value)
        for name in (".bumpversion.cfg", "internal/cli/cli.go", "scripts/release.py", "scripts/version.py", ".gitignore"):
            target = self.repo / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        self.git("add", ".")
        self.git("commit", "-m", "Initial fixture")
        self.git("init", "--bare", str(self.remote))
        self.git("remote", "add", "origin", str(self.remote))
        self.git("push", "origin", "HEAD:refs/heads/master")
        self.current = self.run_script("version.py").stdout.strip()

    def git(self, *args, check=True):
        result = subprocess.run(["git", *args], cwd=self.repo, capture_output=True, text=True)
        if check:
            self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def run_script(self, name, *args):
        return subprocess.run([sys.executable, str(self.repo / "scripts" / name), *args],
                              cwd=self.repo, capture_output=True, text=True)

    def test_bump_commits_tags_and_pushes(self):
        major, minor, patch = map(int, self.current.split("."))
        for part, expected, extra in (
            ("patch", f"{major}.{minor}.{patch + 1}", ()),
            ("minor", f"{major}.{minor + 1}.0", ()),
            ("major", f"{major + 1}.0.0", ()),
            ("patch", f"{major + 1}.2.3", ("--new-version", f"{major + 1}.2.3")),
        ):
            with self.subTest(part=part, expected=expected):
                result = self.run_script("release.py", part, *extra)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                tag = f"v{expected}"
                self.assertEqual(self.run_script("version.py", "--tag", tag).stdout.strip(), expected)
                self.assertEqual(self.git("status", "--porcelain"), "")
                self.assertEqual(self.git("rev-parse", "HEAD"), self.git("rev-parse", f"{tag}^{{commit}}"))
                self.assertEqual(self.git("cat-file", "-t", tag), "tag")
                self.assertEqual(self.git("rev-parse", "HEAD"),
                                 self.git("--git-dir", str(self.remote), "rev-parse", "refs/heads/master"))
                self.assertEqual(self.git("rev-parse", tag),
                                 self.git("--git-dir", str(self.remote), "rev-parse", f"refs/tags/{tag}"))

    def test_dirty_worktree_and_nonincreasing_version_are_rejected(self):
        head = self.git("rev-parse", "HEAD")
        dirty = self.repo / "uncommitted.txt"
        dirty.write_text("work in progress")
        result = self.run_script("release.py")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Commit or stash", result.stderr)
        dirty.unlink()
        for value in (self.current, "0.0.0", "1.2.03", "1.2.3-rc1"):
            result = self.run_script("release.py", "patch", "--new-version", value)
            self.assertNotEqual(result.returncode, 0, value)
        self.assertEqual(self.git("rev-parse", "HEAD"), head)
        self.assertEqual(self.git("tag", "--list"), "")

    def test_rejected_branch_push_does_not_publish_tag(self):
        # Publish a commit on origin, then return locally to the old commit.
        # Only the disposable fixture is reset.
        old_head = self.git("rev-parse", "HEAD")
        (self.repo / "upstream.txt").write_text("another maintainer's change")
        self.git("add", ".")
        self.git("commit", "-m", "Upstream change")
        self.git("push", "origin", "HEAD:refs/heads/master")
        self.git("reset", "--hard", old_head)
        result = self.run_script("release.py")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exists locally; the push failed", result.stdout)
        self.assertTrue(self.git("tag", "--list"))
        self.assertEqual(self.git("--git-dir", str(self.remote), "tag", "--list"), "")

    def test_version_check_rejects_wrong_tag_and_source(self):
        self.assertNotEqual(self.run_script("version.py", "--tag", "v999.0.0").returncode, 0)
        source = self.repo / "internal/cli/cli.go"
        source.write_text(source.read_text().replace(f'var Version = "{self.current}"', 'var Version = "999.0.0"'))
        result = self.run_script("version.py")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match", result.stderr)


if __name__ == "__main__":
    unittest.main()
