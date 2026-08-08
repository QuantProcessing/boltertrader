export interface AuthBoundary {
  generation: number;
  reason: string;
}

export interface AuthGenerationSnapshot {
  readonly generation: number;
  readonly signal: AbortSignal;
  assertCurrent(): void;
}

export class StaleAuthGenerationError extends Error {
  readonly capturedGeneration: number;
  readonly currentGeneration: number;

  constructor(capturedGeneration: number, currentGeneration: number) {
    super("authentication changed while the operation was in flight");
    this.name = "StaleAuthGenerationError";
    this.capturedGeneration = capturedGeneration;
    this.currentGeneration = currentGeneration;
  }
}

export class AuthGenerationGuard {
  private generation = 0;
  private controller = new AbortController();
  private readonly listeners = new Set<(boundary: AuthBoundary) => void>();

  capture(): AuthGenerationSnapshot {
    const generation = this.generation;
    const signal = this.controller.signal;
    return {
      generation,
      signal,
      assertCurrent: () => this.assertCurrent(generation),
    };
  }

  advance(reason: string): number {
    this.controller.abort(reason);
    this.generation += 1;
    this.controller = new AbortController();
    const boundary = { generation: this.generation, reason };
    for (const listener of this.listeners) {
      listener(boundary);
    }
    return this.generation;
  }

  assertCurrent(generation: number): void {
    if (generation !== this.generation) {
      throw new StaleAuthGenerationError(generation, this.generation);
    }
  }

  onBoundary(listener: (boundary: AuthBoundary) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
}

export const authGeneration = new AuthGenerationGuard();
