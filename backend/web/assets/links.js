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
// Nothing here has to clear a message: each one is placed inside the element
// its request swaps (the card, the status line, the queue's content), so the
// next successful swap of that same target replaces it with the rest.
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
            existingOrNew(status, '[data-request-error]', 'span', 'link-scan-status__error', false).textContent = message;
            return;
        }

        // Queue loads, scope changes and the bulk accept swap the queue's
        // content: the message goes on top, above the summary.
        var queue = document.getElementById('links-queue') || document.body;
        existingOrNew(queue, ':scope > [data-request-error]', 'p', 'request-error', true).textContent = message;
    }

    document.body.addEventListener('htmx:responseError', function (event) {
        var detail = event && event.detail;
        showRequestError(detail, messageFromResponse(detail ? detail.xhr : null));
    });

    document.body.addEventListener('htmx:sendError', function (event) {
        showRequestError(event && event.detail, 'Could not reach the server. Check the connection and try again.');
    });
})();
