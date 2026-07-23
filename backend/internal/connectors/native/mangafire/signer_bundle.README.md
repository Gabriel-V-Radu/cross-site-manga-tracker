# Vendored MangaFire request signer (`signer_bundle.js`)

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
