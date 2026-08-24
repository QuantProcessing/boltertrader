import { describe, expect, it, vi } from "vitest";

import { PreferencesStore } from "@/app/preferences";

describe("operator preferences", () => {
  it("confirms orders by default and persists an explicit opt-out", () => {
    const storage = new MemoryStorage();
    const store = new PreferencesStore(storage);
    expect(store.getSnapshot()).toEqual({ confirmOrders: true });

    store.setConfirmOrders(false);

    expect(new PreferencesStore(storage).getSnapshot()).toEqual({ confirmOrders: false });
  });

  it("publishes changes immediately", () => {
    const store = new PreferencesStore(new MemoryStorage());
    const listener = vi.fn();
    store.subscribe(listener);

    store.setConfirmOrders(false);

    expect(listener).toHaveBeenCalledOnce();
    expect(store.getSnapshot().confirmOrders).toBe(false);
  });

  it("falls back safely when stored preferences are malformed", () => {
    const storage = new MemoryStorage();
    storage.setItem("bolter.preferences.v1", "not-json");
    expect(new PreferencesStore(storage).getSnapshot()).toEqual({ confirmOrders: true });
  });
});

class MemoryStorage implements Storage {
  #values = new Map<string, string>();

  get length(): number {
    return this.#values.size;
  }

  clear(): void {
    this.#values.clear();
  }

  getItem(key: string): string | null {
    return this.#values.get(key) ?? null;
  }

  key(index: number): string | null {
    return [...this.#values.keys()][index] ?? null;
  }

  removeItem(key: string): void {
    this.#values.delete(key);
  }

  setItem(key: string, value: string): void {
    this.#values.set(key, value);
  }
}
