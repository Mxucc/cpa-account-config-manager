#!/usr/bin/env python3
"""Capture real-browser evidence for the provider v2 interface.

Starts nothing itself: run web/scripts/mock-provider-ui.mjs first (or point
MOCK_BASE at an equivalent host). Uses Playwright's Chromium, logs in through the
real form and walks every primary view at desktop and mobile width, asserting
there is no horizontal overflow and no console error.

Usage:
    node web/scripts/mock-provider-ui.mjs &
    python3 web/scripts/capture-evidence.py
"""

import json
import os
import sys

from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOCK_BASE", "http://127.0.0.1:8399/")
OUT = os.environ.get("EVIDENCE_DIR", ".web-builder/evidence/sprint-5")
KEY = os.environ.get("MOCK_KEY", "evidence-key")

VIEWS = [
    ("overview", "#/overview"),
    ("accounts", "#/accounts"),
    ("providers", "#/providers"),
    ("provider-codex", "#/providers/codex/accounts"),
    ("provider-compat", "#/providers/codex/advanced"),
    ("quota", "#/quota"),
    ("usage", "#/usage"),
]


def main() -> int:
    os.makedirs(OUT, exist_ok=True)
    report = {"base": BASE, "shots": [], "overflow": [], "console": []}
    failures = []

    with sync_playwright() as playwright:
        browser = playwright.chromium.launch()
        for label, width, height in (("desktop", 1440, 900), ("mobile", 390, 844)):
            context = browser.new_context(viewport={"width": width, "height": height}, device_scale_factor=1)
            page = context.new_page()
            page.on(
                "console",
                lambda message: report["console"].append({"type": message.type, "text": message.text})
                if message.type == "error"
                else None,
            )
            page.on("pageerror", lambda error: report["console"].append({"type": "pageerror", "text": str(error)}))

            page.goto(BASE, wait_until="networkidle")
            page.fill('input[autocomplete="current-password"]', KEY)
            page.click('button[type="submit"]')
            page.wait_for_selector("nav[aria-label]", timeout=15000)

            for name, route in VIEWS:
                page.goto(BASE + route, wait_until="networkidle")
                page.wait_for_timeout(500)
                metrics = page.evaluate(
                    "() => ({ scroll: document.documentElement.scrollWidth,"
                    " client: document.documentElement.clientWidth,"
                    " bodyText: document.body.innerText.slice(0, 400) })"
                )
                overflow = metrics["scroll"] > metrics["client"] + 1
                report["overflow"].append(
                    {"view": name, "screen": label, "scroll": metrics["scroll"], "client": metrics["client"], "overflows": overflow}
                )
                if overflow:
                    failures.append(f"{label}/{name}: horizontal overflow {metrics['scroll']} > {metrics['client']}")
                path = os.path.join(OUT, f"{label}-{name}.png")
                page.screenshot(path=path, full_page=False)
                report["shots"].append(path)

                if name == "quota" and label == "desktop":
                    # The unreported window must say so, and the wallet must show a
                    # balance instead of a meter.
                    body = metrics["bodyText"] + page.inner_text("body")
                    bars = page.locator('[role="progressbar"]').count()
                    report["quota_text"] = {
                        "says_not_reported": "未上报" in body,
                        "mentions_balance": "Balance" in body or "余额" in body,
                        # 10 windows exist; one is unreported and one is a wallet, so
                        # exactly 8 meters may be drawn. A meter for the unreported
                        # window (or one for the wallet) would make this 9 or 10.
                        "meters": bars,
                    }

            if label == "desktop":
                page.goto(BASE + "#/accounts", wait_until="networkidle")
                page.wait_for_timeout(400)
                page.get_by_role("button", name="详情").first.click()
                page.wait_for_timeout(400)
                drawer = os.path.join(OUT, "desktop-account-drawer.png")
                page.screenshot(path=drawer)
                report["shots"].append(drawer)

            context.close()
        browser.close()

    if report["console"]:
        failures.append(f"console errors: {report['console'][:3]}")
    if not report.get("quota_text", {}).get("says_not_reported"):
        failures.append("the unreported quota window does not say 未上报")
    if not report.get("quota_text", {}).get("mentions_balance"):
        failures.append("the wallet balance is not visible on the quota page")
    meters = report.get("quota_text", {}).get("meters")
    if meters != 8:
        failures.append(f"quota meters = {meters}, want 8 (unreported window and wallet must not draw one)")

    # A blank screenshot is a failed capture, not evidence.
    for shot in report["shots"]:
        if os.path.getsize(shot) < 10_000:
            failures.append(f"{shot} looks blank ({os.path.getsize(shot)} bytes)")

    report["ok"] = not failures
    report["failures"] = failures
    with open(os.path.join(OUT, "visual-report.json"), "w", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2, ensure_ascii=False)

    print(f"shots: {len(report['shots'])}")
    print(f"overflow: {sum(1 for row in report['overflow'] if row['overflows'])}/{len(report['overflow'])}")
    for failure in failures:
        print(f"FAIL: {failure}")
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
