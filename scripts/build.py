#!/usr/bin/env python3
"""Build Pageport's standalone executables and portable ZIP archives."""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from typing import Iterator
import zipfile


ROOT = Path(__file__).resolve().parent.parent
TARGETS = {
    "windows-amd64": ("windows", "amd64"),
    "windows-arm64": ("windows", "arm64"),
    "macos-arm64": ("darwin", "arm64"),
    "macos-amd64": ("darwin", "amd64"),
    "linux-amd64": ("linux", "amd64"),
    "linux-arm64": ("linux", "arm64"),
}


def run(command: list[str], workdir: Path, env: dict[str, str] | None = None) -> None:
    print("+ " + " ".join(command), flush=True)
    subprocess.run(command, cwd=workdir, env=env, check=True)


@contextmanager
def source_tree() -> Iterator[Path]:
    """Go's module locking needs a local filesystem when building on Windows."""
    if os.name != "nt" or not str(ROOT).startswith("\\\\"):
        yield ROOT
        return

    with tempfile.TemporaryDirectory(prefix="pageport-build-") as temporary:
        staged = Path(temporary)
        for source in ROOT.glob("*.go"):
            shutil.copy2(source, staged / source.name)
        for name in ("go.mod", "go.sum"):
            source = ROOT / name
            if source.exists():
                shutil.copy2(source, staged / name)
        for name in ("web", "internal"):
            source = ROOT / name
            if source.is_dir():
                shutil.copytree(source, staged / name)
        print("Building from a temporary local copy (Windows network-path compatibility).", flush=True)
        yield staged


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--target", action="append", choices=TARGETS,
        help="Platform to build; repeat to select several. Default: all platforms.",
    )
    parser.add_argument("--version", default="dev", help="Version embedded in the app.")
    parser.add_argument("--output", type=Path, default=ROOT / "dist", help="Output folder.")
    parser.add_argument("--skip-tests", action="store_true", help="Skip already completed Go tests.")
    args = parser.parse_args()

    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", args.version):
        parser.error("--version must contain only letters, numbers, dots, underscores, and hyphens")
    go = shutil.which("go")
    if go is None:
        parser.error("Go was not found in PATH. Install Go on the build machine first.")

    output = args.output.expanduser().resolve()
    output.mkdir(parents=True, exist_ok=True)
    targets = list(dict.fromkeys(args.target or TARGETS))

    artifacts: list[Path] = []
    with source_tree() as workdir:
        if not args.skip_tests:
            run([go, "test", "./..."], workdir)

        for target in targets:
            goos, goarch = TARGETS[target]
            suffix = ".exe" if goos == "windows" else ""
            binary = output / f"Pageport-{target}{suffix}"
            # Also link to the local staging folder before copying to a UNC destination.
            build_binary = workdir / binary.name if workdir != ROOT else binary
            env = os.environ.copy()
            env.update({"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"})
            run([
                go, "build", "-trimpath", "-ldflags", f"-s -w -X main.version={args.version}",
                "-o", str(build_binary), ".",
            ], workdir, env=env)
            if build_binary != binary:
                shutil.copy2(build_binary, binary)
            if goos != "windows":
                binary.chmod(0o755)

            archive = output / f"Pageport-{target}.zip"
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
                binary_info = zipfile.ZipInfo.from_file(binary, arcname=f"Pageport{suffix}")
                binary_info.create_system = 3
                binary_info.external_attr = (0o100755 << 16)
                binary_info.compress_type = zipfile.ZIP_DEFLATED
                with binary.open("rb") as source, bundle.open(binary_info, "w") as destination:
                    shutil.copyfileobj(source, destination, length=1024 * 1024)
                bundle.write(ROOT / "README.md", arcname="README.md")
            artifacts.extend((binary, archive))
            print(f"Created {archive}", flush=True)

    checksums = output / "SHA256SUMS"
    checksums.write_text(
        "".join(f"{sha256(path)}  {path.name}\n" for path in artifacts), encoding="utf-8",
    )
    print(f"Built {len(targets)} platform(s). Checksums: {checksums}", flush=True)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as error:
        print(f"Build stopped: command exited with status {error.returncode}.", file=sys.stderr)
        sys.exit(error.returncode or 1)
