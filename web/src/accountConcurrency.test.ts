import { describe, expect, it } from "vitest";
import { accountConcurrencySaturated, formatAccountConcurrency } from "./accountConcurrency";

describe("formatAccountConcurrency", () => {
	it("formats active concurrency, the configured request window, and queue", () => {
		expect(formatAccountConcurrency({
			supported: true,
			active: 10,
			limit: 100,
			request_limit: 3,
			request_window_seconds: 15,
			used_requests: 2,
			waiting: 0,
		})).toBe("active 10/100 · 15s request 2/3 · queue 0");
	});

	it("saturates when active or request-window capacity is reached", () => {
		expect(accountConcurrencySaturated({ supported: true, active: 9, limit: 10, request_limit: 3, request_window_seconds: 15, used_requests: 2 })).toBe(false);
		expect(accountConcurrencySaturated({ supported: true, active: 1, limit: 10, request_limit: 3, request_window_seconds: 15, used_requests: 3 })).toBe(true);
		expect(accountConcurrencySaturated({ supported: true, active: 10, limit: 10, request_limit: 3, request_window_seconds: 15, used_requests: 1 })).toBe(true);
	});
});
