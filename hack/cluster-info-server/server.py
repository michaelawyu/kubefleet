"""
A simple HTTP server that returns an HTML page showing the cluster name.

The cluster name is read from a kubeconfig provided via the environment:
  - KUBECONFIG_DATA: base64-encoded kubeconfig YAML (preferred for containers)
  - KUBECONFIG:      path to a kubeconfig file (fallback)

Environment variables:
  PORT (optional): port to listen on (default: 8080)
"""

import base64
import http.server
import os
import socketserver
import sys
import yaml


def get_cluster_name() -> str:
    """Read the cluster name from the kubeconfig in the environment."""
    kubeconfig_data = os.environ.get("KUBECONFIG_DATA")
    if kubeconfig_data:
        try:
            raw = base64.b64decode(kubeconfig_data).decode("utf-8")
        except Exception as exc:
            sys.exit(f"Failed to base64-decode KUBECONFIG_DATA: {exc}")
    else:
        kubeconfig_path = os.environ.get("KUBECONFIG", os.path.expanduser("~/.kube/config"))
        try:
            with open(kubeconfig_path, "r", encoding="utf-8") as f:
                raw = f.read()
        except OSError as exc:
            sys.exit(f"Failed to read kubeconfig at {kubeconfig_path!r}: {exc}")

    try:
        config = yaml.safe_load(raw)
    except yaml.YAMLError as exc:
        sys.exit(f"Failed to parse kubeconfig YAML: {exc}")

    current_context_name = config.get("current-context")
    if not current_context_name:
        return "<unknown>"

    contexts = {c["name"]: c for c in (config.get("contexts") or [])}
    context = contexts.get(current_context_name, {})
    cluster_name = (context.get("context") or {}).get("cluster", "<unknown>")
    return cluster_name


def make_html(cluster_name: str) -> str:
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Cluster Info</title>
  <style>
    body {{ font-family: Arial, sans-serif; display: flex; justify-content: center;
           align-items: center; height: 100vh; margin: 0; background: #f0f4f8; }}
    .card {{ background: white; border-radius: 8px; padding: 2rem 3rem;
             box-shadow: 0 2px 8px rgba(0,0,0,0.15); text-align: center; }}
    h1 {{ color: #333; margin-bottom: 0.5rem; }}
    p  {{ color: #666; font-size: 1.2rem; }}
    .cluster {{ color: #0078d4; font-weight: bold; font-size: 1.4rem; }}
  </style>
</head>
<body>
  <div class="card">
    <h1>Cluster Info</h1>
    <p>Current cluster:</p>
    <p class="cluster">{cluster_name}</p>
  </div>
</body>
</html>
"""


CLUSTER_NAME = get_cluster_name()
HTML_BYTES = make_html(CLUSTER_NAME).encode("utf-8")


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(HTML_BYTES)))
        self.end_headers()
        self.wfile.write(HTML_BYTES)

    def log_message(self, fmt, *args):  # noqa: N802
        print(f"{self.address_string()} - {fmt % args}", flush=True)


if __name__ == "__main__":
    port = int(os.environ.get("PORT", 8080))
    with socketserver.TCPServer(("", port), Handler) as httpd:
        httpd.allow_reuse_address = True
        print(f"Serving on port {port} — cluster: {CLUSTER_NAME}", flush=True)
        httpd.serve_forever()
