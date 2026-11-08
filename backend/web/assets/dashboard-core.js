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

// "user" is the reader asking for another list; "silent" is the page bringing
// the list it already shows up to date (covers, a count after a delete), which
// shows no skeleton and reports no failure; "system" is anything else.
window.dispatchTrackersChanged = function (reason) {
    if (!document || !document.body) {
        return;
    }

    var kind = String(reason || 'system').toLowerCase();
    if (kind === 'user') {
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

// Whether a trackersChanged event asks for a silent refresh. The delete
// response raises a plain one after setting window.__silentCoverRefresh, which
// dashboard-trackers.js reads as the filters form confirms the request.
window.isQuietTrigger = function (triggeringEvent) {
    return !!(triggeringEvent && triggeringEvent.detail && triggeringEvent.detail.reason === 'silent');
};

// A silent refresh is sent by its own element, #trackers-quiet-refresh (see
// dashboard_page.html), not by the filters form. The element that sent a
// request is read from the xhr as well, because htmx rewrites detail.elt to the
// target for the swap events.
window.isQuietListRequest = function (detail) {
    if (!detail) {
        return false;
    }
    var source = (detail.xhr && detail.xhr.__requestElt) || detail.elt;
    return !!(source && source.id === 'trackers-quiet-refresh');
};

// A mutation that fails leaves htmx doing nothing: 4xx and 5xx responses are
// not swapped, so without this the modal stayed open with its "Saving…"
// indicator gone and no word of what went wrong — a tracker half-saved by a bad
// pasted URL looked exactly like a hung request. The server answers every
// refused request with a one-line message written for the reader (see
// handlers.fail), so that text is what gets shown: beside the modal form that
// sent the request, beside the card button that was pressed, else in a strip
// at the top of the trackers zone.
//
// The message goes next to what was pressed, because that is where the reader
// is looking. A card's Delete or Set last read used to report at the top of the
// trackers zone, thousands of pixels above a reader working down the list, and
// a Refresh in the Sources modal behind the modal's own overlay. In a modal it
// is the form the request came from, not the first one in the modal: picking
// the first put a failed tag change from the Custom Tags pane above the
// Switch-profile form in the other pane, and a refused Save above Title, out of
// sight at the top of a modal that scrolls on its own.
//
// The answer is where to put it: host, the node to insert before (null for the
// end), and whether the host's contents are stale and go with it.
var requestErrorPlacement = function (elt, target) {
    // A title search reports by its own results box, and the results of the
    // previous query go: listed under a failure, they read as its answer.
    if (target && target.classList && target.classList.contains('source-search-results')) {
        return { host: target, before: null, replaceContents: true };
    }

    var modalCard = elt && elt.closest ? elt.closest('#modal-zone .modal-card') : null;
    var card = elt && elt.closest ? elt.closest('.tracker-card') : null;

    // Without the requesting element only the old guess is possible.
    if (!elt || !elt.closest) {
        modalCard = document.querySelector('#modal-zone .modal-card');
    }

    if (modalCard) {
        var form = (elt && elt.closest && elt.closest('form')) ||
            (!elt ? modalCard.querySelector('.tracker-form, form') : null);
        if (form && form.classList.contains('tracker-form')) {
            return { host: form, before: form.querySelector(':scope > .modal-actions') || form.firstChild };
        }
        // A tag's Edit/Remove and a logo's Upload/Remove are one-button forms
        // in a row with no room for a sentence; their section is the nearest
        // place it can be read.
        var section = form ? (form.closest('section') || form) : null;
        if (section) {
            var heading = section.querySelector(':scope > h3');
            return { host: section, before: heading ? heading.nextSibling : section.firstChild };
        }
        // A button that belongs to no form (the Sources modal's Refresh): just
        // under the modal's header, inside the card the overlay leaves in sight.
        var header = modalCard.querySelector(':scope > header');
        return { host: modalCard, before: header ? header.nextSibling : modalCard.firstChild };
    }

    if (card) {
        var popover = elt.closest('.tracker-rating__popover');
        if (popover) {
            return { host: popover, before: popover.querySelector(':scope > .tracker-rating__actions') };
        }
        var actions = elt.closest('.card-actions');
        if (actions && actions.parentNode === card) {
            return { host: card, before: actions.nextSibling };
        }
        return { host: card, before: null };
    }

    var zone = document.getElementById('trackers-zone') || document.body;
    return { host: zone, before: zone.firstChild };
};

window.showRequestError = function (message, elt, target) {
    var text = String(message || '').trim() || 'The request failed. Please try again.';
    var placement = requestErrorPlacement(elt, target);
    var host = placement.host;
    var region = (host.closest && host.closest('#modal-zone')) || host;

    // One message at a time: an earlier failure from another form in the
    // same modal, or on another card, would otherwise still be on screen,
    // reading as current.
    Array.prototype.forEach.call(document.querySelectorAll('.request-error'), function (node) {
        node.remove();
    });
    // So does a success notice: "Tag saved" above the refusal of the next
    // tag read as if both had happened to the same request.
    if (region.id === 'modal-zone') {
        Array.prototype.forEach.call(region.querySelectorAll('.profile-feedback'), function (node) {
            node.remove();
        });
    }

    if (placement.replaceContents) {
        host.innerHTML = '';
    }

    var notice = document.createElement('p');
    notice.className = 'request-error';
    notice.setAttribute('role', 'alert');
    notice.textContent = text;
    var before = placement.before && placement.before.parentNode === host ? placement.before : null;
    host.insertBefore(notice, before);

    // Only beside something the user just pressed (the modal, a card): a
    // failed refresh of the list must not yank the page to its top.
    if (host.id !== 'trackers-zone' && host !== document.body && typeof notice.scrollIntoView === 'function') {
        notice.scrollIntoView({ block: 'nearest' });
    }
};

// A silent refresh is the page's own doing, not the reader's: its failure is
// left to dashboard-trackers.js, which tries again later, rather than reported
// as "Could not reach the server" over a list the reader did nothing to.
document.body.addEventListener('htmx:responseError', function (event) {
    var detail = event && event.detail;
    if (window.isQuietListRequest(detail)) {
        return;
    }
    var xhr = detail ? detail.xhr : null;
    var body = xhr && typeof xhr.responseText === 'string' ? xhr.responseText.trim() : '';
    // Only a short plain-text answer is a message; anything else (an HTML
    // error page from a proxy) is replaced by the status line.
    var looksLikeMarkup = body.charAt(0) === '<';
    var message = (!looksLikeMarkup && body.length > 0 && body.length <= 300)
        ? body
        : ('Request failed' + (xhr && xhr.status ? ' (' + xhr.status + ')' : ''));
    window.showRequestError(message, detail ? detail.elt : null, detail ? detail.target : null);
});

document.body.addEventListener('htmx:sendError', function (event) {
    var detail = event && event.detail;
    if (window.isQuietListRequest(detail)) {
        return;
    }
    window.showRequestError('Could not reach the server. Check the connection and try again.', detail ? detail.elt : null, detail ? detail.target : null);
});

// A request sent again takes the message about the last attempt off the
// screen at once: a refusal left up while the retried Save was in flight read
// as the answer to the retry. A pick from the search results means the reader
// has moved on from a failed search too.
document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.elt || window.isQuietListRequest(detail)) {
        return;
    }
    var host = requestErrorPlacement(detail.elt, detail.target).host;
    if (!host || host.id === 'trackers-zone' || host === document.body) {
        return;
    }
    Array.prototype.forEach.call(host.querySelectorAll('.request-error'), function (node) {
        node.remove();
    });
});

document.addEventListener('click', function (event) {
    var pick = event.target && event.target.closest ? event.target.closest('.search-result-item') : null;
    var form = pick ? pick.closest('form') : null;
    if (!form) {
        return;
    }
    Array.prototype.forEach.call(form.querySelectorAll('.source-search-results .request-error'), function (node) {
        node.remove();
    });
});

// HX-Trigger events are dispatched on the element that sent the request. A
// modal closed while its Save was in flight takes that element out of the
// page, and an event dispatched on a detached element reaches no listener: the
// tracker was created but the page never heard of it, so it never appeared and
// the count stayed as it was. Each request watches its own element, and when
// the answer comes back to an element no longer in the page, the events are
// raised on <body>, where everything listens. htmx dispatches them on the
// element right after htmx:beforeOnLoad, which still reaches a detached
// element's own listeners.
var forwardResponseTriggers = function (xhr) {
    var header = xhr && typeof xhr.getResponseHeader === 'function' ? xhr.getResponseHeader('HX-Trigger') : null;
    if (!header) {
        return;
    }
    var triggers = {};
    if (header.charAt(0) === '{') {
        try {
            triggers = JSON.parse(header);
        } catch (_) {
            return;
        }
    } else {
        header.split(',').forEach(function (name) {
            if (name.trim()) {
                triggers[name.trim()] = {};
            }
        });
    }
    Object.keys(triggers).forEach(function (name) {
        var detail = triggers[name];
        if (detail === null || typeof detail !== 'object' || Array.isArray(detail)) {
            detail = { value: detail };
        }
        if (window.htmx && typeof window.htmx.trigger === 'function') {
            window.htmx.trigger(document.body, name, detail);
        } else {
            document.body.dispatchEvent(new CustomEvent(name, { bubbles: true, detail: detail }));
        }
    });
};

document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    var elt = detail && detail.elt;
    var xhr = detail && detail.xhr;
    if (!elt || !xhr || typeof elt.addEventListener !== 'function') {
        return;
    }
    xhr.__requestElt = elt;
    var onLoad = function (loadEvent) {
        // The answer to a request from inside the element (the form's own
        // Cancel button) bubbles through it too, and is not this one.
        if (!loadEvent || loadEvent.target !== elt) {
            return;
        }
        elt.removeEventListener('htmx:beforeOnLoad', onLoad);
        // A listener left by a request that was aborted before its answer
        // came is dropped by the next answer, which is not its own.
        if (!loadEvent.detail || loadEvent.detail.xhr !== xhr) {
            return;
        }
        if (!document.body.contains(elt)) {
            forwardResponseTriggers(xhr);
        }
    };
    elt.addEventListener('htmx:beforeOnLoad', onLoad);
});

// The same closed modal's answer is still swapped into #modal-zone. When the
// reader has opened another modal in the meantime, that one would be wiped by
// the answer to the one they closed; the answer goes into a hidden holder
// instead, where its out-of-band card and its scripts still do their work.
document.body.addEventListener('htmx:beforeSwap', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'modal-zone' || !detail.xhr || detail.shouldSwap === false) {
        return;
    }
    var elt = detail.xhr.__requestElt;
    if (!elt || document.body.contains(elt) || !detail.target.querySelector('.modal-backdrop')) {
        return;
    }
    var holder = document.createElement('div');
    holder.hidden = true;
    document.body.appendChild(holder);
    detail.target = holder;
    window.setTimeout(function () {
        holder.remove();
    }, 1000);
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

// Every modal comes through #modal-zone, so they are all made to behave as
// dialogs here rather than in each template. Before this a modal was only a
// box drawn over the page: focus stayed on the button behind it (the Edit form
// was 195 Tab presses away), Tab walked out of it into the list, Escape did
// nothing, closing it dropped focus to <body>, and a wheel or swipe past the
// end of its content scrolled the page underneath.
var modalState = {
    open: false,
    cardClass: '',
    scroll: null,
    opener: null,
    openerScope: '',
    openerSelector: ''
};

var currentModalCard = function () {
    return document.querySelector('#modal-zone .modal-backdrop .modal-card');
};

var cssAttributeValue = function (value) {
    return String(value).replace(/["\\]/g, '\\$&');
};

// The button that opened the modal is often gone by the time it closes — a
// saved Edit replaces its card, the Sources pill re-renders itself every
// minute — so how to find its successor is kept beside it: the nearest
// ancestor with an id and the request the button makes.
var rememberModalOpener = function (elt) {
    modalState.opener = elt;
    modalState.openerScope = '';
    modalState.openerSelector = '';
    var attrs = ['hx-get', 'hx-post'];
    for (var i = 0; i < attrs.length; i += 1) {
        var value = elt.getAttribute(attrs[i]);
        if (value) {
            modalState.openerSelector = '[' + attrs[i] + '="' + cssAttributeValue(value) + '"]';
            break;
        }
    }
    var scope = elt.parentElement && elt.parentElement.closest ? elt.parentElement.closest('[id]') : null;
    modalState.openerScope = scope ? scope.id : '';
};

var focusModalOpener = function () {
    var target = modalState.opener;
    if (!target || !document.body.contains(target)) {
        target = null;
        if (modalState.openerSelector) {
            var scope = modalState.openerScope ? document.getElementById(modalState.openerScope) : document;
            target = scope ? scope.querySelector(modalState.openerSelector) : null;
        }
    }
    modalState.opener = null;
    if (target && typeof target.focus === 'function') {
        target.focus({ preventScroll: true });
    }
};

// The page behind is made inert (out of the Tab order and of reach) and stops
// scrolling: overflow hidden on the root keeps the reader's offset, and the
// scrollbar's width is given back as padding so the page does not shift
// sideways when it goes.
var lockPageBehindModal = function (locked) {
    var root = document.documentElement;
    var shell = document.querySelector('.shell');
    if (locked) {
        if (!root.classList.contains('modal-open')) {
            var gap = window.innerWidth - root.clientWidth;
            root.classList.add('modal-open');
            if (gap > 0) {
                document.body.style.paddingRight = gap + 'px';
            }
        }
        if (shell) {
            shell.setAttribute('inert', '');
        }
        return;
    }
    root.classList.remove('modal-open');
    document.body.style.paddingRight = '';
    if (shell) {
        shell.removeAttribute('inert');
    }
};

var describeModalCard = function (card) {
    card.setAttribute('role', 'dialog');
    card.setAttribute('aria-modal', 'true');
    card.setAttribute('tabindex', '-1');
    var heading = card.querySelector('h2');
    if (heading) {
        if (!heading.id) {
            heading.id = 'modal-title';
        }
        card.setAttribute('aria-labelledby', heading.id);
    }
};

// Called after anything changes #modal-zone: an htmx swap, or the backdrop
// click and Escape that empty it directly.
window.syncModalZone = function () {
    var card = currentModalCard();
    if (card) {
        describeModalCard(card);
        lockPageBehindModal(true);
        // The Profile menu and the Sources modal answer their own actions by
        // re-rendering themselves whole, which put a phone reader 544px down
        // the menu back at its top after every tag or logo change. The same
        // modal coming back keeps its scroll, and focus stays inside it.
        var reRendered = modalState.open && modalState.cardClass === card.className;
        if (reRendered && modalState.scroll) {
            card.scrollTop = modalState.scroll.card;
            var backdrop = card.closest('.modal-backdrop');
            if (backdrop) {
                backdrop.scrollTop = modalState.scroll.backdrop;
            }
        }
        if (!card.contains(document.activeElement)) {
            card.focus({ preventScroll: true });
        }
        modalState.open = true;
        modalState.cardClass = card.className;
        modalState.scroll = null;
        return;
    }
    if (modalState.open) {
        modalState.open = false;
        modalState.cardClass = '';
        modalState.scroll = null;
        lockPageBehindModal(false);
        focusModalOpener();
    }
};

document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'modal-zone' || !detail.elt || !detail.elt.getAttribute) {
        return;
    }
    if (modalState.open || (detail.elt.closest && detail.elt.closest('#modal-zone'))) {
        return;
    }
    rememberModalOpener(detail.elt);
});

document.body.addEventListener('htmx:beforeSwap', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'modal-zone' || detail.shouldSwap === false) {
        return;
    }
    var card = currentModalCard();
    var backdrop = card ? card.closest('.modal-backdrop') : null;
    modalState.scroll = card ? { card: card.scrollTop, backdrop: backdrop ? backdrop.scrollTop : 0 } : null;
});

document.body.addEventListener('htmx:afterSwap', function (event) {
    var target = event && event.target;
    if (target && target.id === 'modal-zone') {
        window.syncModalZone();
    }
});

var modalFocusables = function (card) {
    var selector = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';
    return Array.prototype.filter.call(card.querySelectorAll(selector), function (node) {
        if (node.getClientRects().length === 0) {
            return false;
        }
        // Only the checked radio of a group is in the Tab order.
        return !(node.type === 'radio' && !node.checked);
    });
};

document.addEventListener('keydown', function (event) {
    if (!event || event.defaultPrevented || event.isComposing) {
        return;
    }

    if (event.key === 'Tab') {
        var card = currentModalCard();
        if (!card) {
            return;
        }
        var items = modalFocusables(card);
        var active = document.activeElement;
        if (items.length === 0) {
            event.preventDefault();
            card.focus({ preventScroll: true });
            return;
        }
        var first = items[0];
        var last = items[items.length - 1];
        if (!card.contains(active)) {
            event.preventDefault();
            (event.shiftKey ? last : first).focus();
        } else if (event.shiftKey && (active === first || active === card)) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && active === last) {
            event.preventDefault();
            first.focus();
        }
        return;
    }

    if (event.key !== 'Escape') {
        return;
    }

    if (currentModalCard()) {
        event.preventDefault();
        if (typeof window.dismissModalZone === 'function') {
            window.dismissModalZone();
        }
        return;
    }

    // Outside a modal Escape closes whatever menu is open — the rating
    // popover, the connector "+N" menu, the Sites and Tags dropdowns — and puts
    // focus back on its toggle if it was inside.
    var openMenus = document.querySelectorAll('details.tracker-rating[open], details.site-links-more[open], details.filter-multi-select[open]');
    if (openMenus.length === 0) {
        return;
    }
    event.preventDefault();
    Array.prototype.forEach.call(openMenus, function (menu) {
        var hadFocus = menu.contains(document.activeElement);
        menu.removeAttribute('open');
        var summary = menu.querySelector(':scope > summary');
        if (hadFocus && summary) {
            summary.focus({ preventScroll: true });
        }
    });
});
