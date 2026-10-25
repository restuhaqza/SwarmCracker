"""Behavioural tests for the SwarmCracker Python client.

Everything runs against a stdlib ThreadingHTTPServer on 127.0.0.1, so the suite
needs no network and no third-party package.
"""

from __future__ import annotations

import base64
import email.utils
import json
import socket
import tempfile
import threading
import time
import unittest
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from swarmcracker import ApiError, Client
from swarmcracker.client import (
    API_VERSION_HEADER,
    NDJSON_MEDIA_TYPE,
    OCTET_STREAM_MEDIA_TYPE,
    SSE_MEDIA_TYPE,
    V1_MEDIA_TYPE,
)

TOKEN = "test-token"
REQUEST_ID = "req_0123456789ab"

SERVICE = {
    "schemaVersion": 1,
    "kind": "Service",
    "id": "s1",
    "name": "web",
    "revision": "3",
    "labels": {"app": "web"},
    "spec": {"mode": "replicated", "replicas": 1, "task": {"image": "nginx"}},
    "status": {
        "desiredTasks": 1,
        "runningTasks": 1,
        "tasksInTransition": 0,
        "converged": True,
    },
}

TASK = {
    "schemaVersion": 1,
    "kind": "Task",
    "id": "t1",
    "revision": "7",
    "serviceId": "s1",
    "state": "running",
    "desiredState": "running",
}

SNAPSHOT = {
    "id": "snap-abc",
    "taskId": "t1",
    "createdAt": "2026-10-25T10:00:00Z",
    "sizeBytes": 1024,
    "memoryMB": 256,
    "vcpuCount": 2,
    "rootfsPath": "/var/lib/firecracker/rootfs/t1.ext4",
}

SNAPSHOT_DOCUMENT = dict(SNAPSHOT, schemaVersion=1, kind="Snapshot")

#: The bytes the file routes serve: one NUL and one non-UTF-8 byte prove the
#: client never decodes the body as text.
FILE_BYTES = b"hello file\x00\xff\n"


#: Exec streams that violate the frame contract, keyed by route.
EXEC_FAULTS = {
    "/v1/tasks/t4/exec": [{"data": base64.b64encode(b"x").decode()}],
    "/v1/tasks/t5/exec": [{"type": "bogus"}],
    "/v1/tasks/t6/exec": [{"type": "stdout", "data": 42}],
    "/v1/tasks/t7/exec": [{"type": "stdout", "data": "%%% not base64 %%%"}],
}


def error_document(code: str, message: str, *, retryable: bool = False, details=None) -> dict:
    return {
        "schemaVersion": 1,
        "kind": "Error",
        "error": {
            "code": code,
            "message": message,
            "requestId": REQUEST_ID,
            "retryable": retryable,
            "details": details,
        },
    }


def event(cursor: str, kind: str, resource_id: str) -> dict:
    return {
        "schemaVersion": 1,
        "kind": "Event",
        "cursor": cursor,
        "at": "2026-10-25T10:00:00Z",
        "type": kind,
        "resource": {"kind": "Service", "id": resource_id},
    }


class Handler(BaseHTTPRequestHandler):
    """A minimal stand-in for the manager, faithful to the v1 wire contract."""

    protocol_version = "HTTP/1.1"

    def log_message(self, *args):  # keep the test output clean
        pass

    # -- HTTP verbs ---------------------------------------------------------

    def do_GET(self):
        self._dispatch("GET")

    def do_POST(self):
        self._dispatch("POST")

    def do_PATCH(self):
        self._dispatch("PATCH")

    def do_PUT(self):
        self._dispatch("PUT")

    def do_DELETE(self):
        self._dispatch("DELETE")

    # -- plumbing -----------------------------------------------------------

    def _dispatch(self, method):
        parsed = urllib.parse.urlsplit(self.path)
        record = {
            "method": method,
            "path": parsed.path,
            "raw_query": parsed.query,
            "query": urllib.parse.parse_qs(parsed.query, keep_blank_values=True),
            "headers": {key.lower(): value for key, value in self.headers.items()},
            "body": self._read_body(),
        }
        self.server.requests.append(record)

        if record["headers"].get("authorization") != f"Bearer {TOKEN}":
            self._json(401, error_document("unauthenticated", "missing bearer token"))
            return
        if not self._route(method, parsed.path, record):
            self._json(404, error_document("not_found", "no such route"))

    def _read_body(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        if not raw:
            return None
        content_type = self.headers.get("Content-Type") or ""
        if content_type == V1_MEDIA_TYPE or content_type.endswith("+json"):
            return json.loads(raw)
        return raw

    def _count(self, key):
        """Increment and return the per-route attempt counter of this server."""
        self.server.counters[key] = self.server.counters.get(key, 0) + 1
        return self.server.counters[key]

    def _route(self, method, path, record):
        if method == "GET" and path == "/v1/version":
            self._json(
                200,
                {
                    "schemaVersion": 1,
                    "kind": "VersionInfo",
                    "apiVersions": ["1"],
                    "serverVersion": "v0.11.0",
                    "grpcProtocolVersion": "1",
                    "capabilities": ["broker", "snapshots"],
                },
            )
            return True
        if method == "GET" and path == "/v1/services":
            self._json(200, {"schemaVersion": 1, "kind": "ServiceList", "items": [SERVICE], "next": None})
            return True
        if method == "POST" and path == "/v1/services":
            if record["headers"].get("content-type") != V1_MEDIA_TYPE:
                self._json(415, error_document("invalid", "unsupported media type"))
                return True
            name = (record["body"] or {}).get("name")
            if name == "flaky-no-key":
                self._json(503, error_document("unavailable", "try later", retryable=True))
                return True
            if name == "flaky" and record["headers"].get("idempotency-key"):
                if self._count("services:flaky") <= 2:
                    self._json(503, error_document("unavailable", "try later", retryable=True))
                    return True
            document = dict(SERVICE, id="s2", name=name)
            self._json(201, document)
            return True
        if path.startswith("/v1/services/"):
            return self._service_route(method, path, record)
        if method == "GET" and path == "/v1/tasks":
            self._json(200, {"schemaVersion": 1, "kind": "TaskList", "items": [TASK], "next": "c2"})
            return True
        if method == "GET" and path == "/v1/tasks/t1":
            document = dict(TASK)
            if record["query"].get("health") == ["true"]:
                document["health"] = {"status": "healthy"}
            self._json(200, document)
            return True
        if method == "POST" and path == "/v1/tasks/t1/exec":
            if record["headers"].get("content-type") != V1_MEDIA_TYPE:
                self._json(415, error_document("invalid", "unsupported media type"))
                return True
            self._exec_stream(
                [
                    {"type": "stdout", "data": base64.b64encode(b"hello\n").decode()},
                    {"type": "stderr", "data": base64.b64encode(b"oops\n").decode()},
                    {"type": "exit", "exitCode": 3},
                ]
            )
            return True
        if method == "POST" and path == "/v1/tasks/t2/exec":
            # A stream that ends after stdout, without a terminal exit frame.
            self._exec_stream(
                [{"type": "stdout", "data": base64.b64encode(b"partial\n").decode()}]
            )
            return True
        if method == "POST" and path == "/v1/tasks/t3/exec":
            self._exec_stream([{"type": "error", "code": "timeout"}])
            return True
        if method == "POST" and path in EXEC_FAULTS:
            self._exec_stream(EXEC_FAULTS[path])
            return True
        if method == "GET" and path == "/v1/tasks/t1/logs":
            self._exec_stream(
                [
                    {
                        "type": "log",
                        "stream": "stdout",
                        "time": "2026-10-25T10:00:00.5Z",
                        "data": base64.b64encode(b"line one\n").decode(),
                    },
                    {
                        "type": "log",
                        "stream": "stdout",
                        "time": "2026-10-25T10:00:01Z",
                        "data": base64.b64encode(b"line two\n").decode(),
                    },
                    {"type": "eof"},
                ],
                content_type=NDJSON_MEDIA_TYPE,
            )
            return True
        if method == "POST" and path == "/v1/tasks/t10/exec":
            # One frame, then the connection dies mid-body: the chunked stream
            # never gets its terminating chunk.
            self._truncated_stream(
                NDJSON_MEDIA_TYPE,
                json.dumps(
                    {"type": "stdout", "data": base64.b64encode(b"partial\n").decode()}
                ).encode("utf-8")
                + b"\n",
            )
            return True
        if method == "GET" and path == "/v1/tasks/t10/logs":
            self._truncated_stream(
                NDJSON_MEDIA_TYPE,
                json.dumps(
                    {
                        "type": "log",
                        "stream": "stdout",
                        "time": "2026-10-25T10:00:00Z",
                        "data": base64.b64encode(b"partial\n").decode(),
                    }
                ).encode("utf-8")
                + b"\n",
            )
            return True
        if path.startswith("/v1/tasks/t1/files"):
            return self._file_route(method, path, record)
        if path == "/v1/snapshots" and method == "GET":
            task = record["query"].get("task", [None])[0]
            if task is None:
                self._json(
                    400,
                    error_document(
                        "invalid",
                        "task is required",
                        details={"parameter": "task"},
                    ),
                )
                return True
            self._json(
                200,
                {
                    "schemaVersion": 1,
                    "kind": "SnapshotList",
                    "items": [SNAPSHOT],
                    "next": None,
                },
            )
            return True
        if path == "/v1/snapshots/snap-abc/restore" and method == "POST":
            if record["query"].get("task") != ["t1"]:
                self._json(400, error_document("invalid", "task is required"))
                return True
            self._json(200, SNAPSHOT_DOCUMENT)
            return True
        if path == "/v1/snapshots/snap-abc" and method == "DELETE":
            if record["query"].get("task") != ["t1"]:
                self._json(400, error_document("invalid", "task is required"))
                return True
            self._send(204, b"", V1_MEDIA_TYPE)
            return True
        if path == "/v1/tasks/t1/restored" and method == "DELETE":
            self._send(204, b"", V1_MEDIA_TYPE)
            return True
        if path == "/v1/tasks/t1/snapshots" and method == "POST":
            body = record["body"] or {}
            document = dict(SNAPSHOT_DOCUMENT)
            if "vcpus" in body:
                document["vcpuCount"] = body["vcpus"]
            if "memoryMB" in body:
                document["memoryMB"] = body["memoryMB"]
            self._json(201, document)
            return True
        if path == "/v1/retry/twice" and method == "GET":
            if self._count(path) <= 2:
                self._json(503, error_document("unavailable", "try later", retryable=True))
                return True
            self._json(200, {"schemaVersion": 1, "kind": "VersionInfo", "serverVersion": "v0.11.0"})
            return True
        if path == "/v1/retry/always" and method == "GET":
            self._count(path)
            self._json(503, error_document("unavailable", "try later", retryable=True))
            return True
        if path == "/v1/retry/after-seconds" and method == "GET":
            if self._count(path) == 1:
                self._json(
                    429,
                    error_document("rate_limited", "slow down", retryable=True),
                    headers={"Retry-After": "7"},
                )
                return True
            self._json(200, {"schemaVersion": 1, "kind": "VersionInfo", "serverVersion": "v0.11.0"})
            return True
        if path == "/v1/retry/after-date" and method == "GET":
            if self._count(path) == 1:
                self._json(
                    503,
                    error_document("unavailable", "try later", retryable=True),
                    headers={
                        "Retry-After": email.utils.formatdate(time.time() + 3, usegmt=True)
                    },
                )
                return True
            self._json(200, {"schemaVersion": 1, "kind": "VersionInfo", "serverVersion": "v0.11.0"})
            return True
        if path == "/v1/retry/capped" and method == "GET":
            if self._count(path) == 1:
                self._json(
                    429,
                    error_document("rate_limited", "slow down", retryable=True),
                    headers={"Retry-After": "600"},
                )
                return True
            self._json(200, {"schemaVersion": 1, "kind": "VersionInfo", "serverVersion": "v0.11.0"})
            return True
        if path == "/v1/retry/bad-request" and method == "GET":
            self._count(path)
            self._json(400, error_document("invalid", "no"))
            return True
        if method == "GET" and path == "/v1/events":
            # The test server reports a typed failure when the subscription asks
            # for the reserved, test-only type "error", and a transport failure
            # when it asks for "drop". "flaky" fails once with a retryable 503:
            # a stream must not replay it even when retries are enabled.
            if record["query"].get("types") == ["error"]:
                self._sse_error_stream()
            elif record["query"].get("types") == ["drop"]:
                self._truncated_stream(
                    SSE_MEDIA_TYPE,
                    b"event: service.created\ndata: "
                    + json.dumps(event("7", "service.created", "s1")).encode("utf-8")
                    + b"\n\n",
                )
            elif record["query"].get("types") == ["flaky"] and self._count("events:flaky") == 1:
                self._json(503, error_document("unavailable", "try later", retryable=True))
            else:
                self._sse_stream()
            return True
        if method == "GET" and path == "/v1/redirect":
            self._redirect(self.server.redirect_to)
            return True
        if method == "GET" and path == "/v1/forbidden":
            self._json(
                403,
                error_document(
                    "forbidden",
                    "the token lacks services:read",
                    details={"requiredScope": "services:read"},
                ),
            )
            return True
        if method == "GET" and path == "/v1/undecodable":
            self._send(500, b"<html>boom</html>", "text/html")
            return True
        return False

    def _file_route(self, method, path, record):
        guest_path = record["query"].get("path", [""])[0]
        if method == "GET":
            if guest_path == "/missing":
                self._json(404, error_document("not_found", "no such file"))
                return True
            self._send(200, FILE_BYTES, OCTET_STREAM_MEDIA_TYPE)
            return True
        if method == "PUT":
            if record["headers"].get("content-type") != OCTET_STREAM_MEDIA_TYPE:
                self._json(415, error_document("invalid", "unsupported media type"))
                return True
            if guest_path == "/missing":
                self._json(404, error_document("not_found", "no such directory"))
                return True
            self._send(204, b"", V1_MEDIA_TYPE)
            return True
        return False

    def _service_route(self, method, path, record):
        reference = urllib.parse.unquote(path[len("/v1/services/"):])
        if method == "GET":
            self._json(200, dict(SERVICE, id=reference))
            return True
        if method == "PATCH":
            if record["headers"].get("content-type") != "application/merge-patch+json":
                self._json(415, error_document("invalid", "unsupported media type"))
                return True
            if "if-match" not in record["headers"]:
                self._json(428, error_document("invalid", "If-Match is required"))
                return True
            document = dict(SERVICE, id=reference)
            document["labels"] = record["body"].get("labels", document["labels"])
            self._json(200, document)
            return True
        if method == "DELETE":
            # If-Match is optional on DELETE: the API rejects only a stale value.
            self._send(204, b"", V1_MEDIA_TYPE)
            return True
        return False

    # -- responses ----------------------------------------------------------

    def _json(self, status, document, headers=None):
        self._send(status, json.dumps(document).encode("utf-8"), V1_MEDIA_TYPE, headers)

    def _send(self, status, body, content_type, headers=None):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header(API_VERSION_HEADER, "1")
        self.send_header("X-Request-Id", REQUEST_ID)
        for name, value in (headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        if body:
            self.wfile.write(body)

    def _exec_stream(self, frames, content_type=NDJSON_MEDIA_TYPE):
        self._start_stream(content_type)
        try:
            for frame in frames:
                self._chunk(json.dumps(frame).encode("utf-8") + b"\n")
            self._end_stream()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _sse_stream(self):
        self._start_stream(SSE_MEDIA_TYPE)
        try:
            self._chunk(b'event: ready\ndata: {"type":"ready"}\n\n')
            self._chunk(b": ping\n\n")
            self._chunk(
                b"id: 7\nevent: service.created\ndata: "
                + json.dumps(event("7", "service.created", "s1")).encode("utf-8")
                + b"\n\n"
            )
            self._chunk(
                b"id: 8\nevent: task.updated\ndata: "
                + json.dumps(event("8", "task.updated", "t1")).encode("utf-8")
                + b"\n\n"
            )
            self._chunk(b"event: end\ndata: {}\n\n")
            self._end_stream()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _sse_error_stream(self):
        self._start_stream(SSE_MEDIA_TYPE)
        try:
            self._chunk(b"event: ready\ndata: {}\n\n")
            self._chunk(
                b"event: error\ndata: "
                + json.dumps(error_document("unavailable", "the broker is down", retryable=True)).encode(
                    "utf-8"
                )
                + b"\n\n"
            )
            self._end_stream()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _redirect(self, location):
        self.send_response(302)
        self.send_header("Location", location)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _start_stream(self, content_type):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Transfer-Encoding", "chunked")
        self.send_header(API_VERSION_HEADER, "1")
        self.end_headers()

    def _chunk(self, data):
        self.wfile.write(b"%x\r\n" % len(data) + data + b"\r\n")
        self.wfile.flush()

    def _end_stream(self):
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()

    def _truncated_stream(self, content_type, chunk):
        """Announce more body than is sent, then close.

        The client's chunked reader raises http.client.IncompleteRead (or
        ConnectionResetError, depending on timing): both are transport
        failures and must surface as ApiError("unreachable").
        """
        self._start_stream(content_type)
        self.wfile.write(b"%x\r\n" % (len(chunk) + 32) + chunk)
        self.wfile.flush()
        self.close_connection = True


class SinkHandler(BaseHTTPRequestHandler):
    """Records any request that reaches it: a redirect must not."""

    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        self.server.requests.append({key.lower(): value for key, value in self.headers.items()})
        body = b'{"ok": true}'
        self.send_response(200)
        self.send_header("Content-Type", V1_MEDIA_TYPE)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class ClientTest(unittest.TestCase):
    """One throwaway server per test, so every request log starts empty."""

    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.requests = []
        self.server.counters = {}
        self.server.redirect_to = ""
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            kwargs={"poll_interval": 0.01},
            daemon=True,
        )
        self.thread.start()
        self.host = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.client = Client(self.host, token=TOKEN)

    def tearDown(self):
        self.client.close()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    # -- helpers ------------------------------------------------------------

    def last_request(self, method="GET", path=None):
        matches = [
            request
            for request in self.server.requests
            if request["method"] == method and (path is None or request["path"] == path)
        ]
        self.assertTrue(matches, f"no {method} request recorded: {self.server.requests}")
        return matches[-1]

    # -- version and envelope ----------------------------------------------

    def test_version_sends_the_envelope_headers(self):
        info = self.client.version()
        self.assertEqual(info["serverVersion"], "v0.11.0")
        self.assertEqual(info["apiVersions"], ["1"])

        request = self.last_request("GET", "/v1/version")
        self.assertEqual(request["headers"]["authorization"], f"Bearer {TOKEN}")
        self.assertEqual(request["headers"]["accept"], V1_MEDIA_TYPE)
        self.assertEqual(request["headers"][API_VERSION_HEADER.lower()], "1")
        self.assertNotIn("content-type", request["headers"])

    def test_close_is_idempotent_and_client_is_a_context_manager(self):
        with Client(self.host, token=TOKEN) as client:
            self.assertEqual(client.version()["kind"], "VersionInfo")
            client.close()
        self.client.close()

    # -- services -----------------------------------------------------------

    def test_services_list_encodes_label_limit_and_cursor(self):
        page = self.client.services.list(label="app=web", limit=5, cursor="abc/def")
        self.assertEqual(page["kind"], "ServiceList")
        self.assertEqual(page["items"][0]["id"], "s1")

        request = self.last_request("GET", "/v1/services")
        self.assertEqual(request["query"], {"label": ["app=web"], "limit": ["5"], "cursor": ["abc/def"]})

    def test_services_list_repeats_a_label_sequence(self):
        self.client.services.list(label=["app=web", "tier=front"])
        request = self.last_request("GET", "/v1/services")
        self.assertEqual(request["query"]["label"], ["app=web", "tier=front"])

    def test_services_get_percent_encodes_the_reference(self):
        document = self.client.services.get("web/one")
        self.assertEqual(document["id"], "web/one")
        request = self.last_request("GET")
        self.assertEqual(request["path"], "/v1/services/web%2Fone")

    def test_services_create_sends_media_type_and_idempotency_key(self):
        body = {
            "name": "api",
            "spec": {"mode": "replicated", "replicas": 1, "task": {"image": "api:1"}},
        }
        document = self.client.services.create(body, idempotency_key="create-1")
        self.assertEqual(document["name"], "api")

        request = self.last_request("POST", "/v1/services")
        self.assertEqual(request["headers"]["content-type"], V1_MEDIA_TYPE)
        self.assertEqual(request["headers"]["idempotency-key"], "create-1")
        self.assertEqual(request["body"], body)

    def test_services_patch_sends_a_merge_patch_if_match_and_wait(self):
        document = self.client.services.patch(
            "s1", {"labels": {"app": None}}, if_match="3", wait="converged"
        )
        self.assertEqual(document["labels"], {"app": None})

        request = self.last_request("PATCH", "/v1/services/s1")
        self.assertEqual(request["headers"]["content-type"], "application/merge-patch+json")
        self.assertEqual(request["headers"]["if-match"], "3")
        self.assertEqual(request["query"], {"wait": ["converged"]})

    def test_services_delete_returns_none_and_sends_if_match(self):
        self.assertIsNone(self.client.services.delete("s1", if_match="3"))
        request = self.last_request("DELETE", "/v1/services/s1")
        self.assertEqual(request["headers"]["if-match"], "3")

    def test_services_delete_omits_the_header_when_if_match_is_absent(self):
        self.assertIsNone(self.client.services.delete("s1"))
        request = self.last_request("DELETE", "/v1/services/s1")
        self.assertNotIn("if-match", request["headers"])

    # -- tasks --------------------------------------------------------------

    def test_tasks_list_sends_health_true_and_pagination(self):
        page = self.client.tasks.list(limit=2, cursor="c1", health=True)
        self.assertEqual(page["kind"], "TaskList")
        self.assertEqual(page["next"], "c2")

        request = self.last_request("GET", "/v1/tasks")
        self.assertEqual(request["query"], {"limit": ["2"], "cursor": ["c1"], "health": ["true"]})

    def test_tasks_list_omits_absent_parameters(self):
        self.client.tasks.list()
        request = self.last_request("GET", "/v1/tasks")
        self.assertEqual(request["raw_query"], "")

    def test_tasks_get_sends_health_false(self):
        self.client.tasks.get("t1", health=False)
        request = self.last_request("GET", "/v1/tasks/t1")
        self.assertEqual(request["query"], {"health": ["false"]})

    # -- files --------------------------------------------------------------

    def test_files_get_returns_raw_bytes_and_sends_the_path(self):
        self.assertEqual(self.client.tasks.files_get("t1", "/etc/hosts"), FILE_BYTES)

        request = self.last_request("GET", "/v1/tasks/t1/files")
        self.assertEqual(request["query"], {"path": ["/etc/hosts"]})
        self.assertEqual(request["headers"]["accept"], OCTET_STREAM_MEDIA_TYPE)

    def test_files_get_maps_a_typed_failure(self):
        with self.assertRaises(ApiError) as caught:
            self.client.tasks.files_get("t1", "/missing")
        self.assertEqual(caught.exception.code, "not_found")
        self.assertEqual(caught.exception.status, 404)
        self.assertEqual(caught.exception.request_id, REQUEST_ID)
        self.assertNotIn(TOKEN, str(caught.exception))

    def test_files_put_sends_raw_bytes_content_type_and_mode(self):
        self.assertIsNone(
            self.client.tasks.files_put("t1", "/tmp/payload", FILE_BYTES, mode="0600")
        )

        request = self.last_request("PUT", "/v1/tasks/t1/files")
        self.assertEqual(request["headers"]["content-type"], OCTET_STREAM_MEDIA_TYPE)
        self.assertEqual(request["query"], {"path": ["/tmp/payload"], "mode": ["0600"]})
        self.assertEqual(request["body"], FILE_BYTES)

    def test_files_put_omits_mode_and_accepts_an_empty_file(self):
        self.assertIsNone(self.client.tasks.files_put("t1", "/tmp/empty", b""))
        request = self.last_request("PUT", "/v1/tasks/t1/files")
        self.assertEqual(request["query"], {"path": ["/tmp/empty"]})
        self.assertIsNone(request["body"])
        self.assertEqual(request["headers"]["content-type"], OCTET_STREAM_MEDIA_TYPE)

    def test_files_put_rejects_a_non_bytes_body(self):
        with self.assertRaises(TypeError):
            self.client.tasks.files_put("t1", "/tmp/text", "not bytes")

    # -- snapshots ----------------------------------------------------------

    def test_snapshots_list_sends_the_task_and_pagination(self):
        page = self.client.tasks.snapshots_list("t1", limit=10, cursor="c1")
        self.assertEqual(page["kind"], "SnapshotList")
        self.assertEqual(page["items"][0]["id"], "snap-abc")

        request = self.last_request("GET", "/v1/snapshots")
        self.assertEqual(request["query"], {"task": ["t1"], "limit": ["10"], "cursor": ["c1"]})

    def test_snapshots_list_without_a_task_is_a_typed_invalid(self):
        with self.assertRaises(ApiError) as caught:
            self.client.tasks.snapshots_list()
        self.assertEqual(caught.exception.code, "invalid")
        self.assertEqual(caught.exception.details, {"parameter": "task"})
        request = self.last_request("GET", "/v1/snapshots")
        self.assertEqual(request["raw_query"], "")

    def test_snapshots_create_sends_the_optional_resources(self):
        document = self.client.tasks.snapshots_create("t1", vcpus=2, memory_mb=256)
        self.assertEqual(document["kind"], "Snapshot")

        request = self.last_request("POST", "/v1/tasks/t1/snapshots")
        self.assertEqual(request["headers"]["content-type"], V1_MEDIA_TYPE)
        self.assertEqual(request["body"], {"vcpus": 2, "memoryMB": 256})

    def test_snapshots_create_without_resources_sends_no_body(self):
        self.client.tasks.snapshots_create("t1")
        request = self.last_request("POST", "/v1/tasks/t1/snapshots")
        self.assertIsNone(request["body"])
        self.assertNotIn("content-type", request["headers"])

    def test_snapshots_restore_uses_the_flat_route_with_the_task(self):
        document = self.client.tasks.snapshots_restore("t1", "snap-abc")
        self.assertEqual(document["id"], "snap-abc")
        request = self.last_request("POST", "/v1/snapshots/snap-abc/restore")
        self.assertEqual(request["query"], {"task": ["t1"]})

    def test_snapshots_delete_uses_the_flat_route_with_the_task(self):
        self.assertIsNone(self.client.tasks.snapshots_delete("t1", "snap-abc"))
        request = self.last_request("DELETE", "/v1/snapshots/snap-abc")
        self.assertEqual(request["query"], {"task": ["t1"]})

    def test_snapshots_stop_restored_deletes_the_restored_vm(self):
        self.assertIsNone(self.client.tasks.snapshots_stop_restored("t1"))
        request = self.last_request("DELETE", "/v1/tasks/t1/restored")
        self.assertEqual(request["raw_query"], "")

    # -- exec ---------------------------------------------------------------

    def test_exec_decodes_frames_and_exposes_the_exit_code(self):
        frames = []
        with self.client.tasks.exec("t1", ["sh", "-c", "hi"], env={"A": "1"}, workdir="/tmp") as run:
            for frame in run:
                frames.append(frame)
            exit_code = run.exit_code
            first = frames[0]

        self.assertEqual([frame["type"] for frame in frames], ["stdout", "stderr", "exit"])
        self.assertEqual(first.type, "stdout")
        self.assertEqual(first.data, b"hello\n")
        self.assertEqual(frames[1].data, b"oops\n")
        self.assertEqual(frames[2].data, b"")
        self.assertEqual(exit_code, 3)

        request = self.last_request("POST", "/v1/tasks/t1/exec")
        self.assertEqual(request["headers"]["accept"], NDJSON_MEDIA_TYPE)
        self.assertEqual(request["headers"]["content-type"], V1_MEDIA_TYPE)
        self.assertEqual(request["body"], {"command": ["sh", "-c", "hi"], "env": {"A": "1"}, "workdir": "/tmp"})

    def test_exec_truncated_stream_raises_unreachable_on_exit_code(self):
        with self.client.tasks.exec("t2", ["false"]) as run:
            frames = list(run)
        self.assertEqual([frame["type"] for frame in frames], ["stdout"])
        with self.assertRaises(ApiError) as caught:
            _ = run.exit_code
        self.assertEqual(caught.exception.code, "unreachable")
        self.assertEqual(caught.exception.status, 0)

    def test_exec_mid_stream_transport_failure_is_unreachable(self):
        frames = []
        with self.assertRaises(ApiError) as caught:
            with self.client.tasks.exec("t10", ["sh"]) as run:
                for frame in run:
                    frames.append(frame)
        self.assertEqual([frame["type"] for frame in frames], ["stdout"])
        self.assertEqual(caught.exception.code, "unreachable")
        self.assertEqual(caught.exception.status, 0)
        self.assertNotIn(TOKEN, str(caught.exception))

    def test_exec_error_frame_raises_the_frame_code(self):
        with self.assertRaises(ApiError) as caught:
            with self.client.tasks.exec("t3", ["sleep", "100"]) as run:
                list(run)
        self.assertEqual(caught.exception.code, "timeout")

    def test_exec_frame_without_a_type_is_internal(self):
        with self.assertRaises(ApiError) as caught:
            with self.client.tasks.exec("t4", ["sh"]) as run:
                list(run)
        self.assertEqual(caught.exception.code, "internal")
        self.assertIn("type", caught.exception.message)

    def test_exec_frame_with_an_unknown_type_is_internal(self):
        with self.assertRaises(ApiError) as caught:
            with self.client.tasks.exec("t5", ["sh"]) as run:
                list(run)
        self.assertEqual(caught.exception.code, "internal")
        self.assertIn("bogus", caught.exception.message)

    def test_exec_frame_with_non_string_data_is_internal(self):
        with self.assertRaises(ApiError) as caught:
            with self.client.tasks.exec("t6", ["sh"]) as run:
                list(run)
        self.assertEqual(caught.exception.code, "internal")
        self.assertIn("base64", caught.exception.message)

    def test_exec_frame_with_invalid_base64_is_internal(self):
        with self.assertRaises(ApiError) as caught:
            with self.client.tasks.exec("t7", ["sh"]) as run:
                list(run)
        self.assertEqual(caught.exception.code, "internal")
        self.assertIn("base64", caught.exception.message)

    # -- logs ---------------------------------------------------------------

    def test_logs_decode_data_and_keep_time_and_stream(self):
        frames = list(self.client.tasks.logs("t1", follow=True, tail=2, since="2026-10-25T09:00:00Z"))

        self.assertEqual([frame.type for frame in frames], ["log", "log", "eof"])
        self.assertEqual(frames[0].data, b"line one\n")
        self.assertEqual(frames[0].stream, "stdout")
        self.assertEqual(frames[0].time, "2026-10-25T10:00:00.5Z")
        self.assertEqual(frames[2].data, b"")

        request = self.last_request("GET", "/v1/tasks/t1/logs")
        self.assertEqual(request["headers"]["accept"], NDJSON_MEDIA_TYPE)
        self.assertEqual(
            request["query"],
            {"follow": ["true"], "tail": ["2"], "since": ["2026-10-25T09:00:00Z"]},
        )

    # -- events -------------------------------------------------------------

    def test_logs_mid_stream_transport_failure_is_unreachable(self):
        frames = []
        with self.assertRaises(ApiError) as caught:
            for frame in self.client.tasks.logs("t10", follow=True):
                frames.append(frame)
        self.assertEqual([frame["type"] for frame in frames], ["log"])
        self.assertEqual(frames[0].data, b"partial\n")
        self.assertEqual(caught.exception.code, "unreachable")
        self.assertEqual(caught.exception.status, 0)

    def test_events_mid_stream_transport_failure_is_unreachable(self):
        events = []
        with self.assertRaises(ApiError) as caught:
            for item in self.client.events.watch(types=["drop"]):
                events.append(item)
        self.assertEqual([item["cursor"] for item in events], ["7"])
        self.assertEqual(caught.exception.code, "unreachable")
        self.assertEqual(caught.exception.status, 0)

    def test_events_watch_yields_events_and_stops_at_end(self):
        events = list(self.client.events.watch(types=["service", "task"]))
        self.assertEqual([item["cursor"] for item in events], ["7", "8"])
        self.assertEqual(events[0]["type"], "service.created")
        self.assertEqual(events[1]["resource"], {"kind": "Service", "id": "t1"})

        request = self.last_request("GET", "/v1/events")
        self.assertEqual(request["headers"]["accept"], SSE_MEDIA_TYPE)
        self.assertEqual(request["query"], {"types": ["service,task"]})
        self.assertNotIn("last-event-id", request["headers"])

    def test_events_watch_sends_last_event_id_when_given_a_cursor(self):
        list(self.client.events.watch(since="7"))
        request = self.last_request("GET", "/v1/events")
        self.assertEqual(request["headers"]["last-event-id"], "7")

    def test_events_watch_raises_api_error_on_an_error_event(self):
        with self.assertRaises(ApiError) as caught:
            list(self.client.events.watch(types=["error"]))
        error = caught.exception
        self.assertEqual(error.code, "unavailable")
        self.assertEqual(error.message, "the broker is down")
        self.assertTrue(error.retryable)
        self.assertEqual(error.status, 200)
        self.assertEqual(error.request_id, REQUEST_ID)

    def test_logs_and_events_clear_the_response_after_closing(self):
        logs = self.client.tasks.logs("t1")
        list(logs)
        self.assertIsNone(logs._response)

        events = self.client.events.watch()
        list(events)
        self.assertIsNone(events._response)

    # -- errors -------------------------------------------------------------

    def test_api_error_carries_the_error_document_fields(self):
        with self.assertRaises(ApiError) as caught:
            self.client._request("GET", "/v1/forbidden")
        error = caught.exception
        self.assertEqual(error.code, "forbidden")
        self.assertEqual(error.message, "the token lacks services:read")
        self.assertEqual(error.status, 403)
        self.assertEqual(error.request_id, REQUEST_ID)
        self.assertFalse(error.retryable)
        self.assertEqual(error.details, {"requiredScope": "services:read"})

    def test_undecodable_error_body_is_internal(self):
        with self.assertRaises(ApiError) as caught:
            self.client._request("GET", "/v1/undecodable")
        self.assertEqual(caught.exception.code, "internal")
        self.assertEqual(caught.exception.status, 500)
        self.assertEqual(caught.exception.request_id, REQUEST_ID)

    def test_redirects_are_refused_so_the_token_is_never_replayed(self):
        sink = ThreadingHTTPServer(("127.0.0.1", 0), SinkHandler)
        sink.requests = []
        thread = threading.Thread(
            target=sink.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
        )
        thread.start()
        try:
            self.server.redirect_to = (
                f"http://127.0.0.1:{sink.server_address[1]}/capture"
            )
            with self.assertRaises(ApiError) as caught:
                self.client._request("GET", "/v1/redirect")
            self.assertEqual(caught.exception.status, 302)
            self.assertEqual(caught.exception.code, "internal")
            self.assertIn("redirect", caught.exception.message)
            self.assertEqual(sink.requests, [], "the redirect target was contacted")
        finally:
            sink.shutdown()
            sink.server_close()
            thread.join(timeout=5)

    def test_transport_failure_is_unreachable_and_never_carries_the_token(self):
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        client = Client(f"http://127.0.0.1:{port}", token="secret-token")
        try:
            with self.assertRaises(ApiError) as caught:
                client.version()
        finally:
            client.close()
        self.assertEqual(caught.exception.code, "unreachable")
        self.assertEqual(caught.exception.status, 0)
        self.assertNotIn("secret-token", str(caught.exception))

    # -- configuration ------------------------------------------------------

    def test_plain_http_is_rejected_for_a_non_loopback_host(self):
        with self.assertRaises(ValueError):
            Client("http://mgr.example.com:4244", token=TOKEN)
        with self.assertRaises(ValueError):
            Client("ftp://127.0.0.1", token=TOKEN)
        Client("https://mgr.example.com:4244", token=TOKEN).close()

    def test_token_and_token_file_are_mutually_exclusive(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "token")
            path.write_text(f"  {TOKEN}\n")
            with self.assertRaises(ValueError):
                Client(self.host, token=TOKEN, token_file=str(path))

            client = Client(self.host, token_file=str(path))
            try:
                self.assertEqual(client.version()["kind"], "VersionInfo")
            finally:
                client.close()
            request = self.last_request("GET", "/v1/version")
            self.assertEqual(request["headers"]["authorization"], f"Bearer {TOKEN}")

    def test_a_client_without_a_token_sends_no_authorization_header(self):
        client = Client(self.host)
        try:
            with self.assertRaises(ApiError) as caught:
                client.version()
        finally:
            client.close()
        self.assertEqual(caught.exception.code, "unauthenticated")
        request = self.last_request("GET", "/v1/version")
        self.assertNotIn("authorization", request["headers"])


class RetryTest(unittest.TestCase):
    """Opt-in retries against the same loopback fixture, with a fake sleep."""

    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.requests = []
        self.server.counters = {}
        self.server.redirect_to = ""
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            kwargs={"poll_interval": 0.01},
            daemon=True,
        )
        self.thread.start()
        self.host = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.delays = []

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def retrying(self, retries=2, **kwargs):
        client = Client(
            self.host,
            token=TOKEN,
            retries=retries,
            retry_backoff=0.5,
            sleep=self.delays.append,
            **kwargs,
        )
        self.addCleanup(client.close)
        return client

    def attempts(self, path):
        return [request for request in self.server.requests if request["path"] == path]

    def test_zero_retries_is_the_default(self):
        client = Client(self.host, token=TOKEN)
        self.addCleanup(client.close)
        with self.assertRaises(ApiError) as caught:
            client._request("GET", "/v1/retry/twice")
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(len(self.attempts("/v1/retry/twice")), 1)
        self.assertEqual(self.delays, [])

    def test_retries_retry_5xx_until_success_with_exponential_backoff(self):
        client = self.retrying(retries=2)
        document = client._request("GET", "/v1/retry/twice")
        self.assertEqual(document["serverVersion"], "v0.11.0")
        self.assertEqual(len(self.attempts("/v1/retry/twice")), 3)
        self.assertEqual(self.delays, [0.5, 1.0])

    def test_retry_after_seconds_is_honoured(self):
        client = self.retrying(retries=1)
        client._request("GET", "/v1/retry/after-seconds")
        self.assertEqual(len(self.attempts("/v1/retry/after-seconds")), 2)
        self.assertEqual(self.delays, [7.0])

    def test_retry_after_http_date_is_honoured(self):
        client = self.retrying(retries=1)
        client._request("GET", "/v1/retry/after-date")
        self.assertEqual(len(self.attempts("/v1/retry/after-date")), 2)
        self.assertEqual(len(self.delays), 1)
        self.assertGreater(self.delays[0], 1.0)
        self.assertLessEqual(self.delays[0], 3.0)

    def test_retry_after_is_capped_by_retry_max_wait(self):
        client = self.retrying(retries=1, retry_max_wait=5.0)
        client._request("GET", "/v1/retry/capped")
        self.assertEqual(self.delays, [5.0])

    def test_a_retry_budget_exhausts_into_the_last_error(self):
        client = self.retrying(retries=1)
        with self.assertRaises(ApiError) as caught:
            client._request("GET", "/v1/retry/always")
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(caught.exception.code, "unavailable")
        self.assertEqual(len(self.attempts("/v1/retry/always")), 2)
        self.assertEqual(len(self.delays), 1)

    def test_a_4xx_is_never_retried(self):
        client = self.retrying(retries=2)
        with self.assertRaises(ApiError) as caught:
            client._request("GET", "/v1/retry/bad-request")
        self.assertEqual(caught.exception.status, 400)
        self.assertEqual(len(self.attempts("/v1/retry/bad-request")), 1)
        self.assertEqual(self.delays, [])

    def test_a_post_without_an_idempotency_key_is_never_retried(self):
        client = self.retrying(retries=2)
        body = {
            "name": "flaky-no-key",
            "spec": {"mode": "replicated", "replicas": 1, "task": {"image": "api:1"}},
        }
        with self.assertRaises(ApiError) as caught:
            client.services.create(body)
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(len(self.attempts("/v1/services")), 1)
        self.assertEqual(self.delays, [])

    def test_a_post_with_an_idempotency_key_is_retried(self):
        client = self.retrying(retries=2)
        body = {
            "name": "flaky",
            "spec": {"mode": "replicated", "replicas": 1, "task": {"image": "api:1"}},
        }
        document = client.services.create(body, idempotency_key="create-1")
        self.assertEqual(document["name"], "flaky")
        self.assertEqual(len(self.attempts("/v1/services")), 3)
        self.assertEqual(self.delays, [0.5, 1.0])
        self.assertTrue(
            all(
                request["headers"]["idempotency-key"] == "create-1"
                for request in self.attempts("/v1/services")
            )
        )

    def test_a_stream_is_never_retried(self):
        client = self.retrying(retries=2)
        with self.assertRaises(ApiError) as caught:
            for _ in client.events.watch(types=["flaky"]):
                pass
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(len(self.attempts("/v1/events")), 1)
        self.assertEqual(self.delays, [])

    def test_retry_after_is_exposed_on_the_error(self):
        client = self.retrying(retries=0)
        with self.assertRaises(ApiError) as caught:
            client._request("GET", "/v1/retry/after-seconds")
        self.assertEqual(caught.exception.retry_after, "7")

    def test_retry_configuration_is_validated(self):
        with self.assertRaises(ValueError):
            Client(self.host, token=TOKEN, retries=-1)
        with self.assertRaises(ValueError):
            Client(self.host, token=TOKEN, retry_backoff=-1)
        with self.assertRaises(ValueError):
            Client(self.host, token=TOKEN, retry_max_wait=-1)
        with self.assertRaises(TypeError):
            Client(self.host, token=TOKEN, sleep="later")


class GeneratedTypesTest(unittest.TestCase):
    """The committed generated models must import and describe the wire shapes."""

    def test_types_are_typed_dicts(self):
        from swarmcracker.types import Service, Task

        self.assertIn("id", Service.__annotations__)
        self.assertIn("serviceId", Task.__annotations__)
        self.assertIn("exitCode", Task.__annotations__)

    def test_the_package_exposes_the_client_and_the_models(self):
        import swarmcracker

        for name in ("ApiError", "Client", "ExecStream", "LogStream", "EventStream", "VersionInfo"):
            self.assertTrue(hasattr(swarmcracker, name), name)


if __name__ == "__main__":
    unittest.main()
