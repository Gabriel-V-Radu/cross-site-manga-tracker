package mangafire

import (
	_ "embed"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// signerBundle is MangaFire's own obfuscated signer chunk. It defines
// globalThis.getProtectionToken(path, paramsObject), which every /api/titles*
// request must call to produce the mandatory `vrf` query token. See
// signer_bundle.README.md for provenance and refresh instructions.
//
//go:embed signer_bundle.js
var signerBundle string

// signerHostShim provides the minimal browser globals getProtectionToken touches.
// The routine is deterministic and does not depend on any real DOM state (proven
// by matching the browser byte-for-byte), so no-op/constant stubs are sufficient.
// navigator.appCodeName must be the standard "Mozilla" — the signer throws if it
// is missing or empty, and it feeds the token's key derivation.
const signerHostShim = `
var globalThis = this;
var window = this;
var self = this;
var navigator = { appCodeName: "Mozilla", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36", appName: "Netscape", platform: "Win32", language: "en-US" };
var __ls = {};
var localStorage = {
  getItem: function(k){ return Object.prototype.hasOwnProperty.call(__ls, k) ? __ls[k] : null; },
  setItem: function(k, v){ __ls[k] = String(v); },
  removeItem: function(k){ delete __ls[k]; },
  clear: function(){ __ls = {}; }
};
var document = {
  cookie: "",
  documentElement: {},
  querySelector: function(){ return null; },
  querySelectorAll: function(){ return []; },
  getElementById: function(){ return null; },
  getElementsByTagName: function(){ return []; },
  getElementsByClassName: function(){ return []; },
  createElement: function(){ return { style: {}, setAttribute: function(){}, appendChild: function(){} }; },
  addEventListener: function(){},
  removeEventListener: function(){}
};
var console = { log: function(){}, warn: function(){}, error: function(){}, info: function(){}, debug: function(){} };
function setTimeout(f){ if (typeof f === "function") { f(); } return 0; }
function setInterval(){ return 0; }
function clearTimeout(){}
function clearInterval(){}
function queueMicrotask(f){ if (typeof f === "function") { f(); } }
var performance = { now: function(){ return 0; } };
var crypto = { getRandomValues: function(a){ for (var i = 0; i < a.length; i++) { a[i] = 0; } return a; } };
var __b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function btoa(s){
  var out = "", i = 0;
  while (i < s.length){
    var c1 = s.charCodeAt(i++), c2 = s.charCodeAt(i++), c3 = s.charCodeAt(i++);
    var e1 = c1 >> 2, e2 = ((c1 & 3) << 4) | (c2 >> 4), e3 = ((c2 & 15) << 2) | (c3 >> 6), e4 = c3 & 63;
    if (isNaN(c2)) { e3 = e4 = 64; } else if (isNaN(c3)) { e4 = 64; }
    out += __b64.charAt(e1) + __b64.charAt(e2) + (e3 === 64 ? "=" : __b64.charAt(e3)) + (e4 === 64 ? "=" : __b64.charAt(e4));
  }
  return out;
}
function atob(s){
  s = String(s).replace(/=+$/, ""); var out = "", bits = 0, val = 0;
  for (var i = 0; i < s.length; i++){
    var idx = __b64.indexOf(s.charAt(i)); if (idx < 0) { continue; }
    val = (val << 6) | idx; bits += 6;
    if (bits >= 8){ bits -= 8; out += String.fromCharCode((val >> bits) & 0xFF); }
  }
  return out;
}
function TextEncoder(){}
TextEncoder.prototype.encode = function(str){
  str = String(str === undefined ? "" : str);
  var bytes = [];
  for (var i = 0; i < str.length; i++){
    var c = str.charCodeAt(i);
    if (c < 0x80) { bytes.push(c); }
    else if (c < 0x800) { bytes.push(0xC0 | (c >> 6), 0x80 | (c & 0x3F)); }
    else if (c >= 0xD800 && c <= 0xDBFF) {
      var c2 = str.charCodeAt(++i);
      var cp = 0x10000 + ((c & 0x3FF) << 10) + (c2 & 0x3FF);
      bytes.push(0xF0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3F), 0x80 | ((cp >> 6) & 0x3F), 0x80 | (cp & 0x3F));
    } else { bytes.push(0xE0 | (c >> 12), 0x80 | ((c >> 6) & 0x3F), 0x80 | (c & 0x3F)); }
  }
  return new Uint8Array(bytes);
};
function TextDecoder(){}
TextDecoder.prototype.decode = function(buf){
  var arr = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  var out = "", i = 0;
  while (i < arr.length){
    var c = arr[i++];
    if (c < 0x80) { out += String.fromCharCode(c); }
    else if (c < 0xE0) { out += String.fromCharCode(((c & 0x1F) << 6) | (arr[i++] & 0x3F)); }
    else if (c < 0xF0) { out += String.fromCharCode(((c & 0x0F) << 12) | ((arr[i++] & 0x3F) << 6) | (arr[i++] & 0x3F)); }
    else { var cp = ((c & 0x07) << 18) | ((arr[i++] & 0x3F) << 12) | ((arr[i++] & 0x3F) << 6) | (arr[i++] & 0x3F); cp -= 0x10000; out += String.fromCharCode(0xD800 + (cp >> 10), 0xDC00 + (cp & 0x3FF)); }
  }
  return out;
};
`

// signer runs MangaFire's getProtectionToken in an embedded JS runtime to mint
// the `vrf` token required by the API. goja.Runtime is not safe for concurrent
// use, so all access is serialized through mu. Initialization (esbuild transpile
// + goja parse of the ~140 KB bundle) is deferred until the first Sign call.
type signer struct {
	once    sync.Once
	initErr error

	mu   sync.Mutex
	fn   goja.Callable
	self goja.Value
	rt   *goja.Runtime
}

func newSigner() *signer {
	return &signer{}
}

func (s *signer) init() {
	// esbuild lowers the bundle's modern syntax (async generators, ESM
	// export) to a target goja can parse. The signer's crypto is pure JS, so
	// no WebAssembly/WebCrypto is involved.
	result := esbuild.Transform(signerBundle, esbuild.TransformOptions{
		Loader: esbuild.LoaderJS,
		Format: esbuild.FormatIIFE,
		Target: esbuild.ES2017,
	})
	if len(result.Errors) > 0 {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			msgs = append(msgs, e.Text)
		}
		s.initErr = fmt.Errorf("transpile mangafire signer: %s", strings.Join(msgs, "; "))
		return
	}

	rt := goja.New()
	if _, err := rt.RunString(signerHostShim); err != nil {
		s.initErr = fmt.Errorf("init mangafire signer host shim: %w", err)
		return
	}
	if _, err := rt.RunString(string(result.Code)); err != nil {
		s.initErr = fmt.Errorf("load mangafire signer bundle: %w", err)
		return
	}

	fn, ok := goja.AssertFunction(rt.Get("getProtectionToken"))
	if !ok {
		s.initErr = fmt.Errorf("mangafire signer: getProtectionToken not defined")
		return
	}

	s.rt = rt
	s.fn = fn
	s.self = rt.GlobalObject()
}

// Sign returns the `vrf` token for an API request to path with the given query
// params. path is the request path only (no host, no query); params are the
// query params that will be sent alongside the token (excluding vrf itself).
// getProtectionToken canonicalizes params internally (sorted, values
// stringified), so the token stays stable regardless of param order.
func (s *signer) Sign(path string, params url.Values) (string, error) {
	s.once.Do(s.init)
	if s.initErr != nil {
		return "", s.initErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	obj := s.rt.NewObject()
	for key := range params {
		if err := obj.Set(key, params.Get(key)); err != nil {
			return "", fmt.Errorf("build mangafire signer params: %w", err)
		}
	}

	value, err := s.fn(s.self, s.rt.ToValue(path), obj)
	if err != nil {
		return "", fmt.Errorf("mangafire signer: %w", err)
	}
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return "", fmt.Errorf("mangafire signer returned no token for %q", path)
	}

	token := strings.TrimSpace(value.String())
	if token == "" {
		return "", fmt.Errorf("mangafire signer returned empty token for %q", path)
	}
	return token, nil
}
