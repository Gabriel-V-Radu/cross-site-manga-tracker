"""
CF Browser Sidecar — Anti-detect browser proxy for Cloudflare-protected sites.

Uses Camoufox (custom anti-detect Firefox) to automatically handle Cloudflare
JS challenges, Turnstile CAPTCHAs, and cookie lifecycle. The Go backend simply
POSTs a URL and gets back rendered HTML — no manual cookie management required.
"""

import asyncio
import logging
import os
import time

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)-5s %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger("cf-browser")

WARMUP_URL = os.environ.get("WARMUP_URL", "https://mangafire.to/home")
REQUEST_TIMEOUT = int(os.environ.get("REQUEST_TIMEOUT_MS", "30000"))
CF_WAIT_SECONDS = int(os.environ.get("CF_WAIT_SECONDS", "8"))

app = FastAPI()


# ---------------------------------------------------------------------------
# Request / response models
# ---------------------------------------------------------------------------

class FetchRequest(BaseModel):
    url: str
    timeout: int = REQUEST_TIMEOUT  # ms


class FetchResponse(BaseModel):
    url: str
    html: str


# ---------------------------------------------------------------------------
# Browser lifecycle manager
# ---------------------------------------------------------------------------

class BrowserManager:
    """Manages a single Camoufox browser instance with automatic CF handling."""

    def __init__(self):
        self._cm = None          # Camoufox context-manager
        self._context = None     # BrowserContext (Playwright)
        self._page = None        # Page
        self._lock = asyncio.Lock()
        self._ready = False
        self._started_at: float = 0

    # -- lifecycle -----------------------------------------------------------

    async def _launch(self):
        """(Re-)start the anti-detect browser and warm it up."""
        await self._close_internal()

        from camoufox.async_api import AsyncCamoufox

        logger.info("Launching Camoufox browser …")
        self._cm = AsyncCamoufox(headless=False, humanize=True)
        self._context = await self._cm.__aenter__()
        self._page = await self._context.new_page()
        self._started_at = time.monotonic()

        # Warm up — visit homepage so CF sets its cookies in-browser.
        logger.info("Warming up on %s …", WARMUP_URL)
        try:
            await self._page.goto(
                WARMUP_URL, wait_until="domcontentloaded", timeout=60_000
            )
            # Give the invisible JS challenge a moment to complete.
            await self._page.wait_for_load_state("networkidle", timeout=15_000)

            warmup_html = await self._page.content()
            warmup_title = await self._page.title()
            if self._is_cf_challenge(warmup_title, warmup_html):
                logger.info("CF challenge during warm-up — waiting for auto-resolution …")
                if await self._wait_for_challenge(timeout_s=30):
                    logger.info("CF challenge resolved during warm-up")
                else:
                    logger.warning("CF challenge NOT resolved during warm-up")
            else:
                await asyncio.sleep(CF_WAIT_SECONDS)
        except Exception as exc:
            logger.warning("Warm-up hiccup (may still work): %s", exc)

        logger.info("Browser ready. Current URL: %s", self._page.url)
        self._ready = True

    async def _close_internal(self):
        if self._cm is not None:
            try:
                await self._cm.__aexit__(None, None, None)
            except Exception:
                pass
            self._cm = None
            self._context = None
            self._page = None
            self._ready = False

    async def close(self):
        async with self._lock:
            await self._close_internal()

    async def _ensure(self):
        if not self._ready:
            await self._launch()

    # -- CF challenge detection & resolution --------------------------------

    @staticmethod
    def _is_cf_challenge(title: str, html: str) -> bool:
        # Primary signal: CF challenge pages have a distinctive title.
        title_markers = ["Just a moment", "Attention Required"]
        if any(m.lower() in title.lower() for m in title_markers):
            return True
        # Fallback: page has almost no real content (CF interstitial pages are tiny).
        if len(html) < 4000 and "Checking your browser" in html:
            return True
        return False

    async def _wait_for_challenge(self, timeout_s: int = 30) -> bool:
        """Wait up to *timeout_s* seconds for a CF challenge to auto-resolve."""
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            await asyncio.sleep(2)
            title = await self._page.title()
            html = await self._page.content()
            if not self._is_cf_challenge(title, html):
                return True
            logger.debug("Still on challenge page (title=%r) …", title)
        return False

    # -- public API ----------------------------------------------------------

    async def fetch(self, url: str, timeout_ms: int = 30_000) -> FetchResponse:
        async with self._lock:
            await self._ensure()

            try:
                resp = await self._page.goto(
                    url, wait_until="domcontentloaded", timeout=timeout_ms
                )
            except Exception as exc:
                logger.error("Navigation error for %s: %s", url, exc)
                self._ready = False
                raise

            # Best-effort: wait for network to quiet down, but don't fail on it.
            try:
                await self._page.wait_for_load_state(
                    "networkidle", timeout=15_000
                )
            except Exception:
                pass

            html = await self._page.content()

            # If we landed on a CF challenge page, wait for auto-resolution.
            title = await self._page.title()
            if self._is_cf_challenge(title, html):
                logger.info("CF challenge detected for %s (title=%r) — waiting …", url, title)
                if await self._wait_for_challenge():
                    html = await self._page.content()
                    logger.info("CF challenge resolved for %s", url)
                else:
                    logger.warning(
                        "CF challenge NOT resolved for %s — restarting browser",
                        url,
                    )
                    self._ready = False
                    raise RuntimeError("CF challenge could not be auto-solved")

            current_url = self._page.url

            return FetchResponse(url=current_url, html=html)


manager = BrowserManager()


# ---------------------------------------------------------------------------
# FastAPI routes
# ---------------------------------------------------------------------------

@app.on_event("startup")
async def startup():
    """Best-effort browser launch at startup; first /fetch retries if needed."""
    try:
        async with manager._lock:
            await manager._launch()
    except Exception as exc:
        logger.error("Startup browser launch failed (will retry on first request): %s", exc)


@app.on_event("shutdown")
async def shutdown():
    await manager.close()


@app.get("/health")
async def health():
    return {"status": "ok" if manager._ready else "starting"}


@app.post("/fetch", response_model=FetchResponse)
async def fetch(req: FetchRequest):
    try:
        return await manager.fetch(req.url, req.timeout)
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc))
