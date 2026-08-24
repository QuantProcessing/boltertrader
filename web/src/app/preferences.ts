import { useSyncExternalStore } from "react";

/* ---------------------------------------------------------------------------
 * Browser-local operator preferences.
 *
 * These settings change presentation and interaction only. They never grant a
 * trading capability and they never bypass the server preview: turning order
 * confirmation off removes the second click only after that preview succeeds.
 * ------------------------------------------------------------------------- */

export interface PreferencesSnapshot {
  confirmOrders: boolean;
}

const STORAGE_KEY = "bolter.preferences.v1";
const DEFAULTS: PreferencesSnapshot = { confirmOrders: true };

function read(storage: Storage | null): PreferencesSnapshot {
  if (!storage) return DEFAULTS;
  try {
    const stored = JSON.parse(storage.getItem(STORAGE_KEY) ?? "null") as Partial<PreferencesSnapshot> | null;
    return {
      confirmOrders: typeof stored?.confirmOrders === "boolean" ? stored.confirmOrders : DEFAULTS.confirmOrders,
    };
  } catch {
    return DEFAULTS;
  }
}

function browserStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export class PreferencesStore {
  #storage: Storage | null;
  #snapshot: PreferencesSnapshot;
  #listeners = new Set<() => void>();

  constructor(storage: Storage | null = browserStorage()) {
    this.#storage = storage;
    this.#snapshot = read(storage);
  }

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  };

  getSnapshot = (): PreferencesSnapshot => this.#snapshot;

  setConfirmOrders = (confirmOrders: boolean): void => {
    if (this.#snapshot.confirmOrders === confirmOrders) return;
    this.#publish({ ...this.#snapshot, confirmOrders });
  };

  #publish(snapshot: PreferencesSnapshot): void {
    this.#snapshot = snapshot;
    try {
      this.#storage?.setItem(STORAGE_KEY, JSON.stringify(snapshot));
    } catch {
      // The in-memory preference remains valid for this document when browser
      // storage is unavailable or full.
    }
    for (const listener of this.#listeners) listener();
  }
}

export const preferencesStore = new PreferencesStore();

export function usePreferences(): PreferencesSnapshot {
  return useSyncExternalStore(
    preferencesStore.subscribe,
    preferencesStore.getSnapshot,
    preferencesStore.getSnapshot,
  );
}
