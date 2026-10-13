"""Drive an attached herdr client in a pty and check what its screen shows.

usage: python3 -I ptyclient.py SCRIPT RAWLOG -- CMD...

Based on X0's ptyclient (nhm-qbi.1); nhm-qbi.11 adds named keys, a model
of the client's screen and expectations. herdr's client draws with
absolute cursor moves and sends only the cells that change, so the raw
stream is not the screen: the script's checks read a grid that this
driver keeps from the stream (cursor moves, erases, the alternate screen,
text; colours and titles are dropped).

SCRIPT is a text file, one action per line:
  sleep SECONDS
  send TEXT            TEXT with Python escapes (\\x1b, \\r, \\x02 ...)
  key NAME...          named keys: esc enter tab space up down left right
                       backspace pgup pgdown home end ctrl+X alt+X, or one
                       character
  paste FILE           FILE's content wrapped in bracketed paste
  resize COLS ROWS     set the pty size (the kernel sends SIGWINCH)
  run SHELLCMD         run a shell command (its output to stderr), wait
  check SHELLCMD       run a shell command; it passes when it exits 0
  expect SECONDS TEXT  within SECONDS the screen shows TEXT
  refute SECONDS TEXT  after SECONDS the screen does not show TEXT
  screen               print the screen to stderr
  note TEXT            a line in stderr
Each check, expect and refute prints "PASS line N: ..." or "FAIL line N: ..."
(a FAIL prints the screen to stderr). The client's raw output is appended
to RAWLOG. The client gets SIGTERM after the last action, and SIGKILL
when it has not exited 5 seconds later. A run or check that takes longer
than PTY_CMD_TIMEOUT seconds (default 60) is killed, and a check fails
then. When the client's output ends, an expect fails at once. The exit
status is 1 when an expectation failed.
"""
import codecs
import fcntl
import os
import pty
import select
import signal
import struct
import subprocess
import sys
import termios
import time

KEYS = {
    "esc": b"\x1b", "enter": b"\r", "tab": b"\t", "space": b" ",
    "up": b"\x1b[A", "down": b"\x1b[B", "right": b"\x1b[C", "left": b"\x1b[D",
    "backspace": b"\x7f", "pgup": b"\x1b[5~", "pgdown": b"\x1b[6~",
    "home": b"\x1b[H", "end": b"\x1b[F",
}


def keybytes(name):
    if name in KEYS:
        return KEYS[name]
    if name.startswith("ctrl+") and len(name) == 6:
        return bytes([ord(name[5].lower()) & 0x1f])
    if name.startswith("alt+") and len(name) == 5:
        return b"\x1b" + name[4].encode()
    if len(name) == 1:
        return name.encode()
    raise ValueError("unknown key %r" % name)


class Screen:
    """A grid of characters fed with a terminal's output stream."""

    def __init__(self, cols, rows):
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.state, self.buf = "text", ""
        self.resize(cols, rows)

    def resize(self, cols, rows):
        self.cols, self.rows = cols, rows
        self.clear()

    def clear(self):
        self.grid = [[" "] * self.cols for _ in range(self.rows)]
        self.row = self.col = 0

    def text(self):
        return "\n".join("".join(r).rstrip() for r in self.grid)

    def put(self, ch):
        if 0 <= self.row < self.rows and 0 <= self.col < self.cols:
            self.grid[self.row][self.col] = ch
        self.col += 1

    def erase(self, row, start, end):
        if 0 <= row < self.rows:
            for c in range(max(0, start), min(self.cols, end)):
                self.grid[row][c] = " "

    def csi(self, seq):
        final, body = seq[-1], seq[:-1]
        private = body.startswith("?")
        nums = [int(p) if p.isdigit() else 0 for p in body.lstrip("?").split(";")] if body.lstrip("?") else []

        def n(i, d=1):
            return nums[i] if len(nums) > i and nums[i] else d

        if private:
            if final in "hl" and any(m in (47, 1047, 1049) for m in nums):
                self.clear()
            return
        if final in "Hf":
            self.row, self.col = n(0) - 1, n(1) - 1
        elif final == "A":
            self.row = max(0, self.row - n(0))
        elif final == "B":
            self.row = min(self.rows - 1, self.row + n(0))
        elif final == "C":
            self.col = min(self.cols - 1, self.col + n(0))
        elif final == "D":
            self.col = max(0, self.col - n(0))
        elif final == "G":
            self.col = n(0) - 1
        elif final == "d":
            self.row = n(0) - 1
        elif final == "J":
            mode = nums[0] if nums else 0
            if mode in (2, 3):
                self.grid = [[" "] * self.cols for _ in range(self.rows)]
            elif mode == 0:
                self.erase(self.row, self.col, self.cols)
                for r in range(self.row + 1, self.rows):
                    self.erase(r, 0, self.cols)
            elif mode == 1:
                for r in range(0, self.row):
                    self.erase(r, 0, self.cols)
                self.erase(self.row, 0, self.col + 1)
        elif final == "K":
            mode = nums[0] if nums else 0
            if mode == 0:
                self.erase(self.row, self.col, self.cols)
            elif mode == 1:
                self.erase(self.row, 0, self.col + 1)
            else:
                self.erase(self.row, 0, self.cols)
        elif final == "X":
            self.erase(self.row, self.col, self.col + n(0))

    def feed(self, data):
        for ch in self.decoder.decode(data):
            if self.state == "text":
                if ch == "\x1b":
                    self.state = "esc"
                elif ch == "\r":
                    self.col = 0
                elif ch == "\n":
                    self.row = min(self.rows - 1, self.row + 1)
                elif ch == "\b":
                    self.col = max(0, self.col - 1)
                elif ch == "\t":
                    self.col = min(self.cols - 1, (self.col // 8 + 1) * 8)
                elif ch >= " " and ch != "\x7f":
                    self.put(ch)
            elif self.state == "esc":
                if ch == "[":
                    self.state, self.buf = "csi", ""
                elif ch in "]PX^_":
                    self.state = "string"
                else:
                    self.state = "text"
            elif self.state == "csi":
                self.buf += ch
                if "@" <= ch <= "~":
                    self.csi(self.buf)
                    self.state = "text"
            elif self.state == "string":
                # OSC, DCS and the like end with BEL or ESC \.
                if ch == "\x07":
                    self.state = "text"
                elif ch == "\x1b":
                    self.state = "string-esc"
            elif self.state == "string-esc":
                self.state = "text" if ch == "\\" else "string"


def setsize(fd, cols, rows):
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


def shell(cmd, timeout):
    """Run cmd with bash, in a session of its own, for at most timeout
    seconds. Return its exit status (None after the timeout) and its
    output. After the timeout the whole process group gets SIGKILL: a
    child of bash that holds the output pipe would hang a wait for EOF."""
    p = subprocess.Popen(["bash", "-c", cmd], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         text=True, start_new_session=True)
    try:
        out, _ = p.communicate(timeout=timeout)
        return p.returncode, out
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(p.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        out, _ = p.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        # A process that left the group still holds the pipe.
        out = ""
        p.stdout.close()
        p.wait()
    return None, out


def main():
    script, rawlog = sys.argv[1], sys.argv[2]
    cmd = sys.argv[sys.argv.index("--") + 1:]
    cols, rows = int(os.environ.get("PTY_COLS", "120")), int(os.environ.get("PTY_ROWS", "40"))
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)
    setsize(fd, cols, rows)
    screen = Screen(cols, rows)
    out = open(rawlog, "ab")
    failed = 0
    cmd_timeout = float(os.environ.get("PTY_CMD_TIMEOUT", "60"))
    # eof[0] is set when the client's output ends (EIO on Linux): select
    # then always reports the pty readable, so pump only waits.
    eof = [False]

    def pump(seconds):
        end = time.time() + seconds
        while True:
            left = end - time.time()
            if left <= 0:
                return
            if eof[0]:
                time.sleep(left)
                return
            r, _, _ = select.select([fd], [], [], left)
            if r:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    data = b""
                if not data:
                    eof[0] = True
                    log("the client's output ended")
                    continue
                out.write(data)
                out.flush()
                screen.feed(data)

    def log(s):
        sys.stderr.write("%s ptyclient: %s\n" % (time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), s))
        sys.stderr.flush()

    def dump():
        sys.stderr.write("---- screen %dx%d\n%s\n----\n" % (screen.cols, screen.rows, screen.text()))
        sys.stderr.flush()

    pump(1.0)
    with open(script) as f:
        lines = f.read().splitlines()
    for n, line in enumerate(lines, 1):
        if not line or line.startswith("#"):
            continue
        verb, _, arg = line.partition(" ")
        if verb == "sleep":
            pump(float(arg))
        elif verb == "send":
            os.write(fd, arg.encode().decode("unicode_escape").encode("latin-1"))
            log("send %r" % arg)
            pump(0.3)
        elif verb == "key":
            for name in arg.split():
                os.write(fd, keybytes(name))
                log("key %s" % name)
                pump(0.3)
        elif verb == "paste":
            with open(arg, "rb") as p:
                data = p.read()
            os.write(fd, b"\x1b[200~" + data + b"\x1b[201~")
            log("paste %d bytes" % len(data))
            pump(0.5)
        elif verb == "resize":
            c, r = (int(x) for x in arg.split())
            setsize(fd, c, r)
            screen.resize(c, r)
            log("resize %dx%d" % (c, r))
            pump(0.5)
        elif verb in ("run", "check"):
            log("%s %s" % (verb, arg))
            code, output = shell(arg, cmd_timeout)
            sys.stderr.write(output)
            if code is None:
                log("killed after %g seconds" % cmd_timeout)
            else:
                log("exit %d" % code)
            if verb == "check":
                if code != 0:
                    failed += 1
                print("%s line %d: check %s" % ("PASS" if code == 0 else "FAIL", n, arg), flush=True)
            pump(0.3)
        elif verb in ("expect", "refute"):
            secs, _, want = arg.partition(" ")
            end = time.time() + float(secs)
            if verb == "expect":
                while want not in screen.text() and time.time() < end and not eof[0]:
                    pump(min(0.2, max(0.0, end - time.time())))
                ok = want in screen.text()
            else:
                pump(max(0.0, end - time.time()))
                ok = want not in screen.text()
            if not ok:
                failed += 1
            print("%s line %d: %s %r" % ("PASS" if ok else "FAIL", n, verb, want), flush=True)
            if not ok:
                dump()
        elif verb == "screen":
            dump()
        elif verb == "note":
            log(arg)
        else:
            failed += 1
            print("FAIL line %d: unknown action %r" % (n, line), flush=True)
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    pump(0.5)
    end = time.time() + 5
    while os.waitpid(pid, os.WNOHANG) == (0, 0):
        if time.time() > end:
            log("the client did not exit on SIGTERM: SIGKILL")
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
            break
        time.sleep(0.1)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
