window.escapeHtml = function (value) {
    return String(value || '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/\"/g, '&quot;')
        .replace(/'/g, '&#39;');
};

// JSON.parse for the hidden-input payloads: anything that is not a JSON array
// (empty value, corrupt JSON, a bare object) comes back as an empty array.
window.parseJSONArray = function (raw) {
    if (!raw) {
        return [];
    }
    var parsed;
    try {
        parsed = JSON.parse(raw);
    } catch (_) {
        return [];
    }
    return Array.isArray(parsed) ? parsed : [];
};

// The tracker form usually wraps the element that triggered the action, but a
// button rendered outside it (search results, modal chrome) still means the
// one open in the modal.
window.findTrackerForm = function (el) {
    if (!el || !el.closest) {
        return null;
    }
    return el.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form');
};

// Writes a form field and fires the events htmx/listeners expect. null and
// undefined never overwrite; pass allowEmpty to let '' clear a field —
// otherwise '' is treated as "nothing to write" and the field keeps its value.
window.setTrackerFormField = function (form, selector, value, allowEmpty) {
    if (!form || value === undefined || value === null) {
        return;
    }
    if (value === '' && !allowEmpty) {
        return;
    }
    var field = form.querySelector(selector);
    if (!field) {
        return;
    }
    field.value = value;
    field.dispatchEvent(new Event('input', { bubbles: true }));
    field.dispatchEvent(new Event('change', { bubbles: true }));
};

// Guard for hrefs assembled in JS: only absolute http(s) URLs pass, anything
// else (javascript:, data:, relative fragments, garbage) becomes ''.
window.safeHttpUrl = function (value) {
    var raw = String(value || '').trim();
    if (!raw) {
        return '';
    }
    var parsed;
    try {
        parsed = new URL(raw);
    } catch (_) {
        return '';
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
        return '';
    }
    return raw;
};

window.syncTrackerCardHoverState = function (clientX, clientY) {
    if (!document) {
        return;
    }

    var hasCoordinates = typeof clientX === 'number' && typeof clientY === 'number';
    if (hasCoordinates) {
        window.__lastPointerClientX = clientX;
        window.__lastPointerClientY = clientY;
    }

    var activeX = window.__lastPointerClientX;
    var activeY = window.__lastPointerClientY;
    var hasActivePointer = typeof activeX === 'number' && typeof activeY === 'number';
    var hoveredCard = null;

    if (hasActivePointer && typeof document.elementFromPoint === 'function') {
        var elementUnderPointer = document.elementFromPoint(activeX, activeY);
        hoveredCard = elementUnderPointer && elementUnderPointer.closest
            ? elementUnderPointer.closest('.tracker-card')
            : null;
    }

    var cards = document.querySelectorAll('.tracker-card--hovered');
    Array.prototype.forEach.call(cards, function (card) {
        if (card !== hoveredCard) {
            card.classList.remove('tracker-card--hovered');
        }
    });

    if (!hoveredCard) {
        return;
    }

    hoveredCard.classList.add('tracker-card--hovered');
};

document.addEventListener('pointermove', function (event) {
    if (!event || event.pointerType === 'touch') {
        return;
    }

    window.syncTrackerCardHoverState(event.clientX, event.clientY);
});

document.addEventListener('pointerleave', function () {
    window.__lastPointerClientX = null;
    window.__lastPointerClientY = null;
    window.syncTrackerCardHoverState();
}, true);

window.dispatchTrackersChanged = function (reason) {
    if (!document || !document.body) {
        return;
    }

    var kind = String(reason || 'system').toLowerCase();
    if (kind === 'user') {
        window.__freezeTrackersOrder = false;
        window.__pinnedTrackerID = '';

        if (window.__pendingTrackersRefreshTimer) {
            window.clearTimeout(window.__pendingTrackersRefreshTimer);
            window.__pendingTrackersRefreshTimer = null;
        }
    }

    document.body.dispatchEvent(new CustomEvent('trackersChanged', {
        detail: { reason: kind }
    }));
};

window.setDashboardViewMode = function (mode, shouldRefresh) {
    var nextMode = (mode === 'list') ? 'list' : 'grid';
    var viewInput = document.getElementById('view-input');
    var currentMode = viewInput && viewInput.value ? viewInput.value : 'grid';

    if (shouldRefresh && currentMode === nextMode) {
        return;
    }

    if (viewInput) {
        viewInput.value = nextMode;
    }

    var options = document.querySelectorAll('[data-view-mode]');
    Array.prototype.forEach.call(options, function (option) {
        var isActive = option.getAttribute('data-view-mode') === nextMode;
        option.classList.toggle('view-toggle__option--active', isActive);
        option.setAttribute('aria-pressed', isActive ? 'true' : 'false');
    });

    if (!shouldRefresh) {
        return;
    }

    var pageInput = document.getElementById('page-input');
    if (pageInput) {
        pageInput.value = '1';
    }
    window.dispatchTrackersChanged('user');
};

// A mutation that fails leaves htmx doing nothing: 4xx and 5xx responses are
// not swapped, so without this the modal stayed open with its "Saving…"
// indicator gone and no word of what went wrong — a tracker half-saved by a bad
// pasted URL looked exactly like a hung request. The server answers every
// refused request with a one-line message written for the reader (see
// handlers.fail), so that text is what gets shown: beside the modal form that
// sent the request, else in a strip at the top of the trackers zone.
//
// The form is the one the request came from, not the first one in the modal.
// Picking the first put a failed tag change from the Custom Tags pane above the
// Switch-profile form in the other pane, and a refused Save above Title, out of
// sight at the top of a modal that scrolls on its own — so the message goes
// right above the buttons that were pressed, and is scrolled into view.
var requestErrorHost = function (elt) {
    // Without the requesting element only the old guess is possible; with one
    // outside the modal (a card button, the list's own refresh) the message
    // belongs to the trackers zone even while a modal happens to be open.
    var fromModal = !elt || !elt.closest || !!elt.closest('#modal-zone');
    var form = null;
    if (fromModal) {
        form = (elt && elt.closest && elt.closest('#modal-zone form')) ||
            document.querySelector('#modal-zone .tracker-form, #modal-zone form');
    }
    // A tag's Edit/Remove and a logo's Upload/Remove are one-button forms in a
    // row with no room for a sentence; their section is the nearest place it
    // can be read.
    if (form && !form.classList.contains('tracker-form')) {
        form = form.closest('section') || form;
    }
    return form || document.getElementById('trackers-zone') || document.body;
};

var requestErrorAnchor = function (host) {
    var actions = host.querySelector(':scope > .modal-actions');
    if (actions) {
        return actions;
    }
    var heading = host.querySelector(':scope > h3');
    if (heading) {
        return heading.nextSibling;
    }
    return host.firstChild;
};

window.showRequestError = function (message, elt) {
    var text = String(message || '').trim() || 'The request failed. Please try again.';
    var host = requestErrorHost(elt);
    var region = (host.closest && host.closest('#modal-zone')) || host;

    // One message per region: an earlier failure from another form in the
    // same modal would otherwise still be on screen, reading as current.
    Array.prototype.forEach.call(region.querySelectorAll('.request-error'), function (node) {
        node.remove();
    });

    var notice = document.createElement('p');
    notice.className = 'request-error';
    notice.setAttribute('role', 'alert');
    notice.textContent = text;
    host.insertBefore(notice, requestErrorAnchor(host));

    // Only inside the modal, where the user just pressed something: a failed
    // background refresh of the list must not yank the page to its top.
    if (region.id === 'modal-zone' && typeof notice.scrollIntoView === 'function') {
        notice.scrollIntoView({ block: 'nearest' });
    }
};

document.body.addEventListener('htmx:responseError', function (event) {
    var xhr = event && event.detail ? event.detail.xhr : null;
    var body = xhr && typeof xhr.responseText === 'string' ? xhr.responseText.trim() : '';
    // Only a short plain-text answer is a message; anything else (an HTML
    // error page from a proxy) is replaced by the status line.
    var looksLikeMarkup = body.charAt(0) === '<';
    var message = (!looksLikeMarkup && body.length > 0 && body.length <= 300)
        ? body
        : ('Request failed' + (xhr && xhr.status ? ' (' + xhr.status + ')' : ''));
    window.showRequestError(message, event && event.detail ? event.detail.elt : null);
});

document.body.addEventListener('htmx:sendError', function (event) {
    window.showRequestError('Could not reach the server. Check the connection and try again.', event && event.detail ? event.detail.elt : null);
});

// htmx swaps a region by inserting the new nodes before the old ones and only
// then removing the old ones. When the focused element is in the old content —
// a chapter link just tapped to open in another tab, a rating's Save button —
// Chrome's scroll anchoring holds that element in place while the new copy is
// inserted above it, scrolling down by the whole region's height; removing the
// old copy then clamps the page to its end. A silent cover refresh of a phone's
// 20000px list threw a reader at y=6000 to the very bottom. Focus leaves the
// region before the swap, and the anchoring has nothing to hold on to. The
// modal is left alone: it is fixed in place, and htmx restores focus to an
// element with an id there.
var releaseFocusBeforeSwap = function (event) {
    var detail = event && event.detail;
    var region = detail && detail.target;
    if (!region || detail.shouldSwap === false || !region.contains) {
        return;
    }
    var focused = document.activeElement;
    if (!focused || focused === document.body || !region.contains(focused)) {
        return;
    }
    if (region.closest && region.closest('#modal-zone')) {
        return;
    }
    if (typeof focused.blur === 'function') {
        focused.blur();
    }
};
document.body.addEventListener('htmx:beforeSwap', releaseFocusBeforeSwap);
document.body.addEventListener('htmx:oobBeforeSwap', releaseFocusBeforeSwap);

// A successful swap into the modal or the trackers zone clears any error strip
// left there by an earlier attempt.
document.body.addEventListener('htmx:afterSwap', function (event) {
    var target = event && event.target;
    if (!target || !target.querySelectorAll) {
        return;
    }
    Array.prototype.forEach.call(target.querySelectorAll('.request-error'), function (node) {
        node.remove();
    });
});
