// Hand-written declarations for the SwarmCracker Node client (src/client.mjs).
//
// The wire models come from the generated types.d.ts, so the client never
// re-declares a document shape the OpenAPI spec already owns.
//
// The stream declarations rely on lib "es2018" (AsyncIterable) or later.

import type {
  Event,
  ServiceDocument,
  ServiceList,
  ServicePatch,
  ServiceWrite,
  TaskDocument,
  TaskList,
  VersionInfo,
} from './types.js';

/** The only body media type API v1 serves or accepts. */
export declare const V1_MEDIA_TYPE: 'application/vnd.swarmcracker.v1+json';
/** The request media type of a merge patch (PATCH /v1/services/{ref}). */
export declare const MERGE_PATCH_MEDIA_TYPE: 'application/merge-patch+json';
/** The media type of the exec and log streams, the documented envelope exception. */
export declare const NDJSON_MEDIA_TYPE: 'application/x-ndjson';
/** The media type of the event stream. */
export declare const SSE_MEDIA_TYPE: 'text/event-stream';
/** The API version this client speaks, sent on every request. */
export declare const API_VERSION: '1';
export declare const API_VERSION_HEADER: 'X-SwarmCracker-Api-Version';
export declare const REQUEST_ID_HEADER: 'X-Request-Id';
/** The frame types of an exec stream. */
export declare const EXEC_FRAME_TYPES: ReadonlySet<ExecFrameType>;
/** Hosts for which plain http is accepted. */
export declare const LOOPBACK_HOSTS: ReadonlySet<string>;

export interface ApiErrorInit {
  /** The HTTP status; 0 for a transport failure. */
  status?: number;
  requestId?: string | null;
  retryable?: boolean;
  details?: Record<string, unknown> | null;
}

/**
 * A typed v1 failure, a transport failure or an undecodable response.
 *
 * `code` is the v1 error code; a transport failure is `"unreachable"` (status 0)
 * and a response the client cannot decode is `"internal"`. The bearer token never
 * appears in any field or message.
 */
export declare class ApiError extends Error {
  /** The v1 error code (`invalid`, `not_found`, `forbidden`, ...). */
  readonly code: string;
  /** The HTTP status, or 0 for a transport failure. */
  readonly status: number;
  /** The `X-Request-Id` of the response, when the server sent one. */
  readonly requestId: string | null;
  /** Whether the server says a retry can succeed. */
  readonly retryable: boolean;
  /** Typed failure context, or null. */
  readonly details: Record<string, unknown> | null;
  constructor(code: string, message: string, init?: ApiErrorInit);
}

export type ExecFrameType = 'stdout' | 'stderr' | 'exit' | 'error';

/**
 * One decoded NDJSON exec frame: the wire object with `data` base64-decoded to
 * bytes (`data` is always present and empty on frames without a payload).
 */
export interface ExecFrame {
  type: ExecFrameType;
  data: Uint8Array;
  /** Process status on an exit frame. */
  exitCode?: number;
  /** The terminating signal number on an exit frame. */
  signal?: number;
  /** The error code on an error frame. */
  code?: string;
  [key: string]: unknown;
}

/** One decoded NDJSON log frame. */
export interface LogFrame {
  type: 'log' | 'eof';
  data: Uint8Array;
  /** RFC 3339 timestamp. */
  time?: string;
  /** The merged console stream; always stdout. */
  stream?: 'stdout';
  [key: string]: unknown;
}

/** An async iterable stream that releases its connection when finished. */
export interface Frames<T> extends AsyncIterable<T> {
  /** Stop the stream and release the connection. */
  close(): void;
}

/** The NDJSON frames of `POST /v1/tasks/{ref}/exec`. */
export declare class ExecStream implements Frames<ExecFrame> {
  /** The exit frame's status. Throws ApiError with code `unreachable` when none arrived. */
  readonly exitCode: number;
  close(): void;
  [Symbol.asyncIterator](): AsyncGenerator<ExecFrame, void, void>;
  private constructor();
}

/** The NDJSON frames of `GET /v1/tasks/{ref}/logs`. */
export declare class LogStream implements Frames<LogFrame> {
  close(): void;
  [Symbol.asyncIterator](): AsyncGenerator<LogFrame, void, void>;
  private constructor();
}

/** The server-sent events of `GET /v1/events`. */
export declare class EventStream implements Frames<Event> {
  close(): void;
  [Symbol.asyncIterator](): AsyncGenerator<Event, void, void>;
  private constructor();
}

/**
 * The subset of `AbortSignal` the client uses. Structural, so the declarations
 * do not depend on the DOM lib or @types/node.
 */
export interface AbortSignalLike {
  readonly aborted: boolean;
  addEventListener(type: 'abort', listener: () => void): void;
  removeEventListener(type: 'abort', listener: () => void): void;
}

export interface ClientOptions {
  /** An https URL. Plain http is accepted only for a loopback host. */
  host: string;
  /** A PEM bundle, or the path to one. Never disables verification. */
  ca?: string;
  /** A bearer token. Mutually exclusive with `tokenFile`. */
  token?: string;
  /** A file holding a bearer token; read once and trimmed. */
  tokenFile?: string;
  /** Request deadline in milliseconds. Defaults to 30000. */
  timeoutMs?: number;
}

export interface ListOptions {
  /** One selector, or several sent as repeated `label` parameters. */
  label?: string | string[];
  limit?: number;
  cursor?: string;
}

export interface TaskListOptions extends ListOptions {
  health?: boolean;
}

export interface CreateOptions {
  idempotencyKey?: string;
}

export interface PatchOptions {
  /** Required: the resource revision (or `"*"`) the patch applies to. */
  ifMatch: string;
  /** Wait for the service to converge before answering. */
  wait?: string;
}

export interface DeleteOptions {
  /** Required: the resource revision (or `"*"`) the delete applies to. */
  ifMatch: string;
}

export interface ExecOptions {
  env?: Record<string, string>;
  workdir?: string;
  /** Stop the stream when the signal aborts. */
  signal?: AbortSignalLike;
}

export interface LogsOptions {
  follow?: boolean;
  tail?: number;
  since?: string;
  /** Stop the stream when the signal aborts. */
  signal?: AbortSignalLike;
}

export interface WatchOptions {
  /** Event type prefixes to subscribe to. */
  types?: string[];
  /** Resume after this cursor; sent as `Last-Event-ID`. */
  since?: string;
  /** Stop the stream when the signal aborts. */
  signal?: AbortSignalLike;
}

/** The `/v1/services` routes. */
export declare class Services {
  list(options?: ListOptions): Promise<ServiceList>;
  get(reference: string): Promise<ServiceDocument>;
  create(body: ServiceWrite, options?: CreateOptions): Promise<ServiceDocument>;
  patch(reference: string, patch: ServicePatch, options: PatchOptions): Promise<ServiceDocument>;
  delete(reference: string, options: DeleteOptions): Promise<null>;
  private constructor();
}

/** The `/v1/tasks` routes and the exec/log streams. */
export declare class Tasks {
  list(options?: TaskListOptions): Promise<TaskList>;
  get(reference: string, options?: { health?: boolean }): Promise<TaskDocument>;
  exec(reference: string, command: readonly string[], options?: ExecOptions): ExecStream;
  logs(reference: string, options?: LogsOptions): LogStream;
  private constructor();
}

/** The `/v1/events` stream. */
export declare class Events {
  watch(options?: WatchOptions): EventStream;
  private constructor();
}

/**
 * A client for one SwarmCracker manager.
 *
 * The client is safe for sequential reuse and is not concurrency safe.
 */
export declare class Client {
  constructor(options: ClientOptions);
  readonly services: Services;
  readonly tasks: Tasks;
  readonly events: Events;
  /** GET /v1/version: the server's identity and capabilities. */
  version(): Promise<VersionInfo>;
  /** Close every pooled connection. Safe to call more than once. */
  close(): void;
}
