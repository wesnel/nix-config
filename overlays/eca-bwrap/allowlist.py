"""Allowlist and request log for the bubblewrap backend.

Emits the same JSONL shape as the Gondolin backend, so both tiers produce
one comparable audit trail. A blocked request is logged without a matching
response, which is how the Gondolin side records a refusal too.
"""

import json
import os
import time

from mitmproxy import http

ALLOWED = [h for h in os.environ.get("ECA_SANDBOX_ALLOW_HOSTS", "").split(",") if h]
LOG = os.environ.get("ECA_SANDBOX_LOG") or None


def _record(entry):
    if not LOG:
        return

    entry["at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    try:
        with open(LOG, "a") as handle:
            handle.write(json.dumps(entry) + "\n")
    except OSError:
        # Observability is best-effort: a failed write must not take the
        # session down.
        pass


def _allowed(host):
    return any(host == entry or host.endswith("." + entry) for entry in ALLOWED)


def request(flow: http.HTTPFlow) -> None:
    host = flow.request.pretty_host

    _record(
        {
            "dir": "request",
            "method": flow.request.method,
            "url": flow.request.pretty_url,
        }
    )

    if ALLOWED and not _allowed(host):
        flow.metadata["eca_blocked"] = True
        flow.response = http.Response.make(
            403, b"blocked by eca-sandbox allowlist\n"
        )


def response(flow: http.HTTPFlow) -> None:
    if flow.metadata.get("eca_blocked"):
        return

    _record(
        {
            "dir": "response",
            "status": flow.response.status_code,
            "url": flow.request.pretty_url,
        }
    )
