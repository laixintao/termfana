#!/usr/bin/env python3
"""Bump the version, then atomically push the current branch and its new tag."""

import argparse
import os
import shlex
import shutil
import subprocess

from version import ROOT, read_version, version_tuple


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("part", nargs="?", choices=("patch", "minor", "major"), default="patch")
    parser.add_argument("--new-version")
    args = parser.parse_args()
    os.chdir(ROOT)
    if not shutil.which("bumpversion"):
        parser.exit(1, "Install bumpversion first: pipx install bump2version==1.0.1\n")
    current = read_version()
    if git("status", "--porcelain"):
        parser.exit(1, "Commit or stash all changes before releasing.\n")
    branch = git("symbolic-ref", "--quiet", "--short", "HEAD")
    git("remote", "get-url", "origin")
    command = ["bumpversion", args.part]
    if args.new_version:
        version_tuple(args.new_version)
        command += ["--new-version", args.new_version]
    preview = subprocess.check_output([*command, "--dry-run", "--list"], text=True)
    proposed = dict(line.split("=", 1) for line in preview.splitlines() if "=" in line)["new_version"]
    if version_tuple(proposed) <= version_tuple(current):
        parser.exit(1, f"New version must be greater than {current}.\n")
    subprocess.run(command, check=True)
    tag = f"v{proposed}"
    read_version(tag)
    # Explicit refspecs push exactly one release tag. Atomic push prevents a
    # rejected branch update from publishing a tag on its own.
    push = ["git", "push", "--atomic", "origin", f"HEAD:refs/heads/{branch}", f"refs/tags/{tag}:refs/tags/{tag}"]
    try:
        subprocess.run(push, check=True)
    except subprocess.CalledProcessError:
        print(f"{tag} exists locally; the push failed. Resolve the Git error, then retry:\n{shlex.join(push)}", flush=True)
        return 1
    print(f"Pushed {tag}. GitHub Actions will test, build, and publish the release.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"Release failed: {error}") from error
