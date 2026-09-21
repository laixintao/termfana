#!/usr/bin/env python3
"""Build standalone release archives and their SHA-256 checksums."""

import hashlib
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

from version import ROOT, read_version


def main():
    version = read_version()
    dist = ROOT / "dist"
    dist.mkdir(exist_ok=True)
    checksums = []
    for target_os in ("linux", "darwin"):
        for arch in ("amd64", "arm64"):
            name = f"termfana_{version}_{target_os}_{arch}.tar.gz"
            archive = dist / name
            with tempfile.TemporaryDirectory(prefix="termfana-build-") as directory:
                binary = Path(directory) / "termfana"
                subprocess.run(
                    [os.environ.get("GO", "go"), "build", "-trimpath", "-ldflags=-s -w", "-o", str(binary), "./cmd/termfana"],
                    cwd=ROOT,
                    env={**os.environ, "CGO_ENABLED": "0", "GOOS": target_os, "GOARCH": arch},
                    check=True,
                )
                with tarfile.open(archive, "w:gz") as bundle:
                    bundle.add(binary, arcname="termfana")
                    bundle.add(ROOT / "README.md", arcname="README.md")
                    bundle.add(ROOT / "README.zh-CN.md", arcname="README.zh-CN.md")
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            checksums.append(f"{digest}  {name}\n")
            print(name, flush=True)
    (dist / "SHA256SUMS").write_text("".join(checksums))


if __name__ == "__main__":
    main()
