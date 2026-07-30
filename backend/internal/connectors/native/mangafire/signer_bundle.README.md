# Vendored MangaFire request signer (`signer_bundle.js`)

> **Status (2026-07-30): the signer is not the current problem — do not start here.**
> MangaFire enabled a Cloudflare **managed challenge across the whole domain**.
> Every path (`/`, `/title/…`, `/api/titles*`, even `/robots.txt`) answers `403`
> with `Cf-Mitigated: challenge` and the "Just a moment…" interstitial, so no
> request reaches the point where the `vrf` token is checked. Verified from two
> unrelated networks, so it is not IP reputation, and a Chrome-JA3 `utls` client
> (the fix that works for the freewebnovel connector) is challenged identically.
> Getting past an interactive challenge is not something this project automates.
> The connector detects this case and reports "Cloudflare browser challenge" with
> a 30-minute cooldown, so it recovers on its own if MangaFire turns it off.
> Recheck with:
>
> ```
> go test -tags live -run TestLive -count=1 ./internal/connectors/native/mangafire
> ```
>
> `-count=1` matters — otherwise Go replays a cached PASS from before the block.
> Only follow the refresh runbook below once requests get far enough to return a
> JSON `{"message":"Invalid token."}` again.

MangaFire's JSON API (`/api/titles*`) rejects any request without a valid `vrf`
query-token, answering `403 {"message":"Missing token."}`. The token is produced
client-side by `globalThis.getProtectionToken(path, paramsObject)`, a deterministic
(chained-cipher) routine that MangaFire ships in an obfuscated bundle. There is no
server-rendered HTML fallback — the site is a pure SPA — so the only way to read
cover art or chapter lists is to present a valid token.

Rather than reverse-engineer the cipher (it is intentionally obfuscated and rotated),
[`signer.go`](signer.go) runs MangaFire's *own* signer in a pure-Go JS engine
(`goja`) after transpiling it with `esbuild`. `getProtectionToken` only touches a
handful of host globals (a no-op `document.querySelector`, `navigator.appCodeName`,
`localStorage`, `TextEncoder`, base64), all shimmed in `signer.go`. The tokens it
mints are byte-identical to the browser's and are accepted by the live API.

## Provenance

- File: `signer_bundle.js`
- Source: `https://s.mfcdn.nl/build/mf/assets/polyfill-<hash>.js` (the `polyfill-*`
  chunk referenced by MangaFire's `main-*` bundle; it is the chunk that defines
  `getProtectionToken`/`dynamicEncrypt`).
- Captured: 2026-07-23. Byte-for-byte upstream; do not hand-edit.

## When to refresh

If covers/chapters start failing again with `403 {"message":"Invalid token."}`
(as opposed to `"Missing token."`), MangaFire has rotated the signer/key and this
bundle is stale. To refresh:

1. Open https://mangafire.to in a browser, view the network tab, and find the
   loaded `polyfill-*.js` chunk (or read the `main-*.js` bundle for the chunk name).
   Confirm it defines the signer: `grep getProtectionToken`.
2. Download it and overwrite `signer_bundle.js`.
3. Run the signer test (`go test ./internal/connectors/native/mangafire/ -run Signer`).
   The test pins known `(path, params) -> token` vectors; update them from the
   browser (`getProtectionToken(path, params)` in the console) if the algorithm
   changed, and confirm a live token is accepted.
