// Behavioural tests for the SwarmCracker Node client.
//
// Everything runs against a node:http server on 127.0.0.1, so the suite needs
// no network and no third-party package.

import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  API_VERSION_HEADER,
  ApiError,
  Client,
  MERGE_PATCH_MEDIA_TYPE,
  NDJSON_MEDIA_TYPE,
  OCTET_STREAM_MEDIA_TYPE,
  SSE_MEDIA_TYPE,
  V1_MEDIA_TYPE,
} from '../src/index.mjs';

const TOKEN = 'test-token';
const REQUEST_ID = 'req_0123456789ab';

const SERVICE = {
  schemaVersion: 1,
  kind: 'Service',
  id: 's1',
  name: 'web',
  revision: '3',
  labels: { app: 'web' },
  spec: { mode: 'replicated', replicas: 1, task: { image: 'nginx' } },
  status: { desiredTasks: 1, runningTasks: 1, tasksInTransition: 0, converged: true },
};

const TASK = {
  schemaVersion: 1,
  kind: 'Task',
  id: 't1',
  revision: '7',
  serviceId: 's1',
  state: 'running',
  desiredState: 'running',
};

// Exec streams that violate the frame contract, keyed by task reference.
const EXEC_FAULTS = {
  t4: [{ data: Buffer.from('x').toString('base64') }],
  t5: [{ type: 'bogus' }],
  t6: [{ type: 'stdout', data: 42 }],
  t7: [{ type: 'stdout', data: '%%% not base64 %%%' }],
};

function errorDocument(code, message, { retryable = false, details } = {}) {
  return {
    schemaVersion: 1,
    kind: 'Error',
    error: { code, message, requestId: REQUEST_ID, retryable, details },
  };
}

function event(cursor, type, resourceId) {
  return {
    schemaVersion: 1,
    kind: 'Event',
    cursor,
    at: '2026-10-25T10:00:00Z',
    type,
    resource: { kind: 'Service', id: resourceId },
  };
}

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const SNAPSHOT = {
  id: 'snap-abc',
  taskId: 't1',
  createdAt: '2026-10-25T10:00:00Z',
  sizeBytes: 1024,
  memoryMB: 256,
  vcpuCount: 2,
  rootfsPath: '/var/lib/firecracker/rootfs/t1.ext4',
};

const SNAPSHOT_DOCUMENT = { ...SNAPSHOT, schemaVersion: 1, kind: 'Snapshot' };

// One NUL and one non-UTF-8 byte prove the client never decodes the body.
const FILE_BYTES = Buffer.from('hello file\u0000\u00ff\n', 'latin1');

// -- test manager -----------------------------------------------------------

function record(state, req, rawBody) {
  const url = new URL(req.url, 'http://127.0.0.1');
  const query = {};
  for (const [key, value] of url.searchParams) {
    (query[key] ??= []).push(value);
  }
  const contentType = req.headers['content-type'] ?? '';
  const isJson = contentType.startsWith(V1_MEDIA_TYPE) || contentType.endsWith('+json');
  const entry = {
    method: req.method,
    path: url.pathname,
    rawQuery: url.search.replace(/^\?/, ''),
    query,
    headers: req.headers,
    rawBody,
    body: rawBody.length > 0 && isJson ? JSON.parse(rawBody.toString('utf8')) : null,
  };
  state.requests.push(entry);
  return { entry, url };
}

function bump(state, key) {
  state.counters[key] = (state.counters[key] ?? 0) + 1;
  return state.counters[key];
}

function sendJson(res, status, document, extraHeaders = {}) {
  const body = Buffer.from(JSON.stringify(document), 'utf8');
  res.writeHead(status, {
    'Content-Type': V1_MEDIA_TYPE,
    'Content-Length': String(body.length),
    [API_VERSION_HEADER]: '1',
    'X-Request-Id': REQUEST_ID,
    ...extraHeaders,
  });
  res.end(body);
}

function sendText(res, status, text, contentType) {
  sendBytes(res, status, Buffer.from(text, 'utf8'), contentType);
}

function sendBytes(res, status, body, contentType) {
  res.writeHead(status, {
    'Content-Type': contentType,
    'Content-Length': String(body.length),
    [API_VERSION_HEADER]: '1',
    'X-Request-Id': REQUEST_ID,
  });
  res.end(body);
}

function sendEmpty(res, status) {
  res.writeHead(status, {
    [API_VERSION_HEADER]: '1',
    'X-Request-Id': REQUEST_ID,
  });
  res.end();
}

function startStream(res, contentType) {
  res.writeHead(200, {
    'Content-Type': contentType,
    [API_VERSION_HEADER]: '1',
    'X-Request-Id': REQUEST_ID,
  });
}

function writeFrames(res, frames) {
  for (const frame of frames) {
    res.write(`${JSON.stringify(frame)}\n`);
  }
  res.end();
}

// A stream that keeps producing after the client stops reading, so break and
// close() land mid-stream. The client destroys the socket, so a stray write or
// the end of the response must not crash the fixture.
async function writeSlowFrames(res, contentType, first, second, last) {
  res.on('error', () => {});
  startStream(res, contentType);
  res.write(`${JSON.stringify(first)}\n`);
  await delay(300);
  res.write(`${JSON.stringify(second)}\n`);
  res.end(`${JSON.stringify(last)}\n`);
}

// One exec frame split mid-JSON across three writes, with a three-byte UTF-8
// character whose bytes straddle the two chunk boundaries, then an exit frame.
async function writeSplitExecStream(res) {
  res.on('error', () => {});
  startStream(res, NDJSON_MEDIA_TYPE);
  const stdout = {
    type: 'stdout',
    data: Buffer.from('héllo ✓ wörld\n', 'utf8').toString('base64'),
    note: 'π–✓',
  };
  const bytes = Buffer.from(
    `${JSON.stringify(stdout)}\n${JSON.stringify({ type: 'exit', exitCode: 0 })}\n`,
    'utf8',
  );
  // '✓' is three bytes; cut after its first and second byte, so no single chunk
  // holds the whole character.
  const index = bytes.indexOf(Buffer.from('✓', 'utf8'));
  assert.notEqual(index, -1, 'the fixture lost its multi-byte marker');
  const chunks = [bytes.subarray(0, index + 1), bytes.subarray(index + 1, index + 2), bytes.subarray(index + 2)];
  for (const chunk of chunks) {
    res.write(chunk);
    await delay(10);
  }
  res.end();
}

// Drop the TCP connection after one frame, the mid-stream transport failure.
function dropStream(res, contentType, first) {
  res.on('error', () => {});
  startStream(res, contentType);
  res.write(`${JSON.stringify(first)}\n`);
  setTimeout(() => res.destroy(), 30);
}

function execFrames(reference) {
  if (reference === 't2') {
    return [{ type: 'stdout', data: Buffer.from('partial\n').toString('base64') }];
  }
  if (reference === 't3') {
    return [{ type: 'error', code: 'timeout' }];
  }
  if (EXEC_FAULTS[reference]) {
    return EXEC_FAULTS[reference];
  }
  return [
    { type: 'stdout', data: Buffer.from('hello\n').toString('base64') },
    { type: 'stderr', data: Buffer.from('oops\n').toString('base64') },
    { type: 'exit', exitCode: 3 },
  ];
}

async function route(state, req, res, { entry, url }) {
  const { method } = req;
  const path = url.pathname;

  if (method === 'GET' && path === '/v1/version') {
    sendJson(res, 200, {
      schemaVersion: 1,
      kind: 'VersionInfo',
      apiVersions: ['1'],
      serverVersion: 'v0.11.0',
      grpcProtocolVersion: '1',
      capabilities: ['broker', 'snapshots'],
    });
    return true;
  }
  if (method === 'GET' && path === '/v1/services') {
    if (url.searchParams.get('cursor') === 'redirect') {
      res.writeHead(302, { Location: state.redirectTo, 'Content-Length': '0' });
      res.end();
      return true;
    }
    if (url.searchParams.get('cursor') === 'slow') {
      // Outlives the client's deadline on purpose.
      await delay(400);
    }
    sendJson(res, 200, { schemaVersion: 1, kind: 'ServiceList', items: [SERVICE], next: null });
    return true;
  }
  if (method === 'POST' && path === '/v1/services') {
    if (entry.headers['content-type'] !== V1_MEDIA_TYPE) {
      sendJson(res, 415, errorDocument('invalid', 'unsupported media type'));
      return true;
    }
    const name = entry.body?.name;
    if (name === 'flaky-no-key') {
      sendJson(res, 503, errorDocument('unavailable', 'try later', { retryable: true }));
      return true;
    }
    if (name === 'flaky' && entry.headers['idempotency-key']) {
      if (bump(state, 'services:flaky') <= 2) {
        sendJson(res, 503, errorDocument('unavailable', 'try later', { retryable: true }));
        return true;
      }
    }
    sendJson(res, 201, { ...SERVICE, id: 's2', name });
    return true;
  }
  if (path.startsWith('/v1/services/')) {
    const reference = decodeURIComponent(path.slice('/v1/services/'.length));
    if (method === 'GET') {
      if (reference === 'forbidden') {
        sendJson(
          res,
          403,
          errorDocument('forbidden', 'the token lacks services:read', {
            details: { requiredScope: 'services:read' },
          }),
        );
        return true;
      }
      if (reference === 'undecodable') {
        sendText(res, 500, '<html>boom</html>', 'text/html');
        return true;
      }
      if (reference === 'redirect') {
        res.writeHead(302, { Location: state.redirectTo, 'Content-Length': '0' });
        res.end();
        return true;
      }
      sendJson(res, 200, { ...SERVICE, id: reference });
      return true;
    }
    if (method === 'PATCH') {
      if (entry.headers['content-type'] !== MERGE_PATCH_MEDIA_TYPE) {
        sendJson(res, 415, errorDocument('invalid', 'unsupported media type'));
        return true;
      }
      if (!entry.headers['if-match']) {
        sendJson(res, 428, errorDocument('invalid', 'If-Match is required'));
        return true;
      }
      sendJson(res, 200, {
        ...SERVICE,
        id: reference,
        labels: entry.body.labels ?? SERVICE.labels,
      });
      return true;
    }
    if (method === 'DELETE') {
      // If-Match is optional on DELETE: the API rejects only a stale value.
      res.writeHead(204, { 'X-Request-Id': REQUEST_ID });
      res.end();
      return true;
    }
    return false;
  }
  if (method === 'GET' && path === '/v1/tasks') {
    sendJson(res, 200, { schemaVersion: 1, kind: 'TaskList', items: [TASK], next: 'c2' });
    return true;
  }
  if (method === 'GET' && path === '/v1/tasks/t1') {
    const document = { ...TASK };
    if (url.searchParams.get('health') === 'true') {
      document.health = { status: 'healthy' };
    }
    sendJson(res, 200, document);
    return true;
  }
  if (method === 'POST' && path.endsWith('/exec')) {
    const reference = decodeURIComponent(path.split('/')[3]);
    if (entry.headers['content-type'] !== V1_MEDIA_TYPE) {
      sendJson(res, 415, errorDocument('invalid', 'unsupported media type'));
      return true;
    }
    if (reference === 't8') {
      await writeSplitExecStream(res);
      return true;
    }
    if (reference === 't9') {
      await writeSlowFrames(
        res,
        NDJSON_MEDIA_TYPE,
        { type: 'stdout', data: Buffer.from('first\n').toString('base64') },
        { type: 'stdout', data: Buffer.from('second\n').toString('base64') },
        { type: 'exit', exitCode: 0 },
      );
      return true;
    }
    if (reference === 't10') {
      dropStream(res, NDJSON_MEDIA_TYPE, {
        type: 'stdout',
        data: Buffer.from('before the drop\n').toString('base64'),
      });
      return true;
    }
    if (reference === 't11') {
      startStream(res, NDJSON_MEDIA_TYPE);
      writeFrames(res, [{ type: 'exit', exitCode: 'not-a-number' }]);
      return true;
    }
    startStream(res, NDJSON_MEDIA_TYPE);
    writeFrames(res, execFrames(reference));
    return true;
  }
  if (method === 'GET' && path === '/v1/tasks/t1/logs') {
    startStream(res, NDJSON_MEDIA_TYPE);
    writeFrames(res, [
      {
        type: 'log',
        stream: 'stdout',
        time: '2026-10-25T10:00:00.5Z',
        data: Buffer.from('line one\n').toString('base64'),
      },
      {
        type: 'log',
        stream: 'stdout',
        time: '2026-10-25T10:00:01Z',
        data: Buffer.from('line two\n').toString('base64'),
      },
      { type: 'eof' },
    ]);
    return true;
  }
  if (method === 'GET' && path === '/v1/tasks/t1/files') {
    const guestPath = url.searchParams.get('path') ?? '';
    if (guestPath === '/missing') {
      sendJson(res, 404, errorDocument('not_found', 'no such file'));
      return true;
    }
    sendBytes(res, 200, FILE_BYTES, OCTET_STREAM_MEDIA_TYPE);
    return true;
  }
  if (method === 'PUT' && path === '/v1/tasks/t1/files') {
    if (entry.headers['content-type'] !== OCTET_STREAM_MEDIA_TYPE) {
      sendJson(res, 415, errorDocument('invalid', 'unsupported media type'));
      return true;
    }
    const guestPath = url.searchParams.get('path');
    if (guestPath === '/missing') {
      sendJson(res, 404, errorDocument('not_found', 'no such directory'));
      return true;
    }
    sendEmpty(res, 204);
    return true;
  }
  if (method === 'GET' && path === '/v1/snapshots') {
    if (!url.searchParams.has('task')) {
      sendJson(res, 400, errorDocument('invalid', 'task is required', { details: { parameter: 'task' } }));
      return true;
    }
    sendJson(res, 200, {
      schemaVersion: 1,
      kind: 'SnapshotList',
      items: [SNAPSHOT],
      next: null,
    });
    return true;
  }
  if (method === 'POST' && path === '/v1/tasks/t1/snapshots') {
    const body = entry.body ?? {};
    const document = { ...SNAPSHOT_DOCUMENT };
    if (body.vcpus !== undefined) {
      document.vcpuCount = body.vcpus;
    }
    if (body.memoryMB !== undefined) {
      document.memoryMB = body.memoryMB;
    }
    sendJson(res, 201, document);
    return true;
  }
  if (method === 'POST' && path === '/v1/snapshots/snap-abc/restore') {
    if (url.searchParams.get('task') !== 't1') {
      sendJson(res, 400, errorDocument('invalid', 'task is required'));
      return true;
    }
    sendJson(res, 200, SNAPSHOT_DOCUMENT);
    return true;
  }
  if (method === 'DELETE' && path === '/v1/snapshots/snap-abc') {
    if (url.searchParams.get('task') !== 't1') {
      sendJson(res, 400, errorDocument('invalid', 'task is required'));
      return true;
    }
    sendEmpty(res, 204);
    return true;
  }
  if (method === 'DELETE' && path === '/v1/tasks/t1/restored') {
    sendEmpty(res, 204);
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/twice') {
    if (bump(state, path) <= 2) {
      sendJson(res, 503, errorDocument('unavailable', 'try later', { retryable: true }));
      return true;
    }
    sendJson(res, 200, { schemaVersion: 1, kind: 'VersionInfo', serverVersion: 'v0.11.0' });
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/always') {
    bump(state, path);
    sendJson(res, 503, errorDocument('unavailable', 'try later', { retryable: true }));
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/after-seconds') {
    if (bump(state, path) === 1) {
      sendJson(res, 429, errorDocument('rate_limited', 'slow down', { retryable: true }), {
        'Retry-After': '7',
      });
      return true;
    }
    sendJson(res, 200, { schemaVersion: 1, kind: 'VersionInfo', serverVersion: 'v0.11.0' });
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/after-date') {
    if (bump(state, path) === 1) {
      sendJson(res, 503, errorDocument('unavailable', 'try later', { retryable: true }), {
        'Retry-After': new Date(Date.now() + 3000).toUTCString(),
      });
      return true;
    }
    sendJson(res, 200, { schemaVersion: 1, kind: 'VersionInfo', serverVersion: 'v0.11.0' });
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/capped') {
    if (bump(state, path) === 1) {
      sendJson(res, 429, errorDocument('rate_limited', 'slow down', { retryable: true }), {
        'Retry-After': '600',
      });
      return true;
    }
    sendJson(res, 200, { schemaVersion: 1, kind: 'VersionInfo', serverVersion: 'v0.11.0' });
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/after-hint') {
    // The hint comes from the test, so one route covers a negative, a zero and a
    // garbage Retry-After value.
    if (bump(state, path) === 1) {
      sendJson(res, 429, errorDocument('rate_limited', 'slow down', { retryable: true }), {
        'Retry-After': state.retryHint,
      });
      return true;
    }
    sendJson(res, 200, { schemaVersion: 1, kind: 'VersionInfo', serverVersion: 'v0.11.0' });
    return true;
  }
  if (method === 'GET' && path === '/v1/retry/bad-request') {
    bump(state, path);
    sendJson(res, 400, errorDocument('invalid', 'no'));
    return true;
  }
  if (method === 'GET' && path === '/v1/tasks/t2/logs') {
    // A stream that outlives the connect deadline must not be cut.
    startStream(res, NDJSON_MEDIA_TYPE);
    res.write(`${JSON.stringify({ type: 'log', stream: 'stdout', data: Buffer.from('a\n').toString('base64') })}\n`);
    await delay(300);
    res.write(`${JSON.stringify({ type: 'log', stream: 'stdout', data: Buffer.from('b\n').toString('base64') })}\n`);
    res.end(`${JSON.stringify({ type: 'eof' })}\n`);
    return true;
  }
  if (method === 'GET' && path === '/v1/events') {
    if (url.searchParams.get('types') === 'flaky' && bump(state, 'events:flaky') === 1) {
      // A retryable 503 before any event: a stream must not replay it.
      sendJson(res, 503, errorDocument('unavailable', 'try later', { retryable: true }));
      return true;
    }
    startStream(res, SSE_MEDIA_TYPE);
    if (url.searchParams.get('types') === 'slow') {
      res.on('error', () => {});
      res.write('event: ready\ndata: {"type":"ready"}\n\n');
      res.write(`id: 9\nevent: service.created\ndata: ${JSON.stringify(event('9', 'service.created', 's1'))}\n\n`);
      await delay(300);
      res.end('event: end\ndata: {}\n\n');
      return true;
    }
    if (url.searchParams.get('types') === 'drop') {
      res.on('error', () => {});
      res.write('event: ready\ndata: {"type":"ready"}\n\n');
      res.write(`id: 9\nevent: service.created\ndata: ${JSON.stringify(event('9', 'service.created', 's1'))}\n\n`);
      setTimeout(() => res.destroy(), 30);
      return true;
    }
    if (url.searchParams.get('types') === 'error') {
      res.write('event: ready\ndata: {}\n\n');
      res.write(`event: error\ndata: ${JSON.stringify(errorDocument('unavailable', 'the broker is down', { retryable: true }))}\n\n`);
      res.end();
      return true;
    }
    res.write('event: ready\ndata: {"type":"ready"}\n\n');
    res.write(': ping\n\n');
    res.write(`id: 7\nevent: service.created\ndata: ${JSON.stringify(event('7', 'service.created', 's1'))}\n\n`);
    res.write(`id: 8\nevent: task.updated\ndata: ${JSON.stringify(event('8', 'task.updated', 't1'))}\n\n`);
    res.write('event: end\ndata: {}\n\n');
    res.end();
    return true;
  }
  return false;
}

async function withManager(run) {
  const state = { requests: [], redirectTo: '', counters: {}, retryHint: '7' };
  const server = createServer((req, res) => {
    const chunks = [];
    req.on('data', (chunk) => chunks.push(chunk));
    req.on('end', async () => {
      const rawBody = Buffer.concat(chunks);
      const parsed = record(state, req, rawBody);
      if (req.headers.authorization !== `Bearer ${TOKEN}`) {
        sendJson(res, 401, errorDocument('unauthenticated', 'missing bearer token'));
        return;
      }
      let handled = false;
      try {
        handled = await route(state, req, res, parsed);
      } catch (error) {
        sendJson(res, 500, errorDocument('internal', String(error)));
        handled = true;
      }
      if (!handled) {
        sendJson(res, 404, errorDocument('not_found', 'no such route'));
      }
    });
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const host = `http://127.0.0.1:${server.address().port}`;

  const ctx = { host, state, lastRequest: lastRequest.bind(null, state) };
  ctx.client = new Client({ host, token: TOKEN });
  try {
    await run(ctx);
  } finally {
    ctx.client.close();
    await new Promise((resolve) => server.close(resolve));
  }
}

function lastRequest(state, method = 'GET', path = null) {
  const matches = state.requests.filter(
    (entry) => entry.method === method && (path === null || entry.path === path),
  );
  assert.ok(matches.length > 0, `no ${method} request recorded: ${JSON.stringify(state.requests)}`);
  return matches[matches.length - 1];
}

// -- version and envelope ---------------------------------------------------

test('version sends the envelope headers and decodes the document', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const info = await client.version();
    assert.equal(info.serverVersion, 'v0.11.0');
    assert.deepEqual(info.apiVersions, ['1']);

    const request = lastRequest('GET', '/v1/version');
    assert.equal(request.headers.authorization, `Bearer ${TOKEN}`);
    assert.equal(request.headers.accept, V1_MEDIA_TYPE);
    assert.equal(request.headers[API_VERSION_HEADER.toLowerCase()], '1');
    assert.equal(request.headers['content-type'], undefined);
  });
});

test('close is idempotent', async () => {
  await withManager(async ({ client }) => {
    assert.equal((await client.version()).kind, 'VersionInfo');
    client.close();
    client.close();
  });
});

// -- services ---------------------------------------------------------------

test('services.list encodes label, limit and cursor', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const page = await client.services.list({ label: 'app=web', limit: 5, cursor: 'abc/def' });
    assert.equal(page.kind, 'ServiceList');
    assert.equal(page.items[0].id, 's1');

    const request = lastRequest('GET', '/v1/services');
    assert.deepEqual(request.query, {
      label: ['app=web'],
      limit: ['5'],
      cursor: ['abc/def'],
    });
  });
});

test('services.list repeats a label sequence', async () => {
  await withManager(async ({ client, lastRequest }) => {
    await client.services.list({ label: ['app=web', 'tier=front'] });
    assert.deepEqual(lastRequest('GET', '/v1/services').query.label, ['app=web', 'tier=front']);
  });
});

test('services.get percent-encodes the reference', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const document = await client.services.get('web/one');
    assert.equal(document.id, 'web/one');
    assert.equal(lastRequest('GET').path, '/v1/services/web%2Fone');
  });
});

test('services.create sends the media type and the idempotency key', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const body = {
      name: 'api',
      spec: { mode: 'replicated', replicas: 1, task: { image: 'api:1' } },
    };
    const document = await client.services.create(body, { idempotencyKey: 'create-1' });
    assert.equal(document.name, 'api');

    const request = lastRequest('POST', '/v1/services');
    assert.equal(request.headers['content-type'], V1_MEDIA_TYPE);
    assert.equal(request.headers['idempotency-key'], 'create-1');
    assert.deepEqual(request.body, body);
  });
});

test('services.patch sends a merge patch, If-Match and wait', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const document = await client.services.patch('s1', { labels: { app: null } }, {
      ifMatch: '3',
      wait: 'converged',
    });
    assert.deepEqual(document.labels, { app: null });

    const request = lastRequest('PATCH', '/v1/services/s1');
    assert.equal(request.headers['content-type'], MERGE_PATCH_MEDIA_TYPE);
    assert.equal(request.headers['if-match'], '3');
    assert.deepEqual(request.query, { wait: ['converged'] });
  });
});

test('services.delete sends If-Match and returns null on 204', async () => {
  await withManager(async ({ client, lastRequest }) => {
    assert.equal(await client.services.delete('s1', { ifMatch: '3' }), null);
    assert.equal(lastRequest('DELETE', '/v1/services/s1').headers['if-match'], '3');
  });
});

test('services.delete omits the header when ifMatch is absent', async () => {
  await withManager(async ({ client, lastRequest }) => {
    assert.equal(await client.services.delete('s1'), null);
    assert.equal(lastRequest('DELETE', '/v1/services/s1').headers['if-match'], undefined);
  });
});

// -- tasks ------------------------------------------------------------------

test('tasks.list sends health=true and pagination', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const page = await client.tasks.list({ limit: 2, cursor: 'c1', health: true });
    assert.equal(page.kind, 'TaskList');
    assert.equal(page.next, 'c2');
    assert.deepEqual(lastRequest('GET', '/v1/tasks').query, {
      limit: ['2'],
      cursor: ['c1'],
      health: ['true'],
    });
  });
});

test('tasks.list omits absent parameters', async () => {
  await withManager(async ({ client, lastRequest }) => {
    await client.tasks.list();
    assert.equal(lastRequest('GET', '/v1/tasks').rawQuery, '');
  });
});

test('tasks.get sends health=false', async () => {
  await withManager(async ({ client, lastRequest }) => {
    await client.tasks.get('t1', { health: false });
    assert.deepEqual(lastRequest('GET', '/v1/tasks/t1').query, { health: ['false'] });
  });
});

// -- files ------------------------------------------------------------------

test('filesGet returns the raw bytes and sends the path', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const data = await client.tasks.filesGet('t1', '/etc/hosts');
    assert.ok(data instanceof Uint8Array);
    assert.deepEqual(Buffer.from(data), FILE_BYTES);

    const request = lastRequest('GET', '/v1/tasks/t1/files');
    assert.deepEqual(request.query, { path: ['/etc/hosts'] });
    assert.equal(request.headers.accept, OCTET_STREAM_MEDIA_TYPE);
  });
});

test('filesGet maps a typed failure', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(client.tasks.filesGet('t1', '/missing'), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'not_found');
      assert.equal(error.status, 404);
      assert.equal(error.requestId, REQUEST_ID);
      assert.doesNotMatch(String(error), new RegExp(TOKEN));
      return true;
    });
  });
});

test('filesPut sends raw bytes, the content type and the mode', async () => {
  await withManager(async ({ client, lastRequest }) => {
    assert.equal(await client.tasks.filesPut('t1', '/tmp/payload', FILE_BYTES, { mode: '0600' }), null);

    const request = lastRequest('PUT', '/v1/tasks/t1/files');
    assert.equal(request.headers['content-type'], OCTET_STREAM_MEDIA_TYPE);
    assert.deepEqual(request.query, { path: ['/tmp/payload'], mode: ['0600'] });
    assert.deepEqual(request.rawBody, FILE_BYTES);
  });
});

test('filesPut omits the mode and accepts an empty file', async () => {
  await withManager(async ({ client, lastRequest }) => {
    assert.equal(await client.tasks.filesPut('t1', '/tmp/empty', new Uint8Array(0)), null);
    const request = lastRequest('PUT', '/v1/tasks/t1/files');
    assert.deepEqual(request.query, { path: ['/tmp/empty'] });
    assert.equal(request.rawBody.length, 0);
    assert.equal(request.headers['content-type'], OCTET_STREAM_MEDIA_TYPE);
  });
});

test('filesPut rejects a non-bytes body', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(client.tasks.filesPut('t1', '/tmp/text', 'not bytes'), TypeError);
  });
});

// -- snapshots --------------------------------------------------------------

test('snapshotsList sends the task and pagination', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const page = await client.tasks.snapshotsList({ task: 't1', limit: 10, cursor: 'c1' });
    assert.equal(page.kind, 'SnapshotList');
    assert.equal(page.items[0].id, 'snap-abc');
    assert.deepEqual(lastRequest('GET', '/v1/snapshots').query, {
      task: ['t1'],
      limit: ['10'],
      cursor: ['c1'],
    });
  });
});

test('snapshotsList without a task is a typed invalid', async () => {
  await withManager(async ({ client, lastRequest }) => {
    await assert.rejects(client.tasks.snapshotsList(), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'invalid');
      assert.deepEqual(error.details, { parameter: 'task' });
      return true;
    });
    assert.equal(lastRequest('GET', '/v1/snapshots').rawQuery, '');
  });
});

test('snapshotsCreate sends the optional resources', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const document = await client.tasks.snapshotsCreate('t1', { vcpus: 2, memoryMB: 256 });
    assert.equal(document.kind, 'Snapshot');

    const request = lastRequest('POST', '/v1/tasks/t1/snapshots');
    assert.equal(request.headers['content-type'], V1_MEDIA_TYPE);
    assert.deepEqual(request.body, { vcpus: 2, memoryMB: 256 });
  });
});

test('snapshotsCreate without resources sends no body', async () => {
  await withManager(async ({ client, lastRequest }) => {
    await client.tasks.snapshotsCreate('t1');
    const request = lastRequest('POST', '/v1/tasks/t1/snapshots');
    assert.equal(request.body, null);
    assert.equal(request.headers['content-type'], undefined);
  });
});

test('snapshotsRestore uses the flat route with the task', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const document = await client.tasks.snapshotsRestore('t1', 'snap-abc');
    assert.equal(document.id, 'snap-abc');
    assert.deepEqual(lastRequest('POST', '/v1/snapshots/snap-abc/restore').query, {
      task: ['t1'],
    });
  });
});

test('snapshotsDelete uses the flat route with the task', async () => {
  await withManager(async ({ client, lastRequest }) => {
    assert.equal(await client.tasks.snapshotsDelete('t1', 'snap-abc'), null);
    assert.deepEqual(lastRequest('DELETE', '/v1/snapshots/snap-abc').query, { task: ['t1'] });
  });
});

test('snapshotsStopRestored deletes the restored VM', async () => {
  await withManager(async ({ client, lastRequest }) => {
    assert.equal(await client.tasks.snapshotsStopRestored('t1'), null);
    assert.equal(lastRequest('DELETE', '/v1/tasks/t1/restored').rawQuery, '');
  });
});

// -- exec -------------------------------------------------------------------

test('exec decodes frames and exposes the exit code', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const run = client.tasks.exec('t1', ['sh', '-c', 'hi'], { env: { A: '1' }, workdir: '/tmp' });
    const frames = [];
    for await (const frame of run) {
      frames.push(frame);
    }

    assert.deepEqual(frames.map((frame) => frame.type), ['stdout', 'stderr', 'exit']);
    assert.ok(frames[0].data instanceof Uint8Array);
    assert.deepEqual(Buffer.from(frames[0].data), Buffer.from('hello\n'));
    assert.deepEqual(Buffer.from(frames[1].data), Buffer.from('oops\n'));
    assert.equal(frames[2].data.length, 0);
    assert.equal(run.exitCode, 3);

    const request = lastRequest('POST', '/v1/tasks/t1/exec');
    assert.equal(request.headers.accept, NDJSON_MEDIA_TYPE);
    assert.equal(request.headers['content-type'], V1_MEDIA_TYPE);
    assert.deepEqual(request.body, {
      command: ['sh', '-c', 'hi'],
      env: { A: '1' },
      workdir: '/tmp',
    });
  });
});

test('a truncated exec stream makes exitCode throw unreachable', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t2', ['false']);
    const frames = [];
    for await (const frame of run) {
      frames.push(frame);
    }
    assert.deepEqual(frames.map((frame) => frame.type), ['stdout']);
    assert.throws(
      () => run.exitCode,
      (error) => error instanceof ApiError && error.code === 'unreachable' && error.status === 0,
    );
  });
});

test('an error frame rejects with the frame code', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t3', ['sleep', '100']);
    await assert.rejects(
      async () => {
        for await (const frame of run) {
          assert.fail(`unexpected frame ${frame.type}`);
        }
      },
      (error) => error instanceof ApiError && error.code === 'timeout',
    );
  });
});

test('a frame without a type is internal', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(async () => {
      for await (const frame of client.tasks.exec('t4', ['sh'])) {
        assert.fail(`unexpected frame ${frame.type}`);
      }
    }, (error) => error instanceof ApiError && error.code === 'internal' && /type/.test(error.message));
  });
});

test('a frame with an unknown type is internal', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(async () => {
      for await (const frame of client.tasks.exec('t5', ['sh'])) {
        assert.fail(`unexpected frame ${frame.type}`);
      }
    }, (error) => error instanceof ApiError && error.code === 'internal' && /bogus/.test(error.message));
  });
});

test('a frame with non-string data is internal', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(async () => {
      for await (const frame of client.tasks.exec('t6', ['sh'])) {
        assert.fail(`unexpected frame ${frame.type}`);
      }
    }, (error) => error instanceof ApiError && error.code === 'internal' && /base64/.test(error.message));
  });
});

test('a frame with invalid base64 is internal', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(async () => {
      for await (const frame of client.tasks.exec('t7', ['sh'])) {
        assert.fail(`unexpected frame ${frame.type}`);
      }
    }, (error) => error instanceof ApiError && error.code === 'internal' && /base64/.test(error.message));
  });
});

test('a malformed exit code is internal', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t11', ['sh']);
    await assert.rejects(
      async () => {
        for await (const frame of run) {
          assert.fail(`unexpected frame ${frame.type}`);
        }
      },
      (error) => error instanceof ApiError && error.code === 'internal' && /exit code/.test(error.message),
    );
  });
});

test('an exec frame split across chunks decodes intact', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t8', ['sh', '-c', 'printf héllo']);
    const frames = [];
    for await (const frame of run) {
      frames.push(frame);
    }

    assert.deepEqual(frames.map((frame) => frame.type), ['stdout', 'exit']);
    assert.deepEqual(Buffer.from(frames[0].data).toString('utf8'), 'héllo ✓ wörld\n');
    // The multi-byte text outside `data` is intact too: its bytes straddled the
    // chunk boundaries of the NDJSON stream.
    assert.equal(frames[0].note, 'π–✓');
    assert.equal(run.exitCode, 0);
  });
});

test('breaking out of an exec stream releases it without throwing', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t9', ['sh', '-c', 'slow']);
    const frames = [];
    for await (const frame of run) {
      frames.push(frame);
      break;
    }
    run.close();
    assert.deepEqual(frames.map((frame) => frame.type), ['stdout']);
  });
});

test('close() inside the loop ends an exec stream without throwing', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t9', ['sh', '-c', 'slow']);
    const frames = [];
    for await (const frame of run) {
      frames.push(frame);
      run.close();
    }
    assert.deepEqual(frames.map((frame) => frame.type), ['stdout']);
  });
});

test('a timer calling close() ends an exec stream without throwing', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t9', ['sh', '-c', 'slow']);
    const timer = setTimeout(() => run.close(), 30);
    try {
      await assert.doesNotReject(async () => {
        for await (const frame of run) {
          assert.equal(frame.type, 'stdout');
        }
      });
    } finally {
      clearTimeout(timer);
    }
  });
});

test('a mid-stream transport failure is an unreachable ApiError', async () => {
  await withManager(async ({ client }) => {
    const run = client.tasks.exec('t10', ['sh', '-c', 'drop']);
    const frames = [];
    await assert.rejects(
      async () => {
        for await (const frame of run) {
          frames.push(frame);
        }
      },
      (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.code, 'unreachable');
        assert.equal(error.status, 0);
        assert.doesNotMatch(String(error), new RegExp(TOKEN));
        return true;
      },
    );
    assert.deepEqual(frames.map((frame) => frame.type), ['stdout']);
  });
});

// -- logs -------------------------------------------------------------------

test('logs decode data and keep time and stream', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const frames = [];
    for await (const frame of client.tasks.logs('t1', {
      follow: true,
      tail: 2,
      since: '2026-10-25T09:00:00Z',
    })) {
      frames.push(frame);
    }

    assert.deepEqual(frames.map((frame) => frame.type), ['log', 'log', 'eof']);
    assert.deepEqual(Buffer.from(frames[0].data), Buffer.from('line one\n'));
    assert.equal(frames[0].stream, 'stdout');
    assert.equal(frames[0].time, '2026-10-25T10:00:00.5Z');
    assert.equal(frames[2].data.length, 0);

    const request = lastRequest('GET', '/v1/tasks/t1/logs');
    assert.equal(request.headers.accept, NDJSON_MEDIA_TYPE);
    assert.deepEqual(request.query, {
      follow: ['true'],
      tail: ['2'],
      since: ['2026-10-25T09:00:00Z'],
    });
  });
});

test('a stream outliving the connect deadline is not cut', async () => {
  await withManager(async ({ host }) => {
    const client = new Client({ host, token: TOKEN, timeoutMs: 100 });
    try {
      const frames = [];
      for await (const frame of client.tasks.logs('t2')) {
        frames.push(frame);
      }
      assert.deepEqual(frames.map((frame) => frame.type), ['log', 'log', 'eof']);
    } finally {
      client.close();
    }
  });
});

test('close() inside the loop ends a log stream without throwing', async () => {
  await withManager(async ({ client }) => {
    const stream = client.tasks.logs('t2', { follow: true });
    const frames = [];
    for await (const frame of stream) {
      frames.push(frame);
      stream.close();
    }
    assert.deepEqual(frames.map((frame) => frame.type), ['log']);
  });
});

// -- events -----------------------------------------------------------------

test('events.watch yields events and stops at end', async () => {
  await withManager(async ({ client, lastRequest }) => {
    const events = [];
    for await (const item of client.events.watch({ types: ['service', 'task'] })) {
      events.push(item);
    }

    assert.deepEqual(events.map((item) => item.cursor), ['7', '8']);
    assert.equal(events[0].type, 'service.created');
    assert.deepEqual(events[1].resource, { kind: 'Service', id: 't1' });

    const request = lastRequest('GET', '/v1/events');
    assert.equal(request.headers.accept, SSE_MEDIA_TYPE);
    assert.deepEqual(request.query, { types: ['service,task'] });
    assert.equal(request.headers['last-event-id'], undefined);
  });
});

test('events.watch sends Last-Event-ID when given a cursor', async () => {
  await withManager(async ({ client, lastRequest }) => {
    for await (const _ of client.events.watch({ since: '7' })) {
      // drain
    }
    assert.equal(lastRequest('GET', '/v1/events').headers['last-event-id'], '7');
  });
});

test('events.watch rejects with the error event document', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(async () => {
      for await (const item of client.events.watch({ types: ['error'] })) {
        assert.fail(`unexpected event ${item.type}`);
      }
    }, (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'unavailable');
      assert.equal(error.message, 'the broker is down');
      assert.equal(error.retryable, true);
      assert.equal(error.status, 200);
      assert.equal(error.requestId, REQUEST_ID);
      return true;
    });
  });
});

test('events.watch stops when aborted', async () => {
  await withManager(async ({ client }) => {
    const controller = new AbortController();
    const events = [];
    for await (const item of client.events.watch({ signal: controller.signal })) {
      events.push(item);
      if (events.length === 1) {
        controller.abort();
      }
    }
    assert.deepEqual(events.map((item) => item.cursor), ['7']);
  });
});

test('a pre-aborted signal opens no stream', async () => {
  await withManager(async ({ client, state }) => {
    const controller = new AbortController();
    controller.abort();
    for await (const item of client.events.watch({ signal: controller.signal })) {
      assert.fail(`unexpected event ${item.type}`);
    }
    assert.equal(state.requests.length, 0);
  });
});

test('a timer calling close() ends an event stream without throwing', async () => {
  await withManager(async ({ client }) => {
    const stream = client.events.watch({ types: ['slow'] });
    const timer = setTimeout(() => stream.close(), 30);
    try {
      await assert.doesNotReject(async () => {
        for await (const item of stream) {
          assert.equal(item.type, 'service.created');
        }
      });
    } finally {
      clearTimeout(timer);
    }
  });
});

test('a dropped event stream is an unreachable ApiError', async () => {
  await withManager(async ({ client }) => {
    const events = [];
    await assert.rejects(
      async () => {
        for await (const item of client.events.watch({ types: ['drop'] })) {
          events.push(item);
        }
      },
      (error) => error instanceof ApiError && error.code === 'unreachable' && error.status === 0,
    );
    assert.deepEqual(events.map((item) => item.cursor), ['9']);
  });
});

// -- errors -----------------------------------------------------------------

test('a 403 carries the error document fields', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(client.services.get('forbidden'), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'forbidden');
      assert.equal(error.message, 'the token lacks services:read');
      assert.equal(error.status, 403);
      assert.equal(error.requestId, REQUEST_ID);
      assert.equal(error.retryable, false);
      assert.deepEqual(error.details, { requiredScope: 'services:read' });
      return true;
    });
  });
});

test('an undecodable error body is internal', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(client.services.get('undecodable'), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'internal');
      assert.equal(error.status, 500);
      assert.equal(error.requestId, REQUEST_ID);
      return true;
    });
  });
});

test('redirects are refused so the token is never replayed', async () => {
  const sinkRequests = [];
  const sink = createServer((req, res) => {
    sinkRequests.push(req.headers);
    res.writeHead(200, { 'Content-Type': V1_MEDIA_TYPE });
    res.end('{"ok":true}');
  });
  await new Promise((resolve) => sink.listen(0, '127.0.0.1', resolve));
  try {
    await withManager(async ({ client, state }) => {
      state.redirectTo = `http://127.0.0.1:${sink.address().port}/capture`;
      await assert.rejects(client.services.get('redirect'), (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 302);
        assert.equal(error.code, 'internal');
        assert.match(error.message, /redirect/);
        return true;
      });
      // Also through a list request, which is a plain GET with a query.
      await assert.rejects(client.services.list({ cursor: 'redirect' }), (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 302);
        return true;
      });
      assert.deepEqual(sinkRequests, [], 'the redirect target was contacted');
    });
  } finally {
    await new Promise((resolve) => sink.close(resolve));
  }
});

test('a transport failure is unreachable and never carries the token', async () => {
  // Take a free port, then close it: connecting must fail.
  const probe = createServer(() => {});
  await new Promise((resolve) => probe.listen(0, '127.0.0.1', resolve));
  const port = probe.address().port;
  await new Promise((resolve) => probe.close(resolve));

  const client = new Client({ host: `http://127.0.0.1:${port}`, token: 'secret-token' });
  try {
    await assert.rejects(client.version(), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'unreachable');
      assert.equal(error.status, 0);
      assert.doesNotMatch(String(error), /secret-token/);
      return true;
    });
  } finally {
    client.close();
  }
});

test('a request that outlives the deadline is unreachable', async () => {
  await withManager(async ({ host }) => {
    const client = new Client({ host, token: TOKEN, timeoutMs: 50 });
    try {
      await assert.rejects(
        client.services.list({ cursor: 'slow' }),
        (error) => error instanceof ApiError && error.code === 'unreachable' && /timed out/.test(error.message),
      );
    } finally {
      client.close();
    }
  });
});

// -- retries ----------------------------------------------------------------

function attempts(state, path) {
  return state.requests.filter((entry) => entry.path === path);
}

function retryingClient(host, options = {}) {
  const delays = [];
  const client = new Client({
    host,
    token: TOKEN,
    retries: 2,
    sleep: async (ms) => {
      delays.push(ms);
    },
    ...options,
  });
  return { client, delays };
}

test('retries default to zero', async () => {
  await withManager(async ({ client, state }) => {
    await assert.rejects(client._request('GET', '/v1/retry/twice'), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.status, 503);
      return true;
    });
    assert.equal(attempts(state, '/v1/retry/twice').length, 1);
  });
});

test('retries retry 5xx until success with exponential backoff', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 2 });
    try {
      const document = await client._request('GET', '/v1/retry/twice');
      assert.equal(document.serverVersion, 'v0.11.0');
      assert.equal(attempts(state, '/v1/retry/twice').length, 3);
      assert.deepEqual(delays, [500, 1000]);
    } finally {
      client.close();
    }
  });
});

test('Retry-After seconds is honoured', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 1 });
    try {
      await client._request('GET', '/v1/retry/after-seconds');
      assert.equal(attempts(state, '/v1/retry/after-seconds').length, 2);
      assert.deepEqual(delays, [7000]);
    } finally {
      client.close();
    }
  });
});

test('Retry-After HTTP date is honoured', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 1 });
    try {
      await client._request('GET', '/v1/retry/after-date');
      assert.equal(attempts(state, '/v1/retry/after-date').length, 2);
      assert.equal(delays.length, 1);
      assert.ok(delays[0] > 1000, `delay ${delays[0]}ms`);
      assert.ok(delays[0] <= 3000, `delay ${delays[0]}ms`);
    } finally {
      client.close();
    }
  });
});

test('Retry-After is capped by retryMaxWaitMs', async () => {
  await withManager(async ({ host }) => {
    const { client, delays } = retryingClient(host, { retries: 1, retryMaxWaitMs: 5000 });
    try {
      await client._request('GET', '/v1/retry/capped');
      assert.deepEqual(delays, [5000]);
    } finally {
      client.close();
    }
  });
});

test('a retry budget exhausts into the last error', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 1 });
    try {
      await assert.rejects(client._request('GET', '/v1/retry/always'), (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 503);
        assert.equal(error.code, 'unavailable');
        return true;
      });
      assert.equal(attempts(state, '/v1/retry/always').length, 2);
      assert.equal(delays.length, 1);
    } finally {
      client.close();
    }
  });
});

test('a 4xx is never retried', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 2 });
    try {
      await assert.rejects(client._request('GET', '/v1/retry/bad-request'), (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 400);
        return true;
      });
      assert.equal(attempts(state, '/v1/retry/bad-request').length, 1);
      assert.deepEqual(delays, []);
    } finally {
      client.close();
    }
  });
});

test('a POST without an idempotency key is never retried', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 2 });
    try {
      const body = {
        name: 'flaky-no-key',
        spec: { mode: 'replicated', replicas: 1, task: { image: 'api:1' } },
      };
      await assert.rejects(client.services.create(body), (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 503);
        return true;
      });
      assert.equal(attempts(state, '/v1/services').length, 1);
      assert.deepEqual(delays, []);
    } finally {
      client.close();
    }
  });
});

test('a POST with an idempotency key is retried', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 2 });
    try {
      const body = {
        name: 'flaky',
        spec: { mode: 'replicated', replicas: 1, task: { image: 'api:1' } },
      };
      const document = await client.services.create(body, { idempotencyKey: 'create-1' });
      assert.equal(document.name, 'flaky');
      const sent = attempts(state, '/v1/services');
      assert.equal(sent.length, 3);
      assert.deepEqual(delays, [500, 1000]);
      assert.ok(sent.every((entry) => entry.headers['idempotency-key'] === 'create-1'));
    } finally {
      client.close();
    }
  });
});

test('a non-positive Retry-After falls back to the backoff', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 1 });
    try {
      for (const hint of ['-5', '0']) {
        state.retryHint = hint;
        state.requests.length = 0;
        state.counters = {};
        delays.length = 0;

        await client._request('GET', '/v1/retry/after-hint');

        assert.equal(attempts(state, '/v1/retry/after-hint').length, 2, `hint ${hint}`);
        assert.deepEqual(delays, [500], `hint ${hint}`);
      }
    } finally {
      client.close();
    }
  });
});

test('an undecodable Retry-After falls back to the backoff', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 1 });
    try {
      state.retryHint = 'soon';
      await client._request('GET', '/v1/retry/after-hint');
      assert.equal(attempts(state, '/v1/retry/after-hint').length, 2);
      assert.deepEqual(delays, [500]);
    } finally {
      client.close();
    }
  });
});

test('a stream is never retried', async () => {
  await withManager(async ({ host, state }) => {
    const { client, delays } = retryingClient(host, { retries: 2 });
    try {
      await assert.rejects(
        async () => {
          for await (const item of client.events.watch({ types: ['flaky'] })) {
            assert.fail(`unexpected event ${item.type}`);
          }
        },
        (error) => {
          assert.ok(error instanceof ApiError);
          assert.equal(error.status, 503);
          return true;
        },
      );
      assert.equal(attempts(state, '/v1/events').length, 1);
      assert.deepEqual(delays, []);
    } finally {
      client.close();
    }
  });
});

test('retryAfter is exposed on the error', async () => {
  await withManager(async ({ client }) => {
    await assert.rejects(client._request('GET', '/v1/retry/after-seconds'), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.retryAfter, '7');
      return true;
    });
  });
});

test('retry configuration is validated', () => {
  assert.throws(() => new Client({ host: 'https://mgr.example.com:4244', retries: -1 }), TypeError);
  assert.throws(
    () => new Client({ host: 'https://mgr.example.com:4244', retryBackoffMs: -1 }),
    TypeError,
  );
  assert.throws(
    () => new Client({ host: 'https://mgr.example.com:4244', retryMaxWaitMs: -1 }),
    TypeError,
  );
  assert.throws(() => new Client({ host: 'https://mgr.example.com:4244', sleep: 'later' }), TypeError);
});

// -- configuration ----------------------------------------------------------

test('plain http is rejected for a non-loopback host', async () => {
  assert.throws(() => new Client({ host: 'http://mgr.example.com:4244' }), TypeError);
  assert.throws(() => new Client({ host: 'ftp://127.0.0.1' }), TypeError);
  const client = new Client({ host: 'https://mgr.example.com:4244' });
  client.close();
});

test('token and tokenFile are mutually exclusive', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'swarmcracker-sdk-'));
  const tokenFile = join(directory, 'token');
  writeFileSync(tokenFile, `  ${TOKEN}\n`);

  assert.throws(() => new Client({ host: 'https://mgr.example.com:4244', token: TOKEN, tokenFile }), TypeError);

  await withManager(async ({ host, lastRequest }) => {
    const client = new Client({ host, tokenFile });
    try {
      assert.equal((await client.version()).kind, 'VersionInfo');
    } finally {
      client.close();
    }
    assert.equal(lastRequest('GET', '/v1/version').headers.authorization, `Bearer ${TOKEN}`);
  });
});

test('a missing token file is a typed error', () => {
  assert.throws(
    () => new Client({ host: 'https://mgr.example.com:4244', tokenFile: '/nonexistent/swarmcracker/token' }),
    (error) => error instanceof ApiError && error.code === 'usage' && error.status === 0,
  );
});

test('a client without a token sends no authorization header', async () => {
  await withManager(async ({ host, lastRequest }) => {
    const client = new Client({ host });
    try {
      await assert.rejects(client.version(), (error) => error instanceof ApiError && error.code === 'unauthenticated');
    } finally {
      client.close();
    }
    assert.equal(lastRequest('GET', '/v1/version').headers.authorization, undefined);
  });
});

test('ca accepts a PEM string or a file path', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'swarmcracker-ca-'));
  const caFile = join(directory, 'ca.pem');
  const pem = [
    '-----BEGIN CERTIFICATE-----',
    'MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw',
    '-----END CERTIFICATE-----',
    '',
  ].join('\n');
  writeFileSync(caFile, pem);

  // Construction only: no connection is made, so the CA is not exercised.
  new Client({ host: 'https://mgr.example.com:4244', ca: pem }).close();
  new Client({ host: 'https://mgr.example.com:4244', ca: caFile }).close();
  assert.throws(
    () => new Client({ host: 'https://mgr.example.com:4244', ca: join(directory, 'missing.pem') }),
    (error) => error instanceof ApiError && error.code === 'usage',
  );
});

test('the package re-exports the client and the media types', async () => {
  const module = await import('../src/index.mjs');
  for (const name of ['ApiError', 'Client', 'ExecStream', 'LogStream', 'EventStream']) {
    assert.equal(typeof module[name], 'function', name);
  }
  assert.equal(module.V1_MEDIA_TYPE, 'application/vnd.swarmcracker.v1+json');
  assert.equal(module.NDJSON_MEDIA_TYPE, 'application/x-ndjson');
  assert.equal(module.SSE_MEDIA_TYPE, 'text/event-stream');
  assert.equal(module.MERGE_PATCH_MEDIA_TYPE, 'application/merge-patch+json');
});
