#!/usr/bin/env python3
"""Exercise the real binary in a pseudo-terminal using only Python's stdlib.

Run after `make build`: python3 scripts/pty_smoke.py
Artifacts (ANSI captures and session JSON) live in a temporary directory.
If pyte is installed, decoded screen captures are saved alongside them.
"""

import fcntl
import http.server
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time


class Endpoint(http.server.BaseHTTPRequestHandler):
    counter = 0
    failure = False

    def do_GET(self):
        if self.failure:
            self.send_response(503)
            self.end_headers()
            return
        Endpoint.counter += 10
        n = Endpoint.counter
        body = f"""# HELP http_requests_total Completed HTTP requests
# TYPE http_requests_total counter
http_requests_total{{method="GET",status="200"}} {n}
http_requests_total{{method="GET",status="500"}} {n // 10}
# TYPE request_duration_seconds histogram
request_duration_seconds_bucket{{le="0.1"}} {n // 2}
request_duration_seconds_bucket{{le="1"}} {n}
request_duration_seconds_bucket{{le="+Inf"}} {n}
request_duration_seconds_count {n}
request_duration_seconds_sum {n / 4}
# TYPE memory_bytes gauge
memory_bytes {100000000 + n * 100}
# TYPE workers gauge
workers {n % 12}
""".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; version=0.0.4")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


class Terminal:
    def __init__(self, command, directory, name):
        self.master, self.slave = pty.openpty()
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 42, 132, 0, 0))
        self.before = termios.tcgetattr(self.slave)
        self.directory, self.name, self.raw = directory, name, bytearray()
        self.screen = None
        try:
            import pyte
            self.screen = pyte.Screen(132, 42)
            self.stream = pyte.ByteStream(self.screen)
        except ImportError:
            pass
        self.process = subprocess.Popen(command, stdin=self.slave, stdout=self.slave,
                                        stderr=self.slave, env={**os.environ, "TERM": "xterm-256color"})
        self.drain(0.7)

    def drain(self, seconds=0.18):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if select.select([self.master], [], [], min(0.03, max(0, deadline-time.monotonic())))[0]:
                try:
                    data = os.read(self.master, 65536)
                except OSError:
                    break
                if not data:
                    break
                self.raw.extend(data)
                if self.screen:
                    self.stream.feed(data)

    def send(self, data, delay=0.18):
        os.write(self.master, data.encode())
        self.drain(delay)

    def expect(self, text):
        deadline = time.monotonic() + 3
        while True:
            actual = "\n".join(self.screen.display) if self.screen else self.raw.decode(errors="replace")
            if text in actual:
                return
            if time.monotonic() >= deadline:
                raise AssertionError(f"missing {text!r} in {self.name}:\n{actual}")
            self.drain(0.05)

    def capture(self, suffix):
        (self.directory / f"{self.name}-{suffix}.ansi").write_bytes(self.raw)
        if self.screen:
            (self.directory / f"{self.name}-{suffix}.txt").write_text("\n".join(self.screen.display))

    def close(self, signal_number=None):
        if signal_number:
            self.process.send_signal(signal_number)
        else:
            self.send("q")
        # Keep reading while the child restores the screen: a slow CI runner
        # must not block on a full PTY output buffer during shutdown.
        deadline = time.monotonic() + 5
        while self.process.poll() is None and time.monotonic() < deadline:
            self.drain(0.05)
        self.process.wait(timeout=0.1)
        self.drain(0.05)
        assert self.process.returncode == 0, self.raw.decode(errors="replace")
        after = termios.tcgetattr(self.slave)
        before = list(self.before)
        # Darwin sets PENDIN as bookkeeping when returning to canonical input.
        # Compare the actual terminal configuration, including ECHO and ICANON.
        if sys.platform == "darwin":
            before[3] &= ~termios.PENDIN
            after[3] &= ~termios.PENDIN
        assert after == before, f"terminal attributes were not restored: before={before!r}, after={after!r}"
        assert b"\x1b[?1049l" in self.raw, "alternate screen was not restored"
        os.close(self.master)
        os.close(self.slave)


def main():
    binary = str(Path(sys.argv[1] if len(sys.argv) > 1 else "bin/termfana").resolve())
    directory = Path(tempfile.mkdtemp(prefix="termfana-pty-"))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Endpoint)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{server.server_port}/metrics"
    terminal = None
    try:
        listing = subprocess.run([binary, "list", "--format", "json", url], capture_output=True, text=True, check=True)
        assert len(json.loads(listing.stdout)) == 4
        sample = subprocess.run([binary, "sample", "--metric", "http_requests_total", "--view", "rate", "--count", "3", "--interval", "30ms", "--format", "json", url], capture_output=True, text=True, check=True)
        records = [json.loads(line) for line in sample.stdout.splitlines()]
        assert len(records) == 3 and all(r["samples"][0]["value"] > 0 for r in records)

        terminal = Terminal([binary, "--interval", "100ms", "--window", "10s", url], directory, "workspace")
        terminal.expect("METRIC EXPLORER")
        for index, metric in enumerate(["http_requests_total", "request_duration_seconds", "memory_bytes", "workers"]):
            if index:
                terminal.send("a")
            terminal.send("/" + metric + "\r")
            terminal.send("\r")
            terminal.expect(metric)
        terminal.capture("four-panels")

        terminal.send("1l")
        terminal.expect("LABEL FILTERS")
        terminal.send('/status="500"\r')
        terminal.send("\r")
        terminal.send("\x1b")
        terminal.send("g\r")
        terminal.expect("Labels:")
        terminal.expect("500")
        terminal.capture("details")
        terminal.send("\x1b")
        terminal.send("\x1b")
        terminal.send("\x1b[D")
        terminal.expect("HISTORY")
        terminal.send("r")
        Endpoint.failure = True
        terminal.drain(0.4)
        terminal.expect("Scrape failed")
        Endpoint.failure = False
        terminal.drain(0.4)

        session = directory / "workspace.json"
        terminal.send("s")
        terminal.send("\x15\x1b[200~" + str(session) + "\x1b[201~")
        terminal.send("\r")
        terminal.expect("Session saved")
        saved = json.loads(session.read_text())
        assert len(saved["panels"]) == 4 and saved["panels"][0]["labels"]["status"] == "500"
        fcntl.ioctl(terminal.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        if terminal.screen:
            terminal.screen.resize(24, 80)
        terminal.process.send_signal(signal.SIGWINCH)
        terminal.drain(0.3)
        terminal.expect("http_requests_total")
        terminal.capture("narrow")
        terminal.close()
        # Repeated exits catch races between signal delivery and cancellation.
        for attempt in range(5):
            for exit_signal in (signal.SIGINT, signal.SIGTERM):
                terminal = Terminal([binary, "--session", str(session)], directory,
                                    f"restored-{exit_signal.name}-{attempt}")
                terminal.expect("http_requests_total")
                if attempt == 0:
                    terminal.capture("loaded")
                terminal.close(exit_signal)
        terminal = None
        print(json.dumps({"result": "passed", "artifacts": str(directory), "checks": ["CLI JSON", "four panels", "label filters", "series details", "cursor", "failure recovery", "session save/load", "resize", "q/SIGINT/SIGTERM terminal restoration"]}, indent=2))
    finally:
        if terminal and terminal.process.poll() is None:
            terminal.capture("failure")
            terminal.process.kill()
            terminal.process.wait()
        server.shutdown()


if __name__ == "__main__":
    main()
