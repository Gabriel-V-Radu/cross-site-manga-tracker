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
// handlers.fail), so that text is what gets shown, inside whatever the request
// was going to swap into: the modal's form when one is open, else a strip at
// the top of the trackers zone.
window.showRequestError = function (message) {
    var text = String(message || '').trim() || 'The request failed. Please try again.';
    var form = document.querySelector('#modal-zone .tracker-form, #modal-zone form');
    var host = form || document.getElementById('trackers-zone') || document.body;
    var existing = host.querySelector(':scope > .request-error');
    if (!existing) {
        existing = document.createElement('p');
        existing.className = 'request-error';
        existing.setAttribute('role', 'alert');
        host.insertBefore(existing, host.firstChild);
    }
    existing.textContent = text;
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
    window.showRequestError(message);
});

document.body.addEventListener('htmx:sendError', function () {
    window.showRequestError('Could not reach the server. Check the connection and try again.');
});

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
