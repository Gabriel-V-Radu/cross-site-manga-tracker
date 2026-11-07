// The Link Review page's answer to a failed request. htmx does not swap a 4xx
// or 5xx response, and this page loads none of the dashboard's scripts, so
// every refusal here — a card whose candidate another tab or a rescan already
// settled, a queue that failed to load, a dropped connection on the phone —
// left the page exactly as it was, with no word of what happened. The server
// answers each refusal with a one-line message written for the reader (see
// handlers.fail), and that text is shown next to what the click was about:
// inside the card for a card's own controls, in the status line for the scan
// controls, and at the top of the queue for everything else.
//
// A message is placed inside the element its request swaps (the card, the
// status line, the queue's content), so the next successful swap of that same
// target replaces it with the rest; a card's is also cleared as the card's
// next request starts. The status line is the exception: it is re-rendered by
// its own polls every two seconds, which wiped a failed Stop's message before
// it could be read, so that one is carried over for a while (see
// heldStatusError).
//
// The rest of this file keeps the page steady around htmx's swaps: focus, a
// URL being typed into a card, a row whose section changed, the scope in the
// page's URL, and the scroll position across a reload.
(function () {
    'use strict';

    function messageFromResponse(xhr) {
        var body = xhr && typeof xhr.responseText === 'string' ? xhr.responseText.trim() : '';
        // Only a short plain-text answer is a message; anything else (an HTML
        // error page from a proxy) is replaced by the status line.
        if (body.length > 0 && body.length <= 300 && body.charAt(0) !== '<') {
            return body;
        }
        return 'Request failed' + (xhr && xhr.status ? ' (' + xhr.status + ')' : '');
    }

    // existingOrNew reuses the node an earlier failure left in host, so
    // repeated failures (a poll every two seconds while the phone is offline)
    // update one message instead of stacking them.
    function existingOrNew(host, selector, tagName, className, prepend) {
        var node = host.querySelector(selector);
        if (!node) {
            node = document.createElement(tagName);
            node.className = className;
            node.setAttribute('data-request-error', '');
            if (prepend) {
                host.insertBefore(node, host.firstChild);
            } else {
                host.appendChild(node);
            }
        }
        node.setAttribute('role', 'alert');
        return node;
    }

    // A failed request on the status line (Stop, Scan, a poll) is shown again
    // on each re-render of the line for this long — a few polls — unless a
    // Scan or Stop goes through first.
    var STATUS_ERROR_HOLD_MS = 10000;
    var heldStatusError = null;

    function showStatusError(status, message) {
        existingOrNew(status, '[data-request-error]', 'span', 'link-scan-status__error', false).textContent = message;
    }

    function showRequestError(detail, message) {
        var elt = detail && detail.elt;
        var target = detail && detail.target;

        // A card's buttons and its manual-link form swap the card itself. The
        // card already has a slot for a failed manual link; a stale message in
        // it is overwritten rather than shown twice.
        var card = elt && elt.closest ? elt.closest('.link-card') : null;
        if (card) {
            var tracker = card.querySelector('.link-card__tracker') || card;
            existingOrNew(tracker, '.link-card__error', 'p', 'link-card__error', false).textContent = message;
            return;
        }

        // Scan, Stop and the status polls swap the status line. The server's
        // own scan error shares the class, so the lookup is by marker.
        var status = target && target.closest ? target.closest('#link-scan-status') : null;
        if (status) {
            showStatusError(status, message);
            heldStatusError = { message: message, until: Date.now() + STATUS_ERROR_HOLD_MS };
            return;
        }

        // Queue loads, scope changes and the bulk accept swap the queue's
        // content: the message goes on top, above the summary.
        var queue = document.getElementById('links-queue') || document.body;
        existingOrNew(queue, ':scope > [data-request-error]', 'p', 'request-error', true).textContent = message;
    }

    function isQueueRequest(detail) {
        var target = detail && detail.target;
        return !!(target && target.id === 'links-queue');
    }

    document.body.addEventListener('htmx:responseError', function (event) {
        var detail = event && event.detail;
        if (isQueueRequest(detail)) {
            pendingScroll = null;
        }
        showRequestError(detail, messageFromResponse(detail ? detail.xhr : null));
    });

    document.body.addEventListener('htmx:sendError', function (event) {
        var detail = event && event.detail;
        if (isQueueRequest(detail)) {
            pendingScroll = null;
        }
        showRequestError(detail, 'Could not reach the server. Check the connection and try again.');
    });

    // A refused card action leaves the card in place, message and all. The
    // next try from that card clears it as it starts: a pasted URL can take
    // the full resolve timeout, and the old refusal stood next to the new URL
    // the whole time, read as the answer to it.
    document.body.addEventListener('htmx:beforeRequest', function (event) {
        var elt = event && event.detail && event.detail.elt;
        var card = elt && elt.closest ? elt.closest('.link-card') : null;
        if (!card) {
            return;
        }
        var stale = card.querySelectorAll('.link-card__error');
        for (var i = 0; i < stale.length; i++) {
            stale[i].parentNode.removeChild(stale[i]);
        }
    });

    // A URL half typed into a card's "Paste a series URL" box was wiped by
    // the queue refresh at the end of a scan, and by any re-render of its
    // card. What is typed is kept by card id across the swap and put back into
    // the same card, with the caret, if it is still there.
    var typedURLs = null;

    function rememberTypedURLs(region) {
        if (!region.querySelectorAll) {
            return;
        }
        var inputs = region.querySelectorAll('.link-manual input[name="url"]');
        for (var i = 0; i < inputs.length; i++) {
            var input = inputs[i];
            var card = input.closest ? input.closest('.link-card') : null;
            if (!card || !card.id || input.value.trim() === '') {
                continue;
            }
            typedURLs = typedURLs || {};
            typedURLs[card.id] = {
                value: input.value,
                focused: document.activeElement === input,
                start: input.selectionStart,
                end: input.selectionEnd
            };
        }
    }

    function restoreTypedURLs() {
        var saved = typedURLs;
        typedURLs = null;
        if (!saved) {
            return;
        }
        Object.keys(saved).forEach(function (cardId) {
            var card = document.getElementById(cardId);
            var input = card ? card.querySelector('.link-manual input[name="url"]') : null;
            if (!input || input.value !== '') {
                return;
            }
            var entry = saved[cardId];
            input.value = entry.value;
            // Focus comes back only to a reader who has not moved it since,
            // and without scrolling: the jump this script exists to prevent
            // came from focus, not from where it lands afterwards.
            var active = document.activeElement;
            if (entry.focused && (!active || active === document.body)) {
                input.focus({ preventScroll: true });
                try {
                    input.setSelectionRange(entry.start, entry.end);
                } catch (_) {
                    // Not every input type has a selection; the value is back.
                }
            }
        });
    }

    // htmx swaps a region by inserting the new nodes before the old ones and
    // only then removing the old ones. With focus in the old content — a URL
    // being pasted into a card, an "Open on" link just clicked — Chrome's
    // scroll anchoring holds that element in place while the new queue is
    // inserted above it, and removing the old queue then clamps the page to
    // its end: the refresh at the end of a scan threw a reviewer at the 40th
    // card to the bottom of a 70000px page. Focus leaves the region first.
    // (dashboard-core.js does the same for the dashboard.)
    function releaseFocusBeforeSwap(event) {
        var detail = event && event.detail;
        var region = detail && detail.target;
        if (!region || detail.shouldSwap === false || !region.contains) {
            return;
        }
        var focused = document.activeElement;
        if (!focused || focused === document.body || !region.contains(focused)) {
            return;
        }
        if (typeof focused.blur === 'function') {
            focused.blur();
        }
    }

    document.body.addEventListener('htmx:beforeSwap', function (event) {
        var detail = event && event.detail;
        var region = detail && detail.target;
        if (region && detail.shouldSwap !== false) {
            // Typed URLs are read before focus is released, so the caret's
            // place is known. A Scan or Stop that went through ends the hold
            // on an earlier failure: its message no longer describes the line.
            rememberTypedURLs(region);
            var config = detail.requestConfig;
            if (region.id === 'link-scan-status' && !detail.isError && config && config.verb === 'post') {
                heldStatusError = null;
            }
        }
        releaseFocusBeforeSwap(event);
    });
    document.body.addEventListener('htmx:oobBeforeSwap', releaseFocusBeforeSwap);

    // Rejecting a tracker's last candidate re-renders its card as a "no
    // candidate" row, marked with the row it belongs in front of. Left where
    // the card was, it sat under "Candidates to review" until a reload; it
    // moves into the "No candidate found" list instead, in title order. It is
    // moved as soon as it is swapped in, before htmx processes it, so its
    // controls are wired up where they end up.
    function moveRowToItsSection(row) {
        if (!row.hasAttribute || !row.hasAttribute('data-link-move-before')) {
            return;
        }
        var before = row.getAttribute('data-link-move-before');
        row.removeAttribute('data-link-move-before');
        var list = document.getElementById('links-nomatch-list');
        if (!list) {
            return;
        }
        var anchor = before && before !== '0' ? document.getElementById('link-card-' + before) : null;
        if (anchor && anchor.parentNode === list) {
            list.insertBefore(row, anchor);
        } else {
            list.appendChild(row);
        }
    }

    document.body.addEventListener('htmx:afterSwap', function (event) {
        var swapped = event && event.target;
        if (!swapped) {
            return;
        }
        moveRowToItsSection(swapped);
        restoreTypedURLs();

        if (swapped.id === 'link-scan-status' && heldStatusError) {
            if (Date.now() < heldStatusError.until) {
                showStatusError(swapped, heldStatusError.message);
            } else {
                heldStatusError = null;
            }
        }

        if (swapped.id === 'links-queue' && pendingScroll) {
            restoreScroll();
        }
    });

    // The scope lives in the page's URL as well as in the selects, so a
    // reload opens on what the reader was looking at: the server used to
    // reopen every reload on the last scan's scope, undoing a filter changed
    // since without a word. The first change also retires the note saying the
    // filters were preset from that scan.
    function syncScopeToURL() {
        var form = document.getElementById('links-scope');
        var history = window.history;
        if (!form || !history || typeof history.replaceState !== 'function' || typeof window.URLSearchParams !== 'function') {
            return;
        }
        var params = new window.URLSearchParams(window.location.search);
        var selects = form.querySelectorAll('select[name]');
        for (var i = 0; i < selects.length; i++) {
            params.set(selects[i].name, selects[i].value);
        }
        try {
            history.replaceState(history.state, '', window.location.pathname + '?' + params.toString() + window.location.hash);
        } catch (_) {
            // A refused history write only costs the reload its filters.
        }
    }

    var scopeForm = document.getElementById('links-scope');
    if (scopeForm) {
        scopeForm.addEventListener('change', function () {
            var hint = scopeForm.querySelector('[data-scope-hint]');
            if (hint && hint.parentNode) {
                hint.parentNode.removeChild(hint);
            }
            syncScopeToURL();
        });
    }

    // A reload starts the page with an empty queue — it loads after the load
    // event — so the browser's own scroll restoration found a page one screen
    // tall and left the reader at the top, or at the bottom of that short
    // page on a phone. The browser is told not to restore; the offset is put
    // back once the queue has arrived, unless the reader has scrolled by then
    // or the queue failed to load. The key is the URL as loaded, scope
    // included: a reload or Back returns to the same list, while following
    // the dashboard's link starts at the top. (dashboard-trackers.js does the
    // same for the dashboard.)
    var scrollKey = 'links-scroll:' + window.location.pathname + window.location.search;
    var pendingScroll = null;

    try {
        if ('scrollRestoration' in window.history) {
            window.history.scrollRestoration = 'manual';
        }
        var savedScroll = JSON.parse(window.sessionStorage.getItem(scrollKey) || 'null');
        window.sessionStorage.removeItem(scrollKey);
        if (savedScroll && typeof savedScroll.y === 'number' && savedScroll.y > 0) {
            pendingScroll = savedScroll;
        }
    } catch (_) {
        pendingScroll = null;
    }

    function saveScroll() {
        try {
            window.sessionStorage.setItem('links-scroll:' + window.location.pathname + window.location.search,
                JSON.stringify({ y: Math.round(window.scrollY || 0) }));
        } catch (_) {
            // Storage refused (private mode, quota): the reload starts at the top.
        }
    }

    window.addEventListener('pagehide', saveScroll);
    // A discarded background tab may never see pagehide; hidden is the last
    // moment that is sure to run.
    document.addEventListener('visibilitychange', function () {
        if (document.visibilityState === 'hidden') {
            saveScroll();
        }
    });

    function restoreScroll() {
        var saved = pendingScroll;
        pendingScroll = null;
        if (!saved || window.scrollY > 0) {
            return;
        }
        window.scrollTo(0, saved.y);
    }

    // After the scroll key is read: the URL as loaded may lack the scope the
    // server chose (the dashboard's link carries none).
    syncScopeToURL();
})();
