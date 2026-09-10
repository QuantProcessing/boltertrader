import { expect, it } from "vitest";
import { createQueryClient } from "./query";

it("clears private caches created by websocket before a page mounts", () => {
  const client = createQueryClient();
  client.setQueryData(["trading", "orders"], [{ commandId: "private-order" }]);
  client.setQueryData(["manual", "preview"], { accountId: "private-account" });
  client.setQueryData(["market", "book"], { price: "1" });
  client.removeQueries({ predicate: (query) => query.meta?.privateData === true });
  expect(client.getQueryData(["trading", "orders"])).toBeUndefined();
  expect(client.getQueryData(["manual", "preview"])).toBeUndefined();
  expect(client.getQueryData(["market", "book"])).toEqual({ price: "1" });
  client.clear();
});
