#!/usr/bin/env python3
"""Verify a built executable in an isolated folder, without source or web assets."""

import argparse
import hashlib
import json
from pathlib import Path
import queue
import re
import shutil
import subprocess
import tempfile
import threading
import urllib.parse
import urllib.request


def start(binary, data):
    process = subprocess.Popen(
        [str(binary), "--port", "0", "--data", str(data), "--no-browser"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    lines = queue.Queue()

    def read_output():
        for line in process.stdout:
            lines.put(line.rstrip())
        lines.put(None)

    threading.Thread(target=read_output, daemon=True).start()
    try:
        while True:
            line = lines.get(timeout=15)
            if line is None:
                raise RuntimeError("Executable exited before starting its server")
            if line.startswith("Desktop: "):
                return process, line.removeprefix("Desktop: ").strip()
    except BaseException:
        stop(process)
        raise


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    if process.stdout:
        process.stdout.close()


def request(base, path, data=None, headers=None):
    req = urllib.request.Request(base + path, data=data, headers=headers or {})
    return urllib.request.urlopen(req, timeout=10)


def state(base):
    with request(base, "/api/state") as response:
        return json.load(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    args = parser.parse_args()
    source = args.binary.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="pageport-portable-test-") as temporary:
        folder = Path(temporary)
        binary = folder / source.name
        shutil.copy2(source, binary)
        binary.chmod(0o755)
        data = folder / "library"
        process, base = start(binary, data)
        try:
            with request(base, "/") as response:
                html = response.read().decode()
            token = re.search(r'name="pageport-token" content="([a-f0-9]+)"', html).group(1)
            for path in ("/app.js", "/style.css", "/favicon.svg"):
                with request(base, path) as response:
                    assert response.status == 200 and response.read(), path
            assert state(base)["files"] == []

            second = subprocess.run(
                [str(binary), "--port", "0", "--data", str(data), "--no-browser"],
                capture_output=True, text=True, timeout=15,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )
            assert second.returncode != 0
            assert "already open" in second.stderr.lower(), second.stderr

            payload = b"Portable executable: exact byte transfer and persistence test.\n"
            boundary = "pageport-portable-smoke-boundary"
            multipart = (
                f'--{boundary}\r\nContent-Disposition: form-data; name="files"; filename="A portable test.txt"\r\n'
                "Content-Type: text/plain\r\n\r\n"
            ).encode() + payload + f"\r\n--{boundary}--\r\n".encode()
            with request(base, "/api/files", multipart, {
                "Content-Type": "multipart/form-data; boundary=" + boundary,
                "X-Pageport-Token": token,
            }) as response:
                book = json.load(response)["files"][0]
            session = state(base)
            download = "/download/" + session["sessionCode"] + "/" + book["id"] + "/" + urllib.parse.quote(book["name"])
            with request(base, download) as response:
                assert response.headers["Content-Disposition"].startswith("attachment;")
                assert int(response.headers["Content-Length"]) == len(payload)
                assert hashlib.sha256(response.read()).digest() == hashlib.sha256(payload).digest()
            with request(base, download, headers={"Range": "bytes=0-7"}) as response:
                assert response.status == 206 and response.read() == payload[:8]
            with request(base, "/read/" + session["sessionCode"]) as response:
                assert b"A portable test.txt" in response.read()
        finally:
            stop(process)

        # Terminating and reopening verifies lock release as well as durable metadata.
        process, base = start(binary, data)
        try:
            saved = state(base)["files"]
            assert len(saved) == 1 and saved[0]["name"] == "A portable test.txt"
            assert saved[0]["size"] == len(payload)
        finally:
            stop(process)
    print("PASS: standalone assets, library lock, upload, exact download, ranges, and restart persistence.")


if __name__ == "__main__":
    main()
