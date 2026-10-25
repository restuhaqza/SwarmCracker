"""A thin, dependency-free client for the SwarmCracker API v1.

The client is a transport wrapper over the documented wire contract: it sends
the v1 media types and bearer token, decodes the generated models from
``swarmcracker.types`` and raises :class:`ApiError` for every typed failure. It
does no caching, no retries and no request parallelism, and it is not thread
safe.

Python 3.11+; standard library only.
"""

from __future__ import annotations

import base64
import http.client
import json
import ssl
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Iterator, Mapping, Sequence

from .types import (
    Event,
    ServiceDocument,
    ServiceList,
    ServiceWrite,
    TaskDocument,
    TaskList,
    VersionInfo,
)

#: The only body media type API v1 serves or accepts.
V1_MEDIA_TYPE = "application/vnd.swarmcracker.v1+json"
#: The request media type of a merge patch (PATCH /v1/services/{ref}).
MERGE_PATCH_MEDIA_TYPE = "application/merge-patch+json"
#: The media type of the exec and log streams, the documented envelope exception.
NDJSON_MEDIA_TYPE = "application/x-ndjson"
#: The media type of the event stream.
SSE_MEDIA_TYPE = "text/event-stream"
#: The frame types of an exec stream (pkg/api/v1.ExecFrame).
EXEC_FRAME_TYPES = frozenset({"stdout", "stderr", "exit", "error"})
#: The API version this client speaks, sent on every request.
API_VERSION = "1"
API_VERSION_HEADER = "X-SwarmCracker-Api-Version"
REQUEST_ID_HEADER = "X-Request-Id"

#: Hosts for which plain http is accepted. Everything else must be https.
LOOPBACK_HOSTS = frozenset({"localhost", "127.0.0.1", "::1"})


class ApiError(Exception):
    """A typed v1 failure, a transport failure or an undecodable response.

    ``code`` is the v1 error code; a transport failure is ``"unreachable"``
    (status 0) and a response the client cannot decode is ``"internal"``. The
    bearer token never appears in any field or message.
    """

    def __init__(
        self,
        code: str,
        message: str,
        *,
        status: int = 0,
        request_id: str | None = None,
        retryable: bool = False,
        details: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.status = status
        self.request_id = request_id
        self.retryable = retryable
        self.details = details

    def __str__(self) -> str:
        if self.status:
            return f"{self.code} (HTTP {self.status}): {self.message}"
        return f"{self.code}: {self.message}"


class Frame(dict):
    """One decoded NDJSON frame.

    It is the wire object (mapping access) with ``data`` base64-decoded to
    ``bytes``. Attribute access is provided for readability, so both
    ``frame["type"]`` and ``frame.type`` work. ``data`` is always present and is
    ``b""`` on frames that carry no payload (``exit``, ``eof``, ``error``).
    """

    __slots__ = ()

    def __getattr__(self, name: str) -> Any:
        try:
            return self[name]
        except KeyError:
            raise AttributeError(name) from None


def _quote(reference: str) -> str:
    """Percent-encode one path reference."""
    return urllib.parse.quote(reference, safe="")


def _query(
    *,
    label: str | Sequence[str] | None = None,
    limit: int | None = None,
    cursor: str | None = None,
    health: bool | None = None,
) -> dict[str, Any] | None:
    """Build the query map of a list request, dropping absent parameters."""
    query: dict[str, Any] = {}
    if label is not None:
        query["label"] = label
    if limit is not None:
        query["limit"] = str(int(limit))
    if cursor is not None:
        query["cursor"] = cursor
    if health is not None:
        query["health"] = "true" if health else "false"
    return query or None


def _base_url(host: str) -> str:
    """Validate the host and return the URL every request is built on."""
    parts = urllib.parse.urlsplit(host)
    if parts.scheme not in ("http", "https") or not parts.hostname:
        raise ValueError(f"host must be an https URL, got {host!r}")
    if parts.scheme == "http" and parts.hostname not in LOOPBACK_HOSTS:
        raise ValueError("plain http is accepted only for localhost, 127.0.0.1 or [::1]")
    path = parts.path.rstrip("/")
    return urllib.parse.urlunsplit((parts.scheme, parts.netloc, path, "", ""))


def _header(headers: Any, name: str) -> str | None:
    """Read one response header, tolerating a missing header object."""
    if headers is None:
        return None
    getter = getattr(headers, "get", None)
    if getter is None:
        return None
    return getter(name)


def _error_payload(document: Any) -> dict[str, Any] | None:
    """Return the Error object of an ErrorDocument (or of a bare Error)."""
    if not isinstance(document, dict):
        return None
    error = document.get("error")
    if isinstance(error, dict):
        return error
    if isinstance(document.get("code"), str):
        return document
    return None


def _api_error(document: Any, status: int, request_id: str | None = None) -> ApiError:
    """Build an ApiError from a decoded error document."""
    payload = _error_payload(document)
    if payload is None:
        return ApiError(
            "internal",
            f"the server returned HTTP {status} with an undecodable error document",
            status=status,
            request_id=request_id,
        )
    code = payload.get("code")
    message = payload.get("message")
    details = payload.get("details")
    return ApiError(
        code if isinstance(code, str) and code else "internal",
        message if isinstance(message, str) and message else f"HTTP {status}",
        status=status,
        request_id=payload.get("requestId") or request_id,
        retryable=bool(payload.get("retryable")),
        details=details if isinstance(details, dict) else None,
    )


def _http_error(exc: urllib.error.HTTPError) -> ApiError:
    """Turn an HTTPError into an ApiError, decoding the v1 ErrorDocument."""
    status = exc.code
    request_id = _header(exc.headers, REQUEST_ID_HEADER)
    raw = b""
    try:
        if exc.fp is not None:
            raw = exc.read()
    except (OSError, ValueError, http.client.HTTPException):
        raw = b""
    finally:
        exc.close()
    if 300 <= status < 400:
        # The redirect handler refuses to follow, so urllib surfaces the 3xx.
        # Following it would replay the bearer token to another origin.
        return ApiError(
            "internal",
            "the server answered with a redirect; the client refuses to follow it",
            status=status,
            request_id=request_id,
        )
    if not raw:
        return _api_error(None, status, request_id)
    try:
        document = json.loads(raw)
    except ValueError:
        return _api_error(None, status, request_id)
    return _api_error(document, status, request_id)


def _transport_error(exc: BaseException) -> ApiError:
    """Turn a transport failure into the code="unreachable" ApiError."""
    reason = getattr(exc, "reason", None) or exc
    return ApiError("unreachable", f"cannot reach the SwarmCracker API: {reason}")


def _stream_transport_error(exc: BaseException) -> ApiError:
    """Turn a mid-stream I/O failure into the code="unreachable" ApiError.

    The stream already answered 200 when its body began, so ``status`` is 0: the
    failure is transport, not a status the server sent. A truncated chunked body
    raises http.client.IncompleteRead, which would otherwise escape as a raw
    exception from whatever protocol library happened to be underneath.
    """
    return ApiError("unreachable", f"the stream failed before it completed: {exc}")


class _NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Refuse every redirect.

    Following one would resend the request (and the bearer token) to whatever
    origin the response names, so a redirect becomes a typed failure instead.
    """

    # The signature is fixed by urllib.request.HTTPRedirectHandler.
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _decode_json(response: Any) -> Any:
    """Decode one JSON response body, or None for an empty body."""
    raw = response.read()
    if not raw:
        return None
    try:
        return json.loads(raw)
    except ValueError:
        raise ApiError(
            "internal",
            "the server returned a body that is not JSON",
            status=getattr(response, "status", 0),
        ) from None


def _decode_frame(payload: Any, status: int) -> Frame:
    """Decode one NDJSON frame: base64 data becomes bytes."""
    if not isinstance(payload, dict):
        raise ApiError("internal", "the stream carried a frame that is not a JSON object", status=status)
    kind = payload.get("type")
    if not isinstance(kind, str) or not kind:
        raise ApiError("internal", "the stream carried a frame without a type", status=status)
    frame = Frame(payload)
    encoded = frame.get("data")
    if encoded is None or encoded == "":
        frame["data"] = b""
        return frame
    if not isinstance(encoded, str):
        raise ApiError(
            "internal", "the stream carried data that is not a base64 string", status=status
        )
    try:
        frame["data"] = base64.b64decode(encoded, validate=True)
    except ValueError:
        raise ApiError("internal", "the stream carried data that is not base64", status=status) from None
    return frame


def _iter_frames(response: Any) -> Iterator[Frame]:
    """Yield the NDJSON frames of a stream response."""
    status = getattr(response, "status", 0)
    for raw in response:
        line = raw.strip()
        if not line:
            continue
        try:
            payload = json.loads(line)
        except ValueError:
            raise ApiError("internal", "the stream carried a line that is not JSON", status=status) from None
        yield _decode_frame(payload, status)


class ExecStream:
    """The NDJSON frames of POST /v1/tasks/{ref}/exec.

    Use it as a context manager or close it explicitly::

        with client.tasks.exec("task-1", ["echo", "hi"]) as run:
            for frame in run:
                ...
        print(run.exit_code)

    ``exit_code`` is the exit frame's status and raises ``ApiError`` with code
    ``unreachable`` when the stream ended without one. An ``error`` frame raises
    ``ApiError`` with the frame's code.
    """

    def __init__(
        self,
        client: Client,
        reference: str,
        command: Sequence[str],
        *,
        env: Mapping[str, str] | None = None,
        workdir: str | None = None,
    ) -> None:
        self._client = client
        self._reference = reference
        self._body: dict[str, Any] = {"command": list(command)}
        if env:
            self._body["env"] = dict(env)
        if workdir is not None:
            self._body["workdir"] = workdir
        self._response: Any = None
        self._exit_code: int | None = None

    @property
    def exit_code(self) -> int:
        """The terminal exit status, or ApiError if the stream never sent one."""
        if self._exit_code is None:
            raise ApiError("unreachable", "the exec stream ended without an exit frame")
        return self._exit_code

    def __iter__(self) -> Iterator[Frame]:
        self._exit_code = None
        self._response = self._client._open(
            "POST",
            f"/v1/tasks/{_quote(self._reference)}/exec",
            body=self._body,
            accept=NDJSON_MEDIA_TYPE,
        )
        try:
            for frame in _iter_frames(self._response):
                kind = frame["type"]
                if kind not in EXEC_FRAME_TYPES:
                    raise ApiError(
                        "internal",
                        f"the exec stream carried an unknown frame type {kind!r}",
                        status=getattr(self._response, "status", 0),
                    )
                if kind == "error":
                    code = frame.get("code") or "internal"
                    raise ApiError(
                        code,
                        f"the exec stream reported an error: {code}",
                        status=getattr(self._response, "status", 0),
                    )
                if kind == "exit":
                    exit_code = frame.get("exitCode")
                    self._exit_code = int(exit_code) if exit_code is not None else 0
                yield frame
        except ApiError:
            raise
        except (OSError, http.client.HTTPException) as exc:
            raise _stream_transport_error(exc) from None
        finally:
            self.close()

    def __enter__(self) -> ExecStream:
        return self

    def __exit__(self, *exc_info: Any) -> bool:
        self.close()
        return False

    def close(self) -> None:
        """Close the response, releasing the connection."""
        if self._response is not None:
            self._response.close()
            self._response = None


class LogStream:
    """The NDJSON frames of GET /v1/tasks/{ref}/logs.

    Yields frames with ``type`` (``log`` or ``eof``), base64-decoded ``data``
    bytes, ``time`` (RFC 3339) and ``stream``.
    """

    def __init__(
        self,
        client: Client,
        reference: str,
        *,
        follow: bool = False,
        tail: int | None = None,
        since: str | None = None,
    ) -> None:
        self._client = client
        self._reference = reference
        self._follow = follow
        self._tail = tail
        self._since = since
        self._response: Any = None

    def __iter__(self) -> Iterator[Frame]:
        query: dict[str, Any] = {}
        if self._follow:
            query["follow"] = "true"
        if self._tail is not None:
            query["tail"] = str(int(self._tail))
        if self._since is not None:
            query["since"] = self._since
        self._response = self._client._open(
            "GET",
            f"/v1/tasks/{_quote(self._reference)}/logs",
            query=query or None,
            accept=NDJSON_MEDIA_TYPE,
        )
        try:
            yield from _iter_frames(self._response)
        except ApiError:
            raise
        except (OSError, http.client.HTTPException) as exc:
            raise _stream_transport_error(exc) from None
        finally:
            self.close()

    def __enter__(self) -> LogStream:
        return self

    def __exit__(self, *exc_info: Any) -> bool:
        self.close()
        return False

    def close(self) -> None:
        """Close the response, releasing the connection."""
        if self._response is not None:
            self._response.close()
            self._response = None


class EventStream:
    """The server-sent events of GET /v1/events.

    Yields the decoded ``Event`` documents, skips the ``ready`` frame, stops at
    ``event: end`` and raises ``ApiError`` for an ``event: error`` frame.
    """

    def __init__(
        self,
        client: Client,
        *,
        types: Sequence[str] | None = None,
        since: str | None = None,
    ) -> None:
        self._client = client
        self._types = list(types) if types else None
        self._since = since
        self._response: Any = None

    def __iter__(self) -> Iterator[Event]:
        query: dict[str, Any] = {}
        if self._types:
            query["types"] = ",".join(self._types)
        headers: dict[str, str] = {}
        if self._since is not None:
            headers["Last-Event-ID"] = str(self._since)
        self._response = self._client._open(
            "GET",
            "/v1/events",
            query=query or None,
            headers=headers or None,
            accept=SSE_MEDIA_TYPE,
        )
        status = getattr(self._response, "status", 0)
        try:
            name = ""
            data: list[str] = []
            for raw in self._response:
                line = raw.decode("utf-8").rstrip("\r\n")
                if line == "":
                    payload = "\n".join(data)
                    if name == "end":
                        return
                    if name == "error" and payload:
                        raise _sse_error(payload, status)
                    if payload and name not in ("", "ready"):
                        yield _sse_event(payload, status)
                    name, data = "", []
                    continue
                if line.startswith(":"):
                    continue
                field, _, value = line.partition(":")
                if value.startswith(" "):
                    value = value[1:]
                if field == "event":
                    name = value
                elif field == "data":
                    data.append(value)
        except ApiError:
            raise
        except (OSError, http.client.HTTPException) as exc:
            raise _stream_transport_error(exc) from None
        finally:
            self.close()

    def __enter__(self) -> EventStream:
        return self

    def __exit__(self, *exc_info: Any) -> bool:
        self.close()
        return False

    def close(self) -> None:
        """Close the response, releasing the connection."""
        if self._response is not None:
            self._response.close()
            self._response = None


def _sse_event(payload: str, status: int) -> Event:
    """Decode one event's data payload."""
    try:
        event = json.loads(payload)
    except ValueError:
        raise ApiError("internal", "the event stream carried a body that is not JSON", status=status) from None
    if not isinstance(event, dict):
        raise ApiError("internal", "the event stream carried a body that is not an event", status=status)
    return event


def _sse_error(payload: str, status: int) -> ApiError:
    """Decode the ErrorDocument an `event: error` frame carries."""
    try:
        document = json.loads(payload)
    except ValueError:
        return ApiError("internal", "the event stream carried an error that is not JSON", status=status)
    return _api_error(document, status)


class Client:
    """A client for one SwarmCracker manager.

    ``host`` must be an https URL; plain http is accepted only for a loopback
    host (tests). Pass at most one of ``token`` and ``token_file``: without a
    token no Authorization header is sent, which is what the trusted Unix socket
    transport expects (it is not reachable over TCP). ``ca`` selects a custom CA
    bundle; without it the system trust store is used.

    The client is safe for sequential reuse and is not thread safe.
    """

    services: Services
    tasks: Tasks
    events: Events

    def __init__(
        self,
        host: str,
        *,
        ca: str | None = None,
        token: str | None = None,
        token_file: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        self._base = _base_url(host)
        if token is not None and token_file is not None:
            raise ValueError("pass either token or token_file, not both")
        if token_file is not None:
            with open(token_file, encoding="utf-8") as handle:
                token = handle.read().strip()
        self._token = token or None
        context = ssl.create_default_context(cafile=ca)
        self._opener = urllib.request.build_opener(
            _NoRedirectHandler(), urllib.request.HTTPSHandler(context=context)
        )
        self._timeout = timeout
        self.services = Services(self)
        self.tasks = Tasks(self)
        self.events = Events(self)

    def version(self) -> VersionInfo:
        """GET /v1/version: the server's identity and capabilities."""
        return self._request("GET", "/v1/version")

    def close(self) -> None:
        """Close the opener. It is safe to call more than once."""
        self._opener.close()

    def __enter__(self) -> Client:
        return self

    def __exit__(self, *exc_info: Any) -> bool:
        self.close()
        return False

    def _request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        body: Any = None,
        headers: Mapping[str, str] | None = None,
        accept: str = V1_MEDIA_TYPE,
        media_type: str = V1_MEDIA_TYPE,
    ) -> Any:
        """Perform one request and decode its JSON body (None when empty)."""
        response = self._open(
            method,
            path,
            query=query,
            body=body,
            headers=headers,
            accept=accept,
            media_type=media_type,
        )
        try:
            return _decode_json(response)
        finally:
            response.close()

    def _open(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        body: Any = None,
        headers: Mapping[str, str] | None = None,
        accept: str = V1_MEDIA_TYPE,
        media_type: str = V1_MEDIA_TYPE,
    ) -> Any:
        """Open a request and return the response, mapping every failure."""
        url = self._base + path
        if query:
            url += "?" + urllib.parse.urlencode(query, doseq=True)
        payload = None
        request_headers = {"Accept": accept, API_VERSION_HEADER: API_VERSION}
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            request_headers["Content-Type"] = media_type
        if self._token is not None:
            request_headers["Authorization"] = f"Bearer {self._token}"
        if headers:
            request_headers.update(headers)
        request = urllib.request.Request(url, data=payload, headers=request_headers, method=method)
        try:
            return self._opener.open(request, timeout=self._timeout)
        except urllib.error.HTTPError as exc:
            raise _http_error(exc) from None
        except (urllib.error.URLError, OSError) as exc:
            raise _transport_error(exc) from None


class Services:
    """The /v1/services routes."""

    def __init__(self, client: Client) -> None:
        self._client = client

    def list(
        self,
        *,
        label: str | Sequence[str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> ServiceList:
        """GET /v1/services: one page of services."""
        return self._client._request(
            "GET", "/v1/services", query=_query(label=label, limit=limit, cursor=cursor)
        )

    def get(self, reference: str) -> ServiceDocument:
        """GET /v1/services/{ref}: one service."""
        return self._client._request("GET", f"/v1/services/{_quote(reference)}")

    def create(self, body: ServiceWrite, *, idempotency_key: str | None = None) -> ServiceDocument:
        """POST /v1/services: create a service."""
        headers = {"Idempotency-Key": idempotency_key} if idempotency_key else None
        return self._client._request("POST", "/v1/services", body=body, headers=headers)

    def patch(
        self,
        reference: str,
        patch: dict[str, Any],
        *,
        if_match: str,
        wait: str | None = None,
    ) -> ServiceDocument:
        """PATCH /v1/services/{ref}: apply an RFC 7386 merge patch."""
        query = {"wait": wait} if wait is not None else None
        return self._client._request(
            "PATCH",
            f"/v1/services/{_quote(reference)}",
            query=query,
            body=patch,
            headers={"If-Match": if_match},
            media_type=MERGE_PATCH_MEDIA_TYPE,
        )

    def delete(self, reference: str, *, if_match: str | None = None) -> None:
        """DELETE /v1/services/{ref}: delete a service.

        ``if_match`` is optional; the API only rejects a stale or weak value, not
        an absent one.
        """
        headers = {"If-Match": if_match} if if_match is not None else None
        self._client._request(
            "DELETE", f"/v1/services/{_quote(reference)}", headers=headers
        )
        return None


class Tasks:
    """The /v1/tasks routes and the exec/log streams."""

    def __init__(self, client: Client) -> None:
        self._client = client

    def list(
        self,
        *,
        label: str | Sequence[str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
        health: bool | None = None,
    ) -> TaskList:
        """GET /v1/tasks: one page of tasks, optionally with live health."""
        return self._client._request(
            "GET",
            "/v1/tasks",
            query=_query(label=label, limit=limit, cursor=cursor, health=health),
        )

    def get(self, reference: str, *, health: bool | None = None) -> TaskDocument:
        """GET /v1/tasks/{ref}: one task, optionally with live health."""
        return self._client._request(
            "GET", f"/v1/tasks/{_quote(reference)}", query=_query(health=health)
        )

    def exec(
        self,
        reference: str,
        command: Sequence[str],
        *,
        env: Mapping[str, str] | None = None,
        workdir: str | None = None,
    ) -> ExecStream:
        """POST /v1/tasks/{ref}/exec: run a command and stream its output."""
        return ExecStream(self._client, reference, command, env=env, workdir=workdir)

    def logs(
        self,
        reference: str,
        *,
        follow: bool = False,
        tail: int | None = None,
        since: str | None = None,
    ) -> LogStream:
        """GET /v1/tasks/{ref}/logs: stream a task's console log."""
        return LogStream(self._client, reference, follow=follow, tail=tail, since=since)


class Events:
    """The /v1/events stream."""

    def __init__(self, client: Client) -> None:
        self._client = client

    def watch(self, *, types: Sequence[str] | None = None, since: str | None = None) -> EventStream:
        """GET /v1/events: stream change events as server-sent events."""
        return EventStream(self._client, types=types, since=since)
