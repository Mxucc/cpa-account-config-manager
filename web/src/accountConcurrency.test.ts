import { describe, expect, it } from "vitest";
import { accountConcurrencySaturated, formatAccountConcurrency } from "./accountConcurrency";

describe("formatAccountConcurrency", () => {
	it("formats active transfers and both rolling request windows", () => {
		expect(formatAccountConcurrency({
			supported: true,
			active: 10,
			limit: 100,
			limit_15s: 3,
			used_60s: 7,
			used_15s: 2,
		})).toBe("active 10 · 15s 2/3 · 60s 7/100");
	});

	it("uses rolling windows rather than active transfers for saturation", () => {
		expect(accountConcurrencySaturated({ supported: true, active: 99, limit: 10, limit_15s: 3, used_60s: 9, used_15s: 2 })).toBe(false);
		expect(accountConcurrencySaturated({ supported: true, active: 1, limit: 10, limit_15s: 3, used_60s: 9, used_15s: 3 })).toBe(true);
		expect(accountConcurrencySaturated({ supported: true, active: 1, limit: 10, limit_15s: 3, used_60s: 10, used_15s: 1 })).toBe(true);
	});
});
