"""A fake Agent Mail server for nhm's surfaces step (tests/surfaces_e2e.sh).

usage: python3 -I fake_agentmail.py PORTFILE LOG

It serves Agent Mail's HTTP transport (one JSON-RPC "tools/call" per POST,
the tool's result as JSON text in result.content[0].text) on 127.0.0.1, at
a free port. It writes the port to PORTFILE when it listens. Its tools:

  health_check          ok
  fetch_inbox           IcyKnoll's inbox: one unread message, until a call
                        marks it read; any other agent's inbox is empty
  mark_message_read,    mark the message read
  acknowledge_message

Any other tool is a JSON-RPC error, so a call that the test does not
expect shows. A fetch_inbox marks the message read unless it sends
mark_read false, so a peek that reads the mail changes the next one. Each
call is a JSON line in LOG: the time, the tool and its arguments.
"""
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MESSAGE = {
    "id": 4101,
    "subject": "SURFACE-MAIL please review the board",
    "from": "BlueLake",
    "thread_id": "nhm-surfaces",
    "created_ts": "2026-10-13T00:00:00Z",
    "importance": "normal",
    "ack_required": False,
}

lock = threading.Lock()
state = {"read": False}
logfile = None


def record(tool, args):
    line = json.dumps({"ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "tool": tool, "args": args})
    with lock:
        logfile.write(line + "\n")
        logfile.flush()


def call(tool, args):
    """The tool's result, or None for a tool the fake does not know."""
    if tool == "health_check":
        return {"status": "ok"}
    if tool == "fetch_inbox":
        if str(args.get("agent_name", "")).lower() != "icyknoll":
            return []
        with lock:
            unread = not state["read"]
            if args.get("mark_read") is not False:
                state["read"] = True
        if not unread and args.get("unread_only", False):
            return []
        msg = dict(MESSAGE)
        if not unread:
            msg["read_ts"] = "2026-10-13T00:00:01Z"
        return [msg]
    if tool in ("mark_message_read", "acknowledge_message"):
        with lock:
            state["read"] = True
        return {"ok": True}
    return None


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        try:
            req = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            params = req.get("params") or {}
            tool, args = params.get("name", ""), params.get("arguments") or {}
        except (ValueError, AttributeError):
            self.answer({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
            return
        record(tool, args)
        out = call(tool, args) if req.get("method") == "tools/call" else None
        if out is None:
            self.answer({"jsonrpc": "2.0", "id": req.get("id"),
                         "error": {"code": -32601, "message": "the fake Agent Mail has no tool %r" % tool}})
            return
        self.answer({"jsonrpc": "2.0", "id": req.get("id"),
                     "result": {"content": [{"type": "text", "text": json.dumps(out)}], "isError": False}})

    def answer(self, body):
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_):
        pass


def main():
    global logfile
    portfile, logpath = sys.argv[1], sys.argv[2]
    logfile = open(logpath, "a")
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    tmp = portfile + ".tmp"
    with open(tmp, "w") as f:
        f.write("%d\n" % server.server_address[1])
    os.replace(tmp, portfile)
    server.serve_forever()


if __name__ == "__main__":
    main()
