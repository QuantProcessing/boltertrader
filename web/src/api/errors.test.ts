import { describe, expect, it } from "vitest";

import type { Translate } from "@/lib/i18n";
import { ApiError, describeError } from "./errors";

const messages = {
  "error.validation": "提交内容未通过校验，请检查必填项和字段格式",
} as const;

const t: Translate = (key) => messages[key as keyof typeof messages] ?? key;

describe("describeError", () => {
  it("turns the gateway's redacted validation sentence into actionable copy", () => {
    const error = new ApiError(400, {
      code: "VALIDATION_ERROR",
      message: "request denied",
      details: {},
      retryable: false,
      requestId: "req_123",
    });

    expect(describeError(error, t)).toBe(
      "提交内容未通过校验，请检查必填项和字段格式 (VALIDATION_ERROR · req_123)",
    );
  });

  it("keeps a specific server message intact", () => {
    const error = new ApiError(409, {
      code: "CONFLICT",
      message: "revision is stale",
      details: {},
      retryable: false,
      requestId: "req_456",
    });

    expect(describeError(error, t)).toBe("revision is stale (CONFLICT · req_456)");
  });
});
