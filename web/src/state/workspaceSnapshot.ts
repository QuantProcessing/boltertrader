import {
  AuthGenerationGuard,
  authGeneration,
} from "./authGeneration";

export interface SnapshotReadContext {
  readonly generation: number;
  readonly signal: AbortSignal;
}

export type SnapshotReader<T> = (context: SnapshotReadContext) => T | Promise<T>;

export interface OptionalSnapshotRead<T> {
  readonly read: SnapshotReader<T>;
  readonly optional: true;
  readonly empty: T | (() => T);
}

export type SnapshotRead<T> = SnapshotReader<T> | OptionalSnapshotRead<T>;

type SnapshotValue<T> = T extends SnapshotReader<infer Value>
  ? Awaited<Value>
  : T extends OptionalSnapshotRead<infer Value>
    ? Awaited<Value>
    : never;

export type WorkspaceSnapshot<Reads> = {
  [Name in keyof Reads]: SnapshotValue<Reads[Name]>;
};

export interface WorkspaceSnapshotOptions {
  authGuard?: AuthGenerationGuard;
  signal?: AbortSignal;
}

export class SnapshotLoadError extends Error {
  readonly readName: string;

  constructor(readName: string, cause: unknown) {
    super(`workspace snapshot read failed: ${readName}`, { cause });
    this.name = "SnapshotLoadError";
    this.readName = readName;
  }
}

type AnySnapshotRead = SnapshotRead<unknown>;

export async function loadWorkspaceSnapshot<Reads extends Record<string, AnySnapshotRead>>(
  reads: Reads,
  options: WorkspaceSnapshotOptions = {},
): Promise<WorkspaceSnapshot<Reads>> {
  const guard = options.authGuard ?? authGeneration;
  const auth = guard.capture();
  const controller = new AbortController();
  const detach = forwardAbort([auth.signal, options.signal], controller);
  const entries = Object.entries(reads).map(([name, configured]) => {
    const descriptor = normalizeRead(configured);
    const context: SnapshotReadContext = {
      generation: auth.generation,
      signal: controller.signal,
    };
    const result = Promise.resolve()
      .then(() => descriptor.read(context))
      .catch((error: unknown) => {
        auth.assertCurrent();
        if (options.signal?.aborted) {
          throw options.signal.reason ?? new DOMException("snapshot aborted", "AbortError");
        }
        if (descriptor.optional) return resolveEmpty(descriptor.empty);
        if (!controller.signal.aborted) controller.abort(error);
        throw new SnapshotLoadError(name, error);
      });
    return result.then((value) => [name, value] as const);
  });

  try {
    const resolved = await Promise.all(entries);
    auth.assertCurrent();
    return Object.fromEntries(resolved) as WorkspaceSnapshot<Reads>;
  } finally {
    detach();
  }
}

function normalizeRead(configured: AnySnapshotRead): {
  read: SnapshotReader<unknown>;
  optional: boolean;
  empty?: unknown | (() => unknown);
} {
  if (typeof configured === "function") {
    return { read: configured, optional: false };
  }
  return {
    read: configured.read,
    optional: configured.optional,
    empty: configured.empty,
  };
}

function resolveEmpty(empty: unknown | (() => unknown)): unknown {
  return typeof empty === "function" ? (empty as () => unknown)() : empty;
}

function forwardAbort(
  signals: Array<AbortSignal | undefined>,
  controller: AbortController,
): () => void {
  const active = signals.filter((signal): signal is AbortSignal => Boolean(signal));
  const abort = (event: Event) => {
    const signal = event.target as AbortSignal;
    if (!controller.signal.aborted) controller.abort(signal.reason);
  };
  for (const signal of active) {
    if (signal.aborted) {
      controller.abort(signal.reason);
      break;
    }
    signal.addEventListener("abort", abort, { once: true });
  }
  return () => {
    for (const signal of active) signal.removeEventListener("abort", abort);
  };
}
