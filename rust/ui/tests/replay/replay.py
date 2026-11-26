#!/usr/bin/env python3
"""HTTP replay: Go's `tfstackplan ui` against the Rust port, same config, same requests.

Starts a fake tier serve (which echoes what it received), runs both binaries against it
with one config and one session secret, sends every case below to each, and compares the
status, the headers that matter, the body, and what the fake tier received for that
request. Session cookies and approval states are sealed by Go's codec (`_goseal`), so the
Rust side is also checked against Go-sealed tokens.

No real credentials or cloud resources: HOME points at an empty directory so neither side
finds Application Default Credentials, and the tiers run unauthenticated. Cases that need
a verified Google id_token or a real code exchange are not replayable (Go's verifier
fetches Google's certs URL and its OAuth endpoint is fixed); the crate's ported tests
cover those.

    python3 replay.py --go-bin rust/.go/tfstackplan-go --rust-bin <ui example binary>

The ui example binary is `cargo build -p tfstackplan-ui --example ui`. Built in the
rust:1.98.1-slim-trixie image it may need a newer glibc than the host: pass
`--rust-bin in-docker.sh` with UI_BIN set to the binary to run it in that image.
"""

import argparse
import hashlib
import hmac
import http.client
import http.server
import json
import os
import re
import socket
import subprocess
import sys
import tempfile
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SECRET = "s3cret"
GH_SECRET = "gh-secret"
GRANT = "projects/proj-1/locations/global/entitlements/iam/grants/g1"

# --- the fake tier -----------------------------------------------------------------------

RECEIVED = []
RECEIVED_LOCK = threading.Lock()


class Tier(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, *args):
        pass

    def _record(self, body=b""):
        h = {k.lower(): v for k, v in self.headers.items()
             if k.lower() in ("authorization", "last-event-id", "accept", "content-type",
                              "x-github-event", "x-github-delivery", "x-hub-signature-256",
                              "x-hub-signature")}
        rec = {"method": self.command, "target": self.path, "headers": h,
               "body": body.decode("utf-8", "replace")}
        with RECEIVED_LOCK:
            RECEIVED.append(rec)
        return rec

    def _send(self, status, body, headers=()):
        self.send_response_only(status)
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()
        if isinstance(body, str):
            body = body.encode()
        self.wfile.write(body)

    def do_GET(self):
        rec = self._record()
        path = self.path.split("?", 1)[0]
        if path == "/api/execution/nope":
            return self._send(404, "not found\n", [("Content-Type", "text/plain; charset=utf-8")])
        if path == "/api/execution/noct":
            return self._send(200, "plain words, no content type")
        if path == "/api/execution/empty":
            return self._send(204, b"")
        if path == "/api/execution/boom":
            return self._send(500, "boom\n", [("Content-Type", "text/plain")])
        if path.startswith("/api/execution/") and path.endswith("/events"):
            self.send_response_only(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            self.wfile.write(b"data: changed\n\n")
            self.wfile.flush()
            time.sleep(0.05)
            self.wfile.write(b"event: superseded\ndata: e2\n\n")
            return
        if path.startswith("/logs/"):
            return self._send(200, "id: 42\ndata: " + json.dumps(rec, sort_keys=True) + "\n\n",
                              [("Content-Type", "text/event-stream")])
        if path.startswith("/plan/"):
            return self._send(200, "<h3>" + json.dumps(rec, sort_keys=True) + "</h3>",
                              [("Content-Type", "text/html; charset=utf-8")])
        return self._send(200, json.dumps(rec, sort_keys=True),
                          [("Content-Type", "application/json")])

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        rec = self._record(self.rfile.read(n))
        if self.path == "/github/webhook":
            return self._send(204, b"")
        return self._send(202, json.dumps(rec, sort_keys=True))


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


# --- the two services --------------------------------------------------------------------

CONFIG = """
ui {
  public_base_url           = "https://ui.example.com/"
  session_secret_env        = "TFSTACKPLAN_UI_SESSION_SECRET"
  github_webhook_secret_env = "TFSTACKPLAN_UI_GITHUB_WEBHOOK_SECRET"
  tier "nonprod" { url = "%(tier)s" }
  tier "prod" { url = "%(dead)s" }
  oauth {
    client_id         = "test-client.apps.googleusercontent.com"
    client_secret_env = "TFSTACKPLAN_UI_OAUTH_SECRET"
    allowed_domain    = "Example.com"
    quota_project     = "svc-proj"
  }
}
"""


def start(cmd, port, env, log):
    p = subprocess.Popen(cmd + ["-addr", "127.0.0.1:%d" % port], env=env,
                         stdout=log, stderr=log)
    deadline = time.time() + 60
    while time.time() < deadline:
        try:
            c = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
            c.request("GET", "/ready")
            if c.getresponse().status == 200:
                return p
        except OSError:
            time.sleep(0.2)
    p.kill()
    sys.exit("service on port %d did not come up; see %s" % (port, log.name))


def goseal(*args):
    return subprocess.check_output(
        ["go", "run", os.path.join(HERE, "_goseal", "main.go"), SECRET] + list(args),
        cwd=HERE, text=True)


# --- requests and normalization ----------------------------------------------------------

KEEP_HEADERS = ["content-type", "location", "allow", "x-content-type-options", "cache-control",
                "accept-ranges", "set-cookie"]


def send(port, method, target, headers=None, body=None):
    with RECEIVED_LOCK:
        RECEIVED.clear()
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    c.putrequest(method, target, skip_accept_encoding=True)
    for k, v in (headers or {}).items():
        c.putheader(k, v)
    if body is not None:
        c.putheader("Content-Length", str(len(body)))
    c.endheaders(body)
    r = c.getresponse()
    data = r.read() if method != "HEAD" else b""
    hs = {}
    for k in KEEP_HEADERS:
        vals = r.headers.get_all(k) or []
        if vals:
            hs[k] = vals
    time.sleep(0.05)
    with RECEIVED_LOCK:
        got = list(RECEIVED)
    return {"status": r.status, "headers": hs, "body": data.decode("utf-8", "replace"),
            "tier": got}


STATE_RE = re.compile(r"(state=)[A-Za-z0-9_\-%]+")
COOKIE_RE = re.compile(r"^(tfsp_ui_state=)[A-Za-z0-9_\-]+")


def normalize(r, port):
    r = json.loads(json.dumps(r))
    r["body"] = STATE_RE.sub(r"\1STATE", r["body"])
    for k, vals in r["headers"].items():
        out = []
        for v in vals:
            v = STATE_RE.sub(r"\1STATE", v)
            v = COOKIE_RE.sub(r"\1STATE", v)
            out.append(v)
        r["headers"][k] = out
    # reqwest always sends `Accept: */*` when the request has no Accept; Go's client sends
    # none. The two mean the same (RFC 9110 12.5.1), so the replay ignores that one value.
    for rec in r["tier"]:
        if rec["headers"].get("accept") == "*/*":
            del rec["headers"]["accept"]
    r["body"] = r["body"].replace('"accept": "*/*", ', "").replace(', "accept": "*/*"', "")
    r["body"] = r["body"].replace('{"accept": "*/*"}', "{}")
    return r


# --- the cases ---------------------------------------------------------------------------

def sign(body):
    return "sha256=" + hmac.new(GH_SECRET.encode(), body, hashlib.sha256).hexdigest()


def cases(sess, other, expired, approve_states):
    S = {"Cookie": "tfsp_ui_session=" + sess}
    O = {"Cookie": "tfsp_ui_session=" + other}
    # The approve start also needs a same-origin navigation (the SPA's popup sends it);
    # the Rust ui refuses it otherwise, Go ignores the header.
    SO = dict(S, **{"Sec-Fetch-Site": "same-origin"})
    q = lambda s: __import__("urllib.parse").parse.quote(s, safe="")
    g = q(GRANT)
    out = [
        # health and the SPA
        ("GET", "/ready", {}, None),
        ("HEAD", "/ready", {}, None),
        ("GET", "/", {}, None),
        ("GET", "/pr/7", {}, None),
        ("GET", "/tiers/nonprod/executions/e1", {}, None),
        ("GET", "/index.html", {}, None),
        ("GET", "/index.html?x=1", {}, None),
        ("GET", "/%2E", {}, None),
        ("GET", "/%2E%2E", {}, None),
        ("HEAD", "/", {}, None),
        ("GET", "/auth/logout", {}, None),
        ("GET", "/github/webhook", {}, None),
        # the mux: cleaning, trailing-slash redirects, 405s, escapes
        ("GET", "/a/../api/me?x=1", {}, None),
        ("GET", "/x/../a%20b", {}, None),
        ("GET", "/api/tiers//pr/1", {}, None),
        ("GET", "/api/tiers/nonprod/logs/e1", {}, None),
        ("GET", "/api/tiers/nonprod/logs/e%20f?x=1", {}, None),
        ("HEAD", "/api/tiers/nonprod/logs/e1", {}, None),
        ("POST", "/api/tiers/nonprod/logs/e1", {}, b""),
        ("PUT", "/auth/logout", {}, b""),
        ("POST", "/x", {}, b""),
        ("DELETE", "/api/me", {}, None),
        ("POST", "/auth/login", {}, b""),
        ("GET", "/a%zz", {}, None),
        ("GET", "/api/%6De", S, None),
        ("GET", "/api/me/", S, None),
        ("GET", "/api/tiers/%2F/pr/1", S, None),
        ("GET", "/api/tiers/nonprod/pr/", S, None),
        # sessions
        ("GET", "/api/me", {}, None),
        ("GET", "/api/me", S, None),
        ("HEAD", "/api/me", S, None),
        ("GET", "/api/me", {"Cookie": 'tfsp_ui_session="' + sess + '"'}, None),
        ("GET", "/api/me", {"Cookie": "a=1; tfsp_ui_session=" + sess + "; b=2"}, None),
        ("GET", "/api/me", {"Cookie": "tfsp_ui_session=" + expired}, None),
        ("GET", "/api/me", {"Cookie": "tfsp_ui_session=" + sess[:-2] + "xx"}, None),
        ("GET", "/api/me", {"Cookie": "tfsp_ui_session="}, None),
        ("GET", "/api/me", {"Cookie": "tfsp_ui_session=" + approve_states["valid"]}, None),
        ("GET", "/api/tiers", S, None),
        ("GET", "/api/tiers", {}, None),
        # contract proxies
        ("GET", "/api/tiers/nonprod/executions", S, None),
        ("GET", "/api/tiers/nonprod/executions?pr=7&limit=5", S, None),
        ("GET", "/api/tiers/nonprod/executions?limit=5&pr=%2B07&x=y", S, None),
        ("GET", "/api/tiers/nonprod/executions?pr=1&pr=2", S, None),
        ("GET", "/api/tiers/nonprod/executions?pr=1&pr=2", {}, None),
        ("GET", "/api/tiers/nonprod/executions?pr=abc", S, None),
        ("GET", "/api/tiers/nonprod/executions?pr=", S, None),
        ("GET", "/api/tiers/nonprod/executions?limit=99999999999999999999", S, None),
        ("GET", "/api/tiers/nonprod/executions?pr=%E2%80%A8", S, None),
        ("GET", "/api/tiers/nonprod/executions?pr=1;2", S, None),
        ("GET", "/api/tiers/nonprod/approvals", S, None),
        ("GET", "/api/tiers/nonprod/inspect/pool", S, None),
        ("GET", "/api/tiers/nonprod/merge-queue", S, None),
        ("GET", "/api/tiers/nonprod/lifecycle?pr=42", S, None),
        ("GET", "/api/tiers/nonprod/lifecycle", S, None),
        ("GET", "/api/tiers/nonprod/lifecycle", {}, None),
        ("GET", "/api/tiers/nonprod/pr/7", S, None),
        ("GET", "/api/tiers/nonprod/pr/-7", S, None),
        ("GET", "/api/tiers/nonprod/pr/abc", S, None),
        ("GET", "/api/tiers/nonprod/pr/abc", {}, None),
        ("GET", "/api/tiers/nonprod/pr/%25ff", S, None),
        ("GET", "/api/tiers/nonprod/executions/e1", S, None),
        ("GET", "/api/tiers/nonprod/executions/a%20b", S, None),
        ("GET", "/api/tiers/nonprod/executions/a%252Fb", S, None),
        ("GET", "/api/tiers/nonprod/executions/a,b;c", S, None),
        ("GET", "/api/tiers/nonprod/executions/nope", S, None),
        ("GET", "/api/tiers/nonprod/executions/noct", S, None),
        ("GET", "/api/tiers/nonprod/executions/empty", S, None),
        ("GET", "/api/tiers/nonprod/executions/boom", S, None),
        ("GET", "/api/tiers/%25zz/approvals", S, None),
        ("GET", "/api/tiers/%25zz/approvals", {}, None),
        ("GET", "/api/tiers/%256Eonprod/approvals", S, None),
        ("GET", "/api/tiers/stage/executions", S, None),
        ("GET", "/api/tiers/prod/executions", S, None),
        ("GET", "/api/tiers/prod/pr/7", S, None),
        ("GET", "/api/tiers/prod/lifecycle?pr=42", S, None),
        # stream proxies
        ("GET", "/api/tiers/nonprod/executions/e1/events", S, None),
        ("GET", "/api/tiers/nonprod/executions/e%20f/events", S, None),
        ("GET", "/api/tiers/nonprod/executions/e1/events", {}, None),
        ("GET", "/api/tiers/nonprod/logs/e1/stacks/a?follow=1",
         dict(S, **{"Last-Event-ID": "17", "Accept": "text/event-stream"}), None),
        ("GET", "/api/tiers/nonprod/logs/e1/", S, None),
        ("GET", "/api/tiers/nonprod/logs/e%2F1/a/b%2Fc?x=%20", S, None),
        ("GET", "/api/tiers/nonprod/plan/e1/stacks/a", S, None),
        ("GET", "/api/tiers/nope/logs/e1/stacks/a", S, None),
        ("GET", "/api/tiers/prod/plan/e1/stacks/a", S, None),
        # login (no Google round trip: only the redirect and the pre-exchange checks)
        ("GET", "/auth/login", {}, None),
        ("HEAD", "/auth/login", {}, None),
        ("GET", "/auth/login?next=/pr/7", {}, None),
        ("GET", "/auth/login?next=/a%20b,c", {}, None),
        ("GET", "/auth/login?next=//evil.com", {}, None),
        ("GET", "/auth/login?next=/%5Cevil.com", {}, None),
        ("GET", "/auth/login?next=/caf%C3%A9", {}, None),
        ("GET", "/auth/callback", {}, None),
        ("GET", "/auth/callback?state=abc", {"Cookie": "tfsp_ui_state=xyz"}, None),
        ("GET", "/auth/callback?state=abc", {"Cookie": "tfsp_ui_state=abc"}, None),
        ("GET", "/auth/callback?state=", {"Cookie": "tfsp_ui_state="}, None),
        ("POST", "/auth/logout", {}, b""),
        ("POST", "/auth/logout", S, b""),
        # approvals (the start, and the callback guards that stop before Google)
        ("GET", "/auth/approve?tier=nonprod&decision=approve&reason=lgtm&grant=" + g, {}, None),
        ("GET", "/auth/approve?tier=nonprod&decision=approve&reason=lgtm&grant=" + g, SO, None),
        ("GET", "/auth/approve?tier=nonprod&decision=deny&grant=" + g, SO, None),
        ("GET", "/auth/approve?tier=nope&decision=approve&grant=" + g, SO, None),
        ("GET", "/auth/approve?tier=nonprod&decision=maybe&grant=" + g, SO, None),
        ("GET", "/auth/approve?tier=nonprod&decision=approve&grant=not-a-grant", SO, None),
        ("GET", "/auth/approve?tier=nonprod&decision=approve&grant=caf%C3%A9%01%22", SO, None),
        ("GET", "/auth/approve?tier=nonprod&decision=approve&grant=%ff", SO, None),
        ("GET", "/auth/approve?tier=nonprod&decision=approve&grant=" + g + "&reason=" + "x" * 513, SO, None),
        ("GET", "/auth/approve/callback?code=c&state=bogus", S, None),
        ("GET", "/auth/approve/callback?code=c&state=bogus", {}, None),
        ("GET", "/auth/approve/callback?code=c&state=" + q(approve_states["expired"]), S, None),
        ("GET", "/auth/approve/callback?code=c&state=" + q(approve_states["valid"]), O, None),
        ("GET", "/auth/approve/callback?error=access_denied&state=" + q(approve_states["valid"]), S, None),
        ("GET", "/auth/approve/callback?error=%3Cb%3E%26%22&state=" + q(approve_states["valid"]), S, None),
    ]
    body = b'{"action":"rerequested"}'
    out += [
        ("POST", "/github/webhook", {"X-GitHub-Event": "check_run", "X-GitHub-Delivery": "d1",
                                     "Content-Type": "application/json",
                                     "X-Hub-Signature-256": sign(body)}, body),
        ("POST", "/github/webhook", {"X-Hub-Signature-256": "sha256=deadbeef"}, body),
        ("POST", "/github/webhook", {}, body),
        ("POST", "/github/webhook", {"X-Hub-Signature-256": sign(b"x" * (2 << 20) + b"x")},
         b"x" * (2 << 20) + b"x"),
        ("POST", "/github/webhook", {"X-Hub-Signature-256": sign(b"x" * (2 << 20))},
         b"x" * (2 << 20)),
    ]
    return out


# Deliberate differences, checked separately.
KNOWN = {
    # Also normalized in every case: `Accept: */*` on requests to the tier (see normalize).
    ("GET", "/healthz"): "Rust reports {status, version, git_sha}; Go answers an empty 200",
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--go-bin", required=True)
    ap.add_argument("--rust-bin", required=True,
                    help="the Rust ui binary, or a wrapper such as in-docker.sh")
    ap.add_argument("--report", help="write the case-by-case results as JSON here")
    a = ap.parse_args()

    tier_port = free_port()
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", tier_port), Tier)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    dead = "http://127.0.0.1:%d" % free_port()

    work = tempfile.mkdtemp(prefix="ui-replay-")
    cfg = os.path.join(work, "ui.hcl")
    with open(cfg, "w") as f:
        f.write(CONFIG % {"tier": "http://127.0.0.1:%d" % tier_port, "dead": dead})
    home = os.path.join(work, "home")
    os.mkdir(home)
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(("GOOGLE_", "CLOUDSDK_", "GCE_"))}
    env.update(HOME=home, CLOUDSDK_CONFIG=home,
               TFSTACKPLAN_UI_SESSION_SECRET=SECRET,
               TFSTACKPLAN_UI_OAUTH_SECRET="shh",
               TFSTACKPLAN_UI_GITHUB_WEBHOOK_SECRET=GH_SECRET)

    go_port, rust_port = free_port(), free_port()
    go_log = open(os.path.join(work, "go.log"), "w+")
    rust_log = open(os.path.join(work, "rust.log"), "w+")
    go = start([a.go_bin, "ui", "-config", cfg], go_port, env, go_log)
    rust = start([a.rust_bin, "-config", cfg], rust_port, env, rust_log)
    try:
        sess = goseal("session", "ivan@example.com", "2099-01-01T00:00:00Z")
        other = goseal("session", "other@example.com", "2099-01-01T00:00:00Z")
        expired = goseal("session", "ivan@example.com", "2001-01-01T00:00:00Z")
        st = lambda exp: goseal("json", json.dumps({
            "email": "ivan@example.com", "tier": "nonprod", "grant": GRANT, "approve": True,
            "reason": "lgtm", "expires": exp}))
        states = {"valid": st("2099-01-01T00:00:00Z"), "expired": st("2001-01-01T00:00:00Z")}

        results, mismatches = [], 0
        for method, target, headers, body in cases(sess, other, expired, states):
            g = normalize(send(go_port, method, target, headers, body), go_port)
            r = normalize(send(rust_port, method, target, headers, body), rust_port)
            same = g == r
            mismatches += not same
            results.append({"case": "%s %s" % (method, target[:120]), "match": same,
                            "go": g if not same else None, "rust": r if not same else None})
            print("%-5s %s %s" % ("ok" if same else "DIFF", method, target[:100]))
            if not same:
                print("  go:   ", json.dumps(g)[:1500])
                print("  rust: ", json.dumps(r)[:1500])
        for (method, target), why in KNOWN.items():
            g = send(go_port, method, target)
            r = send(rust_port, method, target)
            print("known %s %s: go=%d %r rust=%d %r (%s)" % (
                method, target, g["status"], g["body"], r["status"], r["body"], why))
        print("\n%d cases, %d match, %d differ" % (len(results), len(results) - mismatches,
                                                   mismatches))
        if a.report:
            with open(a.report, "w") as f:
                json.dump(results, f, indent=1)
        return 1 if mismatches else 0
    finally:
        for p in (go, rust):
            p.terminate()
        for p in (go, rust):
            p.wait(timeout=15)
        go_log.seek(0)
        rust_log.seek(0)
        print("\n--- go startup ---\n" + go_log.read()[-2000:])
        print("--- rust startup ---\n" + rust_log.read()[-2000:])


if __name__ == "__main__":
    sys.exit(main())
