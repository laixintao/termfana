#!/usr/bin/env python3
"""Check that bumpversion, the CLI, and an optional release tag agree."""

import argparse
import configparser
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
SEMVER = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"


def version_tuple(value):
    if not re.fullmatch(SEMVER, value):
        raise ValueError(f"expected a stable major.minor.patch version, got {value!r}")
    return tuple(map(int, value.split(".")))


def read_version(tag=None):
    config = configparser.ConfigParser(interpolation=None)
    config.read(ROOT / ".bumpversion.cfg")
    version = config["bumpversion"]["current_version"]
    version_tuple(version)
    source = (ROOT / "internal/cli/cli.go").read_text()
    match = re.search(r'^var Version = "([^"]+)"$', source, re.MULTILINE)
    if not match or match[1] != version:
        raise ValueError("CLI version does not match .bumpversion.cfg")
    if tag is not None and tag != f"v{version}":
        raise ValueError(f"release tag {tag!r} does not match v{version}")
    return version


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag")
    args = parser.parse_args()
    try:
        print(read_version(args.tag))
    except (ValueError, KeyError) as error:
        parser.exit(1, f"Version check failed: {error}\n")
