// A thin, dependency-free client for the SwarmCracker API v1.
//
// The client is a transport wrapper over the documented wire contract: it sends
// the v1 media types and bearer token, decodes the generated models in
// `types.d.ts` and rejects with ApiError for every typed failure. It does no
// caching and no request parallelism. Retries are opt-in (`retries: 0` by
// default) and are confined to idempotent requests. It is built on node:https
// and node:http (global fetch cannot use a custom CA without a bundled
// dispatcher).
//
// Node 18+; no third-party package.

import { readFileSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';

/** The only body media type API v1 serves or accepts. */
export const V1_MEDIA_TYPE = 'application/vnd.swarmcracker.v1+json';
/** The request media type of a merge patch (PATCH /v1/services/{ref}). */
export const MERGE_PATCH_MEDIA_TYPE = 'application/merge-patch+json';
/** The media type of the exec and log streams, the documented envelope exception. */
export const NDJSON_MEDIA_TYPE = 'application/x-ndjson';
/** The media type of the event stream. */
export const SSE_MEDIA_TYPE = 'text/event-stream';
/** The media type of a raw file read or write (GET/PUT /v1/tasks/{id}/files). */
export const OCTET_STREAM_MEDIA_TYPE = 'application/octet-stream';
/** The frame types of an exec stream (pkg/api/v1.ExecFrame). */
export const EXEC_FRAME_TYPES = new Set(['stdout', 'stderr', 'exit', 'error']);
/** The API version this client speaks, sent on every request. */
export const API_VERSION = '1';
export const API_VERSION_HEADER = 'X-SwarmCracker-Api-Version';
export const REQUEST_ID_HEADER = 'X-Request-Id';
/** The response header carrying how long to wait before retrying. */
export const RETRY_AFTER_HEADER = 'Retry-After';
/** The wait before the first retry when the server sends no Retry-After. */
export const DEFAULT_RETRY_BACKOFF_MS = 500;
/** The longest a retry ever waits, whatever Retry-After or the backoff asks for. */
export const DEFAULT_RETRY_MAX_WAIT_MS = 30000;

/** Hosts for which plain http is accepted. Everything else must be https. */
export const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1', '[::1]']);

const DEFAULT_TIMEOUT_MS = 30000;

/**
 * A typed v1 failure, a transport failure or an undecodable response.
 *
 * `code` is the v1 error code; a transport failure is `unreachable` (status 0)
 * and a response the client cannot decode is `internal`. `retryAfter` is the raw
 * `Retry-After` header of the failure, when the server sent one. The bearer token
 * never appears in any field or message.
 */
export class ApiError extends Error {
  constructor(
    code,
    message,
    { status = 0, requestId = null, retryable = false, retryAfter = null, details = null } = {},
  ) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    this.status = status;
    this.requestId = requestId ?? null;
    this.retryable = Boolean(retryable);
    this.retryAfter = retryAfter ?? null;
    this.details = details ?? null;
  }
}

// -- helpers -----------------------------------------------------------------

function quoteReference(reference) {
  return encodeURIComponent(String(reference));
}

function buildQuery(source) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(source ?? {})) {
    if (value === undefined || value === null) {
      continue;
    }
    if (Array.isArray(value)) {
      for (const item of value) {
        if (item !== undefined && item !== null) {
          params.append(key, String(item));
        }
      }
      continue;
    }
    params.append(key, String(value));
  }
  return params;
}

function parseHost(host) {
  if (typeof host !== 'string' || host === '') {
    throw new TypeError('host must be an https URL');
  }
  let url;
  try {
    url = new URL(host);
  } catch {
    throw new TypeError(`host must be an https URL, got ${JSON.stringify(host)}`);
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    throw new TypeError(`host must be an https URL, got ${JSON.stringify(host)}`);
  }
  if (!url.hostname) {
    throw new TypeError(`host must name a host, got ${JSON.stringify(host)}`);
  }
  if (url.protocol === 'http:' && !LOOPBACK_HOSTS.has(url.hostname.toLowerCase())) {
    throw new TypeError('plain http is accepted only for localhost, 127.0.0.1 or [::1]');
  }
  // The query and fragment of the configured host are dropped, like the Python
  // client: the client owns the query string.
  const path = url.pathname.replace(/\/+$/, '');
  return { base: `${url.protocol}//${url.host}${path}`, protocol: url.protocol };
}

function resolveCA(ca) {
  if (ca === undefined || ca === null) {
    return undefined;
  }
  if (typeof ca !== 'string' || ca === '') {
    throw new TypeError('ca must be a PEM string or a path to a PEM file');
  }
  if (ca.includes('-----BEGIN')) {
    return ca;
  }
  try {
    return readFileSync(ca, 'utf8');
  } catch (error) {
    throw new ApiError('usage', `cannot read the CA file: ${error.message}`);
  }
}

function resolveToken({ token, tokenFile }) {
  if (token !== undefined && token !== null && tokenFile !== undefined && tokenFile !== null) {
    throw new TypeError('pass either token or tokenFile, not both');
  }
  if (tokenFile !== undefined && tokenFile !== null) {
    let raw;
    try {
      raw = readFileSync(tokenFile, 'utf8');
    } catch (error) {
      throw new ApiError('usage', `cannot read the token file: ${error.message}`);
    }
    const trimmed = raw.trim();
    return trimmed === '' ? null : trimmed;
  }
  if (typeof token === 'string' && token !== '') {
    return token;
  }
  return null;
}

function transportFailure(error) {
  const reason = error?.message ?? String(error);
  return new ApiError('unreachable', `cannot reach the SwarmCracker API: ${reason}`);
}

function headerValue(headers, name) {
  if (!headers) {
    return null;
  }
  const value = headers[name.toLowerCase()];
  if (Array.isArray(value)) {
    return value[0] ?? null;
  }
  return value ?? null;
}

function errorPayload(document) {
  if (document === null || typeof document !== 'object' || Array.isArray(document)) {
    return null;
  }
  const error = document.error;
  if (error !== null && typeof error === 'object' && !Array.isArray(error)) {
    return error;
  }
  if (typeof document.code === 'string') {
    return document;
  }
  return null;
}

function apiErrorFromDocument(document, status, requestId = null, retryAfter = null) {
  const payload = errorPayload(document);
  if (payload === null) {
    return new ApiError(
      'internal',
      `the server returned HTTP ${status} with an undecodable error document`,
      { status, requestId, retryAfter },
    );
  }
  const code = typeof payload.code === 'string' && payload.code !== '' ? payload.code : 'internal';
  const message =
    typeof payload.message === 'string' && payload.message !== '' ? payload.message : `HTTP ${status}`;
  return new ApiError(code, message, {
    status,
    requestId: payload.requestId || requestId,
    retryable: Boolean(payload.retryable),
    retryAfter,
    details: payload.details !== null && typeof payload.details === 'object' ? payload.details : null,
  });
}

function httpFailure(status, headers, raw) {
  const requestId = headerValue(headers, REQUEST_ID_HEADER);
  const retryAfter = headerValue(headers, RETRY_AFTER_HEADER);
  if (status >= 300 && status < 400) {
    // A redirect is never followed: doing so would replay the bearer token to
    // whatever origin the response names.
    return new ApiError(
      'internal',
      'the server answered with a redirect; the client refuses to follow it',
      { status, requestId, retryAfter },
    );
  }
  let document = null;
  if (raw.length > 0) {
    try {
      document = JSON.parse(raw.toString('utf8'));
    } catch {
      document = null;
    }
  }
  return apiErrorFromDocument(document, status, requestId, retryAfter);
}

/**
 * Decode a Retry-After header into milliseconds: delta-seconds or an HTTP date.
 * Returns null when the header is absent, cannot be decoded, or names a moment
 * that is not in the future (a zero or negative hint), so the caller falls back
 * to its own backoff rather than retrying instantly.
 */
function retryAfterMs(value) {
  if (value === null || value === undefined) {
    return null;
  }
  const text = String(value).trim();
  if (text === '') {
    return null;
  }
  // Match a signed number explicitly: Date.parse would otherwise read "-5" or
  // "0" as a year, which is not a delta-seconds hint at all.
  if (/^[+-]?(?:\d+(?:\.\d+)?|\.\d+)$/.test(text)) {
    const seconds = Number(text);
    return Number.isFinite(seconds) && seconds > 0 ? seconds * 1000 : null;
  }
  const when = Date.parse(text);
  if (Number.isNaN(when)) {
    return null;
  }
  const remaining = when - Date.now();
  return remaining > 0 ? remaining : null;
}

/**
 * Whether a request may be replayed safely. GET, PUT (the files upload, whose
 * whole body is resent) and DELETE are idempotent; a POST is only replayable
 * with an Idempotency-Key. Streams are excluded by the caller.
 */
function idempotent(method, headers) {
  if (method === 'GET' || method === 'PUT' || method === 'DELETE') {
    return true;
  }
  if (method !== 'POST' || headers === null || headers === undefined) {
    return false;
  }
  return Object.keys(headers).some(
    (name) => name.toLowerCase() === 'idempotency-key' && headers[name] !== undefined && headers[name] !== null,
  );
}

function defaultSleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function decodeJson(body, status) {
  if (body.length === 0) {
    return null;
  }
  try {
    return JSON.parse(body.toString('utf8'));
  } catch {
    throw new ApiError('internal', 'the server returned a body that is not JSON', { status });
  }
}

function decodeBase64(value, status) {
  if (value === undefined || value === null || value === '') {
    return new Uint8Array(0);
  }
  if (typeof value !== 'string') {
    throw new ApiError('internal', 'the stream carried data that is not a base64 string', { status });
  }
  // Buffer.from is lenient (it skips unknown characters), so validate first.
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(value) || value.length % 4 !== 0) {
    throw new ApiError('internal', 'the stream carried data that is not base64', { status });
  }
  return new Uint8Array(Buffer.from(value, 'base64'));
}

function decodeFrame(payload, status) {
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new ApiError('internal', 'the stream carried a frame that is not a JSON object', { status });
  }
  if (typeof payload.type !== 'string' || payload.type === '') {
    throw new ApiError('internal', 'the stream carried a frame without a type', { status });
  }
  return { ...payload, data: decodeBase64(payload.data, status) };
}

async function* readLines(response) {
  response.setEncoding('utf8');
  let buffer = '';
  for await (const chunk of response) {
    buffer += chunk;
    let index = buffer.indexOf('\n');
    while (index >= 0) {
      yield buffer.slice(0, index);
      buffer = buffer.slice(index + 1);
      index = buffer.indexOf('\n');
    }
  }
  if (buffer !== '') {
    yield buffer;
  }
}

async function* decodeFrames(response, status) {
  for await (const line of readLines(response)) {
    const trimmed = line.trim();
    if (trimmed === '') {
      continue;
    }
    let payload;
    try {
      payload = JSON.parse(trimmed);
    } catch {
      throw new ApiError('internal', 'the stream carried a line that is not JSON', { status });
    }
    yield decodeFrame(payload, status);
  }
}

function sseError(payload, status) {
  let document;
  try {
    document = JSON.parse(payload);
  } catch {
    return new ApiError('internal', 'the event stream carried an error that is not JSON', { status });
  }
  return apiErrorFromDocument(document, status);
}

function sseEvent(payload, status) {
  let event;
  try {
    event = JSON.parse(payload);
  } catch {
    throw new ApiError('internal', 'the event stream carried a body that is not JSON', { status });
  }
  if (event === null || typeof event !== 'object' || Array.isArray(event)) {
    throw new ApiError('internal', 'the event stream carried a body that is not an event', { status });
  }
  return event;
}

// -- client ------------------------------------------------------------------

/**
 * A client for one SwarmCracker manager.
 *
 * `host` must be an https URL; plain http is accepted only for a loopback host
 * (tests). Pass at most one of `token` and `tokenFile`: without a token no
 * Authorization header is sent, which is what the trusted Unix socket transport
 * expects (it is not reachable over TCP). `ca` is a PEM string or a path to a PEM
 * file and selects a custom trust store; without it the system trust store is
 * used. TLS verification is never disabled.
 *
 * `retries` is the number of extra attempts for an idempotent request that fails
 * with `429` or `5xx`; 0 (the default) never retries. The wait is `Retry-After`
 * when the server sends a usable one (seconds or an HTTP date, capped at
 * `retryMaxWaitMs`), otherwise `retryBackoffMs` doubled per attempt. A hint of
 * zero, a negative one or an undecodable one counts as absent and falls back to
 * the backoff. `sleep` is the wait function; tests inject a fake one.
 *
 * The client is safe for sequential reuse and is not concurrency safe.
 */
export class Client {
  #base;
  #token;
  #ca;
  #timeoutMs;
  #retries;
  #retryBackoffMs;
  #retryMaxWaitMs;
  #sleep;
  #agents;

  constructor({
    host,
    ca,
    token,
    tokenFile,
    timeoutMs = DEFAULT_TIMEOUT_MS,
    retries = 0,
    retryBackoffMs = DEFAULT_RETRY_BACKOFF_MS,
    retryMaxWaitMs = DEFAULT_RETRY_MAX_WAIT_MS,
    sleep,
  } = {}) {
    const { base } = parseHost(host);
    if (typeof timeoutMs !== 'number' || !Number.isFinite(timeoutMs) || timeoutMs <= 0) {
      throw new TypeError('timeoutMs must be a positive number of milliseconds');
    }
    if (!Number.isInteger(retries) || retries < 0) {
      throw new TypeError('retries must be a non-negative integer');
    }
    if (typeof retryBackoffMs !== 'number' || !Number.isFinite(retryBackoffMs) || retryBackoffMs < 0) {
      throw new TypeError('retryBackoffMs must be a non-negative number of milliseconds');
    }
    if (typeof retryMaxWaitMs !== 'number' || !Number.isFinite(retryMaxWaitMs) || retryMaxWaitMs < 0) {
      throw new TypeError('retryMaxWaitMs must be a non-negative number of milliseconds');
    }
    if (sleep !== undefined && typeof sleep !== 'function') {
      throw new TypeError('sleep must be a function');
    }
    this.#base = base;
    this.#token = resolveToken({ token, tokenFile });
    this.#ca = resolveCA(ca);
    this.#timeoutMs = timeoutMs;
    this.#retries = retries;
    this.#retryBackoffMs = retryBackoffMs;
    this.#retryMaxWaitMs = retryMaxWaitMs;
    this.#sleep = sleep ?? defaultSleep;
    this.#agents = new Map();

    this.services = new Services(this);
    this.tasks = new Tasks(this);
    this.events = new Events(this);
  }

  /** GET /v1/version: the server's identity and capabilities. */
  version() {
    return this._request('GET', '/v1/version');
  }

  /** Close every pooled connection. Safe to call more than once. */
  close() {
    for (const agent of this.#agents.values()) {
      agent.destroy();
    }
    this.#agents.clear();
  }

  #url(path, query) {
    const url = new URL(this.#base + path);
    const search = buildQuery(query).toString();
    if (search !== '') {
      url.search = search;
    }
    return url;
  }

  #headers({ accept = V1_MEDIA_TYPE, mediaType = V1_MEDIA_TYPE, headers = null, body, rawBody } = {}) {
    const result = { Accept: accept, [API_VERSION_HEADER]: API_VERSION };
    if (body !== undefined || rawBody !== undefined) {
      result['Content-Type'] = mediaType;
    }
    if (this.#token !== null) {
      result.Authorization = `Bearer ${this.#token}`;
    }
    if (headers) {
      for (const [name, value] of Object.entries(headers)) {
        if (value !== undefined && value !== null) {
          result[name] = String(value);
        }
      }
    }
    return result;
  }

  #agent(protocol) {
    let agent = this.#agents.get(protocol);
    if (agent === undefined) {
      agent =
        protocol === 'https:'
          ? // `ca` is either a PEM bundle or undefined (system trust store).
            new https.Agent({ keepAlive: true, ca: this.#ca })
          : new http.Agent({ keepAlive: true });
      this.#agents.set(protocol, agent);
    }
    return agent;
  }

  /**
   * Perform one request, retrying an idempotent failure when configured.
   * Resolves with `{status, headers, body}` for a buffered response or
   * `{response}` when `stream` is set. Internal.
   */
  async #send(method, path, options = {}) {
    let attempt = 0;
    for (;;) {
      try {
        return await this.#attempt(method, path, options);
      } catch (error) {
        if (!(error instanceof ApiError) || !this.#retryable(method, options, error, attempt)) {
          throw error;
        }
        await this.#sleep(this.#retryDelay(attempt, error.retryAfter));
        attempt += 1;
      }
    }
  }

  #retryable(method, options, error, attempt) {
    if (options.stream || attempt >= this.#retries) {
      return false;
    }
    if (error.status !== 429 && !(error.status >= 500 && error.status <= 599)) {
      return false;
    }
    return idempotent(method, options.headers);
  }

  #retryDelay(attempt, retryAfter) {
    const hinted = retryAfterMs(retryAfter);
    const wait = hinted === null ? this.#retryBackoffMs * 2 ** attempt : hinted;
    return Math.min(Math.max(wait, 0), this.#retryMaxWaitMs);
  }

  /** Perform exactly one attempt, mapping every failure. Internal. */
  #attempt(method, path, options = {}) {
    const {
      query,
      body,
      rawBody,
      headers,
      accept,
      mediaType,
      signal,
      stream = false,
      timeoutMs = this.#timeoutMs,
    } = options;

    return new Promise((resolve, reject) => {
      const url = this.#url(path, query);
      let payload = null;
      if (rawBody !== undefined && rawBody !== null) {
        payload = Buffer.from(rawBody);
      } else if (body !== undefined) {
        payload = Buffer.from(JSON.stringify(body), 'utf8');
      }
      const requestHeaders = this.#headers({ accept, mediaType, headers, body, rawBody });
      if (payload !== null) {
        requestHeaders['Content-Length'] = String(payload.length);
      }

      let timer = null;
      let settled = false;
      let request = null;

      const clearDeadline = () => {
        if (timer !== null) {
          clearTimeout(timer);
          timer = null;
        }
      };

      const settle = (callback, value) => {
        if (settled) {
          return;
        }
        settled = true;
        clearDeadline();
        if (signal) {
          signal.removeEventListener('abort', onAbort);
        }
        callback(value);
      };

      function onAbort() {
        settle(reject, new ApiError('unreachable', 'the request was cancelled by the caller'));
        request?.destroy();
      }

      const transport = url.protocol === 'https:' ? https : http;
      try {
        request = transport.request(url, {
          method,
          headers: requestHeaders,
          agent: this.#agent(url.protocol),
        });
      } catch (error) {
        settle(reject, transportFailure(error));
        return;
      }

      if (signal) {
        if (signal.aborted) {
          onAbort();
          return;
        }
        signal.addEventListener('abort', onAbort);
      }

      if (Number.isFinite(timeoutMs)) {
        timer = setTimeout(() => {
          settle(
            reject,
            new ApiError('unreachable', `${method} ${url.pathname} timed out after ${timeoutMs}ms`),
          );
          request.destroy();
        }, timeoutMs);
        if (typeof timer.unref === 'function') {
          timer.unref();
        }
      }

      request.on('response', (response) => {
        const status = response.statusCode ?? 0;

        if (stream && status >= 200 && status < 300) {
          // Headers arrived: the connect deadline is met. The stream itself has
          // no total deadline, so the caller can follow it for as long as it
          // runs.
          settle(resolve, { response });
          return;
        }

        const chunks = [];
        response.on('data', (chunk) => {
          chunks.push(chunk);
        });
        response.on('end', () => {
          const raw = Buffer.concat(chunks);
          if (status >= 200 && status < 300) {
            settle(resolve, { status, headers: response.headers, body: raw });
            return;
          }
          settle(reject, httpFailure(status, response.headers, raw));
        });
        response.on('error', (error) => {
          settle(reject, transportFailure(error));
        });
        response.on('close', () => {
          if (!response.complete) {
            settle(
              reject,
              new ApiError('unreachable', 'the connection closed before the response completed'),
            );
          }
        });
      });

      request.on('error', (error) => {
        settle(reject, error instanceof ApiError ? error : transportFailure(error));
      });

      if (payload !== null) {
        request.write(payload);
      }
      request.end();
    });
  }

  /** Perform one request and decode its JSON body (null when empty). Internal. */
  async _request(method, path, options = {}) {
    const { status, body } = await this.#send(method, path, options);
    return decodeJson(body, status);
  }

  /** Perform one request and return the buffered response undecoded. Internal. */
  _requestRaw(method, path, options = {}) {
    return this.#send(method, path, options);
  }

  /** Open a stream response, mapping every non-2xx to ApiError. Internal. */
  _openStream(method, path, options = {}) {
    return this.#send(method, path, { ...options, stream: true });
  }
}

// -- routes ------------------------------------------------------------------

/** The /v1/services routes. */
export class Services {
  #client;

  constructor(client) {
    this.#client = client;
  }

  /** GET /v1/services: one page of services. */
  list({ label, limit, cursor } = {}) {
    return this.#client._request('GET', '/v1/services', { query: { label, limit, cursor } });
  }

  /** GET /v1/services/{ref}: one service. */
  get(reference) {
    return this.#client._request('GET', `/v1/services/${quoteReference(reference)}`);
  }

  /** POST /v1/services: create a service. */
  create(body, { idempotencyKey } = {}) {
    const headers = idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : null;
    return this.#client._request('POST', '/v1/services', { body, headers });
  }

  /** PATCH /v1/services/{ref}: apply an RFC 7386 merge patch. */
  patch(reference, patch, { ifMatch, wait } = {}) {
    if (!ifMatch) {
      throw new TypeError('services.patch requires ifMatch');
    }
    return this.#client._request('PATCH', `/v1/services/${quoteReference(reference)}`, {
      query: { wait },
      body: patch,
      headers: { 'If-Match': ifMatch },
      mediaType: MERGE_PATCH_MEDIA_TYPE,
    });
  }

  /** DELETE /v1/services/{ref}: delete a service. */
  async delete(reference, { ifMatch } = {}) {
    const headers = ifMatch ? { 'If-Match': ifMatch } : null;
    await this.#client._request('DELETE', `/v1/services/${quoteReference(reference)}`, {
      headers,
    });
    return null;
  }
}

/** The /v1/tasks routes and the exec/log streams. */
export class Tasks {
  #client;

  constructor(client) {
    this.#client = client;
  }

  /** GET /v1/tasks: one page of tasks, optionally with live health. */
  list({ label, limit, cursor, health } = {}) {
    return this.#client._request('GET', '/v1/tasks', {
      query: { label, limit, cursor, health: flag(health) },
    });
  }

  /** GET /v1/tasks/{ref}: one task, optionally with live health. */
  get(reference, { health } = {}) {
    return this.#client._request('GET', `/v1/tasks/${quoteReference(reference)}`, {
      query: { health: flag(health) },
    });
  }

  /** POST /v1/tasks/{ref}/exec: run a command and stream its output. */
  exec(reference, command, options = {}) {
    return new ExecStream(this.#client, reference, command, options);
  }

  /** GET /v1/tasks/{ref}/logs: stream a task's console log. */
  logs(reference, options = {}) {
    return new LogStream(this.#client, reference, options);
  }

  /** GET /v1/tasks/{ref}/files: read one regular file's raw bytes. */
  async filesGet(reference, path) {
    const { body } = await this.#client._requestRaw('GET', `/v1/tasks/${quoteReference(reference)}/files`, {
      query: { path },
      accept: OCTET_STREAM_MEDIA_TYPE,
    });
    return body;
  }

  /**
   * PUT /v1/tasks/{ref}/files: write one regular file's raw bytes.
   *
   * `data` is the whole body and is resent on a retry; `mode` is an octal string
   * between `0000` and `0777`. A 204 answer becomes `null`.
   */
  async filesPut(reference, path, data, { mode } = {}) {
    if (!(data instanceof Uint8Array)) {
      throw new TypeError('data must be a Uint8Array or Buffer');
    }
    await this.#client._requestRaw('PUT', `/v1/tasks/${quoteReference(reference)}/files`, {
      query: { path, mode },
      rawBody: data,
      mediaType: OCTET_STREAM_MEDIA_TYPE,
    });
    return null;
  }

  /**
   * GET /v1/snapshots: one page of a task's snapshots.
   *
   * `task` is required by the API; it is optional here only so the flat route can
   * be called without rebuilding the task path. Omitting it makes the server
   * answer 400 invalid with `details.parameter` "task".
   */
  snapshotsList({ task, limit, cursor } = {}) {
    return this.#client._request('GET', '/v1/snapshots', { query: { task, limit, cursor } });
  }

  /**
   * POST /v1/tasks/{ref}/snapshots: snapshot the task's VM.
   *
   * With neither `vcpus` nor `memoryMB` the request has no body and the node
   * snapshots the VM with the resources it is running with.
   */
  snapshotsCreate(reference, { vcpus, memoryMB } = {}) {
    const body = {};
    if (vcpus !== undefined && vcpus !== null) {
      body.vcpus = vcpus;
    }
    if (memoryMB !== undefined && memoryMB !== null) {
      body.memoryMB = memoryMB;
    }
    return this.#client._request('POST', `/v1/tasks/${quoteReference(reference)}/snapshots`, {
      body: Object.keys(body).length === 0 ? undefined : body,
    });
  }

  /** POST /v1/snapshots/{sid}/restore: restore one of a task's snapshots. */
  snapshotsRestore(reference, snapshot) {
    return this.#client._request('POST', `/v1/snapshots/${quoteReference(snapshot)}/restore`, {
      query: { task: reference },
    });
  }

  /** DELETE /v1/snapshots/{sid}: delete one of a task's snapshots. */
  async snapshotsDelete(reference, snapshot) {
    await this.#client._request('DELETE', `/v1/snapshots/${quoteReference(snapshot)}`, {
      query: { task: reference },
    });
    return null;
  }

  /**
   * DELETE /v1/tasks/{ref}/restored: stop the task's restored VM.
   *
   * A restore starts a Firecracker process outside SwarmKit's task lifecycle, so
   * stopping the task does not stop it; this route does.
   */
  async snapshotsStopRestored(reference) {
    await this.#client._request('DELETE', `/v1/tasks/${quoteReference(reference)}/restored`);
    return null;
  }
}

/** The /v1/events stream. */
export class Events {
  #client;

  constructor(client) {
    this.#client = client;
  }

  /** GET /v1/events: stream change events as server-sent events. */
  watch({ types, since, signal } = {}) {
    return new EventStream(this.#client, { types, since, signal });
  }
}

function flag(value) {
  if (value === undefined || value === null) {
    return undefined;
  }
  return value ? 'true' : 'false';
}

// -- streams -----------------------------------------------------------------

/**
 * The NDJSON frames of POST /v1/tasks/{ref}/exec.
 *
 * Iterate it with `for await`. `exitCode` is the exit frame's status and throws
 * ApiError with code `unreachable` when the stream ended without one. An
 * `error` frame throws ApiError with the frame's code.
 */
export class ExecStream {
  #client;
  #reference;
  #command;
  #options;
  #exitCode = null;
  #response = null;
  #closed = false;

  constructor(client, reference, command, options = {}) {
    this.#client = client;
    this.#reference = reference;
    this.#command = command;
    this.#options = options;
  }

  /** The terminal exit status, or ApiError if the stream never sent one. */
  get exitCode() {
    if (this.#exitCode === null) {
      throw new ApiError('unreachable', 'the exec stream ended without an exit frame');
    }
    return this.#exitCode;
  }

  async *[Symbol.asyncIterator]() {
    this.#exitCode = null;
    if (this.#closed || this.#options.signal?.aborted) {
      return;
    }

    const body = { command: [...this.#command] };
    if (this.#options.env) {
      body.env = { ...this.#options.env };
    }
    if (this.#options.workdir !== undefined && this.#options.workdir !== null) {
      body.workdir = this.#options.workdir;
    }

    const { response } = await this.#client._openStream(
      'POST',
      `/v1/tasks/${quoteReference(this.#reference)}/exec`,
      { body, accept: NDJSON_MEDIA_TYPE, signal: this.#options.signal },
    );
    const status = response.statusCode ?? 0;
    this.#response = response;

    const signal = this.#options.signal;
    const stop = () => response.destroy();
    signal?.addEventListener('abort', stop);
    if (signal?.aborted) {
      stop();
    }

    try {
      for await (const frame of decodeFrames(response, status)) {
        if (signal?.aborted) {
          return;
        }
        if (!EXEC_FRAME_TYPES.has(frame.type)) {
          throw new ApiError(
            'internal',
            `the exec stream carried an unknown frame type ${JSON.stringify(frame.type)}`,
            { status },
          );
        }
        if (frame.type === 'error') {
          const code = frame.code || 'internal';
          throw new ApiError(code, `the exec stream reported an error: ${code}`, { status });
        }
        if (frame.type === 'exit') {
          const raw = frame.exitCode;
          if (raw === undefined || raw === null) {
            this.#exitCode = 0;
          } else {
            const code = Number(raw);
            if (!Number.isInteger(code)) {
              throw new ApiError(
                'internal',
                'the exec stream carried an exit frame with a malformed exit code',
                { status },
              );
            }
            this.#exitCode = code;
          }
        }
        yield frame;
      }
    } catch (error) {
      if (this.#closed || signal?.aborted) {
        return;
      }
      if (error instanceof ApiError) {
        throw error;
      }
      throw transportFailure(error);
    } finally {
      signal?.removeEventListener('abort', stop);
      if (this.#response === response) {
        this.#response = null;
      }
      response.destroy();
    }
  }

  /** Stop the stream and release the connection; iteration then ends without throwing. */
  close() {
    this.#closed = true;
    this.#response?.destroy();
    this.#response = null;
  }
}

/**
 * The NDJSON frames of GET /v1/tasks/{ref}/logs.
 *
 * Frames carry `type` (`log` or `eof`), base64-decoded `data`, `time` (RFC 3339)
 * and `stream`.
 */
export class LogStream {
  #client;
  #reference;
  #options;
  #response = null;
  #closed = false;

  constructor(client, reference, options = {}) {
    this.#client = client;
    this.#reference = reference;
    this.#options = options;
  }

  async *[Symbol.asyncIterator]() {
    const { follow, tail, since, signal } = this.#options;
    if (this.#closed || signal?.aborted) {
      return;
    }
    const query = {
      follow: follow ? 'true' : undefined,
      tail,
      since,
    };
    const { response } = await this.#client._openStream(
      'GET',
      `/v1/tasks/${quoteReference(this.#reference)}/logs`,
      { query, accept: NDJSON_MEDIA_TYPE, signal },
    );
    const status = response.statusCode ?? 0;
    this.#response = response;

    const stop = () => response.destroy();
    signal?.addEventListener('abort', stop);
    if (signal?.aborted) {
      stop();
    }

    try {
      for await (const frame of decodeFrames(response, status)) {
        if (signal?.aborted) {
          return;
        }
        yield frame;
      }
    } catch (error) {
      if (this.#closed || signal?.aborted) {
        return;
      }
      if (error instanceof ApiError) {
        throw error;
      }
      throw transportFailure(error);
    } finally {
      signal?.removeEventListener('abort', stop);
      if (this.#response === response) {
        this.#response = null;
      }
      response.destroy();
    }
  }

  /** Stop the stream and release the connection; iteration then ends without throwing. */
  close() {
    this.#closed = true;
    this.#response?.destroy();
    this.#response = null;
  }
}

/**
 * The server-sent events of GET /v1/events.
 *
 * Yields the decoded Event documents, skips the `ready` frame, stops at
 * `event: end` and throws ApiError for an `event: error` frame. Passing
 * `{signal}` stops the stream when the signal aborts.
 */
export class EventStream {
  #client;
  #options;
  #response = null;
  #closed = false;

  constructor(client, options = {}) {
    this.#client = client;
    this.#options = options;
  }

  async *[Symbol.asyncIterator]() {
    const { types, since, signal } = this.#options;
    if (this.#closed || signal?.aborted) {
      return;
    }
    const query = {};
    if (types && types.length > 0) {
      query.types = [...types].join(',');
    }
    const headers = {};
    if (since !== undefined && since !== null) {
      headers['Last-Event-ID'] = String(since);
    }

    const { response } = await this.#client._openStream('GET', '/v1/events', {
      query,
      headers,
      accept: SSE_MEDIA_TYPE,
      signal,
    });
    const status = response.statusCode ?? 0;
    this.#response = response;

    const stop = () => response.destroy();
    signal?.addEventListener('abort', stop);
    if (signal?.aborted) {
      stop();
    }

    try {
      let name = '';
      let data = [];
      for await (const line of readLines(response)) {
        if (signal?.aborted) {
          return;
        }
        const text = line.endsWith('\r') ? line.slice(0, -1) : line;
        if (text === '') {
          const payload = data.join('\n');
          if (name === 'end') {
            return;
          }
          if (name === 'error' && payload !== '') {
            throw sseError(payload, status);
          }
          if (payload !== '' && name !== '' && name !== 'ready') {
            yield sseEvent(payload, status);
          }
          name = '';
          data = [];
          continue;
        }
        if (text.startsWith(':')) {
          continue;
        }
        const index = text.indexOf(':');
        let field = text;
        let value = '';
        if (index >= 0) {
          field = text.slice(0, index);
          value = text.slice(index + 1);
          if (value.startsWith(' ')) {
            value = value.slice(1);
          }
        }
        if (field === 'event') {
          name = value;
        } else if (field === 'data') {
          data.push(value);
        }
      }
    } catch (error) {
      if (this.#closed || signal?.aborted) {
        return;
      }
      if (error instanceof ApiError) {
        throw error;
      }
      throw transportFailure(error);
    } finally {
      signal?.removeEventListener('abort', stop);
      if (this.#response === response) {
        this.#response = null;
      }
      response.destroy();
    }
  }

  /** Stop the stream and release the connection; iteration then ends without throwing. */
  close() {
    this.#closed = true;
    this.#response?.destroy();
    this.#response = null;
  }
}
