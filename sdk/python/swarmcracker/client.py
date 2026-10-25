"""A thin, dependency-free client for the SwarmCracker API v1.

The client is a transport wrapper over the documented wire contract: it sends
the v1 media types and bearer token, decodes the generated models from
``swarmcracker.types`` and raises :class:`ApiError` for every typed failure. It
does no caching and no request parallelism, and it is not thread safe. Retries
are opt-in (``retries=0`` by default) and are confined to idempotent requests.

Python 3.11+; standard library only.
"""

from __future__ import annotations

import base64
import email.utils
import http.client
import json
import ssl
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from typing import Any, Callable, Iterator, Mapping, Sequence

from .types import (
    Event,
    ServiceDocument,
    ServiceList,
    ServiceWrite,
    SnapshotDocument,
    SnapshotList,
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
#: The media type of a raw file read or write (GET/PUT /v1/tasks/{id}/files).
OCTET_STREAM_MEDIA_TYPE = "application/octet-stream"
#: The response header carrying how long to wait before retrying.
RETRY_AFTER_HEADER = "Retry-After"
#: The wait before the first retry when the server sends no Retry-After.
DEFAULT_RETRY_BACKOFF = 0.5
#: The longest a retry ever waits, whatever Retry-After or the backoff asks for.
DEFAULT_RETRY_MAX_WAIT = 30.0
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
    (status 0) and a response the client cannot decode is ``"internal"``.
    ``retry_after`` is the raw ``Retry-After`` header of the failure, when the
    server sent one. The bearer token never appears in any field or message.
    """

    def __init__(
        self,
        code: str,
        message: str,
        *,
        status: int = 0,
        request_id: str | None = None,
        retryable: bool = False,
        retry_after: str | None = None,
        details: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.status = status
        self.request_id = request_id
        self.retryable = retryable
        self.retry_after = retry_after
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


def _api_error(
    document: Any,
    status: int,
    request_id: str | None = None,
    retry_after: str | None = None,
) -> ApiError:
    """Build an ApiError from a decoded error document."""
    payload = _error_payload(document)
    if payload is None:
        return ApiError(
            "internal",
            f"the server returned HTTP {status} with an undecodable error document",
            status=status,
            request_id=request_id,
            retry_after=retry_after,
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
        retry_after=retry_after,
        details=details if isinstance(details, dict) else None,
    )


def _http_error(exc: urllib.error.HTTPError) -> ApiError:
    """Turn an HTTPError into an ApiError, decoding the v1 ErrorDocument."""
    status = exc.code
    request_id = _header(exc.headers, REQUEST_ID_HEADER)
    retry_after = _header(exc.headers, RETRY_AFTER_HEADER)
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
            retry_after=retry_after,
        )
    if not raw:
        return _api_error(None, status, request_id, retry_after)
    try:
        document = json.loads(raw)
    except ValueError:
        return _api_error(None, status, request_id, retry_after)
    return _api_error(document, status, request_id, retry_after)


def _retry_after_seconds(value: str | None) -> float | None:
    """Decode a Retry-After header: delta-seconds or an HTTP date.

    Returns the wait in seconds, or None when the header is absent or cannot be
    decoded (the caller then falls back to its own backoff).
    """
    if value is None:
        return None
    text = value.strip()
    if text == "":
        return None
    try:
        return max(0.0, float(text))
    except ValueError:
        pass
    try:
        when = email.utils.parsedate_to_datetime(text)
    except (TypeError, ValueError):
        return None
    if when is None:
        return None
    if when.tzinfo is None:
        when = when.replace(tzinfo=timezone.utc)
    return max(0.0, (when - datetime.now(timezone.utc)).total_seconds())


def _idempotent(method: str, headers: Mapping[str, str]) -> bool:
    """Whether a request may be replayed safely.

    GET, PUT (the files upload, whose whole body is resent) and DELETE are
    idempotent; a POST is only replayable when it carries an Idempotency-Key.
    Streams are excluded by the caller: a replayed stream would lose its offset.
    """
    if method in ("GET", "PUT", "DELETE"):
        return True
    return method == "POST" and any(name.lower() == "idempotency-key" for name in headers)


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
            stream=True,
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
            stream=True,
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
            stream=True,
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

    ``retries`` is the number of extra attempts for an idempotent request that
    fails with ``429`` or ``5xx``; 0 (the default) never retries. The wait is
    ``Retry-After`` when the server sends one (seconds or an HTTP date, capped at
    ``retry_max_wait``), otherwise ``retry_backoff`` doubled per attempt.
    ``sleep`` is the function used for that wait; tests inject a fake one.

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
        retries: int = 0,
        retry_backoff: float = DEFAULT_RETRY_BACKOFF,
        retry_max_wait: float = DEFAULT_RETRY_MAX_WAIT,
        sleep: Callable[[float], None] | None = None,
    ) -> None:
        self._base = _base_url(host)
        if token is not None and token_file is not None:
            raise ValueError("pass either token or token_file, not both")
        if not isinstance(retries, int) or isinstance(retries, bool) or retries < 0:
            raise ValueError("retries must be a non-negative integer")
        if retry_backoff < 0:
            raise ValueError("retry_backoff must be non-negative")
        if retry_max_wait < 0:
            raise ValueError("retry_max_wait must be non-negative")
        if sleep is not None and not callable(sleep):
            raise TypeError("sleep must be callable")
        if token_file is not None:
            with open(token_file, encoding="utf-8") as handle:
                token = handle.read().strip()
        self._token = token or None
        context = ssl.create_default_context(cafile=ca)
        self._opener = urllib.request.build_opener(
            _NoRedirectHandler(), urllib.request.HTTPSHandler(context=context)
        )
        self._timeout = timeout
        self._retries = retries
        self._retry_backoff = float(retry_backoff)
        self._retry_max_wait = float(retry_max_wait)
        self._sleep = sleep or time.sleep
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
        raw_body: bytes | None = None,
        headers: Mapping[str, str] | None = None,
        accept: str = V1_MEDIA_TYPE,
        media_type: str = V1_MEDIA_TYPE,
        stream: bool = False,
    ) -> Any:
        """Open a request and return the response, mapping every failure.

        ``stream`` marks a response whose body is consumed incrementally; such a
        request is never retried, because a replay would restart the stream from
        an offset the caller has already seen. ``raw_body`` sends bytes verbatim
        (the files upload) instead of a JSON document.
        """
        url = self._base + path
        if query:
            url += "?" + urllib.parse.urlencode(query, doseq=True)
        payload = None
        request_headers = {"Accept": accept, API_VERSION_HEADER: API_VERSION}
        if raw_body is not None:
            payload = bytes(raw_body)
            request_headers["Content-Type"] = media_type
        elif body is not None:
            payload = json.dumps(body).encode("utf-8")
            request_headers["Content-Type"] = media_type
        if self._token is not None:
            request_headers["Authorization"] = f"Bearer {self._token}"
        if headers:
            request_headers.update(headers)
        attempt = 0
        while True:
            request = urllib.request.Request(
                url, data=payload, headers=request_headers, method=method
            )
            try:
                return self._opener.open(request, timeout=self._timeout)
            except urllib.error.HTTPError as exc:
                if self._retry(method, request_headers, exc.code, attempt, stream):
                    retry_after = _header(exc.headers, RETRY_AFTER_HEADER)
                    exc.close()
                    self._sleep(self._retry_wait(attempt, retry_after))
                    attempt += 1
                    continue
                raise _http_error(exc) from None
            except (urllib.error.URLError, OSError) as exc:
                raise _transport_error(exc) from None

    def _retry(
        self,
        method: str,
        headers: Mapping[str, str],
        status: int,
        attempt: int,
        stream: bool,
    ) -> bool:
        """Whether one failed attempt may be replayed."""
        if stream or attempt >= self._retries:
            return False
        if status != 429 and not 500 <= status < 600:
            return False
        return _idempotent(method, headers)

    def _retry_wait(self, attempt: int, retry_after: str | None) -> float:
        """How long to wait before retry number ``attempt`` + 1, capped."""
        wait = _retry_after_seconds(retry_after)
        if wait is None:
            wait = self._retry_backoff * (2**attempt)
        return min(max(wait, 0.0), self._retry_max_wait)


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

    def files_get(self, task: str, path: str) -> bytes:
        """GET /v1/tasks/{ref}/files: read one regular file's raw bytes.

        The body is bytes, not an envelope document. The path must be absolute
        and the task must still be running; both are enforced by the server and
        surface as :class:`ApiError`.
        """
        response = self._client._open(
            "GET",
            f"/v1/tasks/{_quote(task)}/files",
            query={"path": path},
            accept=OCTET_STREAM_MEDIA_TYPE,
        )
        try:
            raw = response.read()
        finally:
            response.close()
        return raw

    def files_put(
        self,
        task: str,
        path: str,
        data: bytes,
        mode: str | None = None,
    ) -> None:
        """PUT /v1/tasks/{ref}/files: write one regular file's raw bytes.

        ``data`` is the whole body and is resent on a retry; ``mode`` is an octal
        string between ``0000`` and ``0777``. A 204 answer becomes ``None``.
        """
        if not isinstance(data, (bytes, bytearray, memoryview)):
            raise TypeError("data must be bytes")
        query: dict[str, Any] = {"path": path}
        if mode is not None:
            query["mode"] = mode
        response = self._client._open(
            "PUT",
            f"/v1/tasks/{_quote(task)}/files",
            query=query,
            raw_body=bytes(data),
            media_type=OCTET_STREAM_MEDIA_TYPE,
        )
        response.close()
        return None

    def snapshots_list(
        self,
        task: str | None = None,
        *,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> SnapshotList:
        """GET /v1/snapshots: one page of a task's snapshots.

        ``task`` is required by the API; it is optional here only so the flat
        route can be called without rebuilding the task path. Omitting it makes
        the server answer 400 invalid with ``details.parameter`` "task".
        """
        query: dict[str, Any] = {}
        if task is not None:
            query["task"] = task
        if limit is not None:
            query["limit"] = str(int(limit))
        if cursor is not None:
            query["cursor"] = cursor
        return self._client._request("GET", "/v1/snapshots", query=query or None)

    def snapshots_create(
        self,
        task: str,
        vcpus: int | None = None,
        memory_mb: int | None = None,
    ) -> SnapshotDocument:
        """POST /v1/tasks/{ref}/snapshots: snapshot the task's VM.

        With neither ``vcpus`` nor ``memory_mb`` the request has no body and the
        node snapshots the VM with the resources it is running with.
        """
        body: dict[str, Any] = {}
        if vcpus is not None:
            body["vcpus"] = int(vcpus)
        if memory_mb is not None:
            body["memoryMB"] = int(memory_mb)
        return self._client._request(
            "POST",
            f"/v1/tasks/{_quote(task)}/snapshots",
            body=body or None,
        )

    def snapshots_restore(self, task: str, snapshot: str) -> SnapshotDocument:
        """POST /v1/snapshots/{sid}/restore: restore one of a task's snapshots."""
        return self._client._request(
            "POST",
            f"/v1/snapshots/{_quote(snapshot)}/restore",
            query={"task": task},
        )

    def snapshots_delete(self, task: str, snapshot: str) -> None:
        """DELETE /v1/snapshots/{sid}: delete one of a task's snapshots."""
        self._client._request(
            "DELETE",
            f"/v1/snapshots/{_quote(snapshot)}",
            query={"task": task},
        )
        return None

    def snapshots_stop_restored(self, task: str) -> None:
        """DELETE /v1/tasks/{ref}/restored: stop the task's restored VM.

        A restore starts a Firecracker process outside SwarmKit's task
        lifecycle, so stopping the task does not stop it; this route does.
        """
        self._client._request("DELETE", f"/v1/tasks/{_quote(task)}/restored")
        return None


class Events:
    """The /v1/events stream."""

    def __init__(self, client: Client) -> None:
        self._client = client

    def watch(self, *, types: Sequence[str] | None = None, since: str | None = None) -> EventStream:
        """GET /v1/events: stream change events as server-sent events."""
        return EventStream(self._client, types=types, since=since)
