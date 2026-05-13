"""
A simple HTTP server that returns an HTML page showing the cluster name.

The cluster name is read from a mounted ConfigMap file. Kubernetes projects each
ConfigMap data entry as a file inside the mount directory, so a ConfigMap with:

    data:
      cluster-name: my-cluster

mounted at /etc/config will produce the file /etc/config/cluster-name.

Configuration:
  CONFIG_MAP_DIR  (optional): directory where the ConfigMap is mounted
                              (default: /etc/config)
  CONFIG_MAP_KEY  (optional): filename (data key) that holds the cluster name
                              (default: cluster-name)
  PORT            (optional): port to listen on (default: 8080)
"""

import http.server
import os
import socketserver
import sys


def get_cluster_name() -> str:
    """Read the cluster name from the mounted ConfigMap file."""
    config_dir = os.environ.get("CONFIG_MAP_DIR", "/etc/config")
    config_key = os.environ.get("CONFIG_MAP_KEY", "cluster-name")
    config_path = os.path.join(config_dir, config_key)

    try:
        with open(config_path, "r", encoding="utf-8") as f:
            return f.read().strip() or "<unknown>"
    except OSError as exc:
        sys.exit(f"Failed to read cluster name from {config_path!r}: {exc}")


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
