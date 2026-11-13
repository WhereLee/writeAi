#!/usr/bin/env python3
import json
import os
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

WEBHOOK = os.environ.get("ALERT_WEBHOOK_URL", "").strip()
KIND = os.environ.get("ALERT_WEBHOOK_TYPE", "feishu").strip().lower()
LISTEN = os.environ.get("ALERT_RELAY_LISTEN", "127.0.0.1:8061")
HOST, PORT = LISTEN.rsplit(":", 1)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b"{}"
        try:
            data = json.loads(raw or b"{}")
        except Exception:
            data = {}
        lines = []
        for alert in data.get("alerts", []):
            labels = alert.get("labels", {})
            annotations = alert.get("annotations", {})
            lines.append("[{}] {}: {}".format(
                alert.get("status", "unknown"),
                labels.get("alertname", "alert"),
                annotations.get("summary", ""),
            ))
        text = "[LLM] " + ("\n".join(lines) or "alertmanager test")
        if not WEBHOOK:
            self.send_response(503)
            self.end_headers()
            self.wfile.write(b"webhook not configured")
            return
        if KIND == "dingtalk":
            payload = {"msgtype": "text", "text": {"content": text}}
        else:
            payload = {"msg_type": "text", "content": {"text": text}}
        request = urllib.request.Request(
            WEBHOOK,
            data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json"},
        )
        try:
            urllib.request.urlopen(request, timeout=10).read()
            self.send_response(200)
        except Exception as exc:
            self.send_response(502)
            self.end_headers()
            self.wfile.write(str(exc).encode())
            return
        self.end_headers()

    def log_message(self, *args):
        pass


HTTPServer((HOST, int(PORT)), Handler).serve_forever()
