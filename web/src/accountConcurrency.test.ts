import { describe, expect, it } from "vitest";
import { formatAccountConcurrency } from "./accountConcurrency";

describe("formatAccountConcurrency", () => {
	it.each([
		{ active: 0, limit: 0, expected: "0/∞" },
		{ active: 10, limit: 0, expected: "10/∞" },
		{ active: 10, limit: 100, expected: "10/100" },
	])("formats $active active requests with limit $limit", ({ active, limit, expected }) => {
		expect(formatAccountConcurrency({ supported: true, active, limit })).toBe(expected);
	});
});
