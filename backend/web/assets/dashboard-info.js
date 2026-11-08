// The series info panel a card's cover opens: type, status, genres, synopsis,
// scores and a link to every linked site (tracker_info_popover.html).
//
// - With a mouse it opens after the pointer rests on a cover for a moment, so
//   sweeping across the grid does not flash a panel per card, and closes when
//   the pointer leaves both the cover and the panel.
// - A click (or a tap, or Enter on the focused cover) opens it pinned: it then
//   stays until a click outside, Escape, or another cover.
// - On a narrow screen it is a sheet along the bottom instead of a panel
//   beside the card, which has no room there.
//
// The panel's HTML is fetched once per card and kept for a minute: the server
// answers from stored data only, but a reader hovering back and forth should
// not wait for it twice.
(function () {
    var HOVER_OPEN_DELAY = 450;
    var HOVER_CLOSE_DELAY = 220;
    var CACHE_MS = 60 * 1000;
    var PANEL_GAP = 12;

    var cache = {};
    var openTimer = null;
    var closeTimer = null;
    var current = { trigger: null, url: '', pinned: false, request: 0 };

    var panel = function () {
        return document.getElementById('tracker-info-popover');
    };

    var narrowScreen = function () {
        return window.matchMedia && window.matchMedia('(max-width: 700px)').matches;
    };

    var profileQuery = function (url) {
        var input = document.getElementById('profile-filter');
        var key = input && input.value ? String(input.value).trim() : '';
        if (!key) {
            return url;
        }
        return url + (url.indexOf('?') === -1 ? '?' : '&') + 'profile=' + encodeURIComponent(key);
    };

    var clearTimers = function () {
        if (openTimer) {
            window.clearTimeout(openTimer);
            openTimer = null;
        }
        if (closeTimer) {
            window.clearTimeout(closeTimer);
            closeTimer = null;
        }
    };

    var fetchPanel = function (url) {
        var entry = cache[url];
        if (entry && (Date.now() - entry.at) < CACHE_MS) {
            return Promise.resolve(entry.html);
        }
        return fetch(profileQuery(url), {
            credentials: 'same-origin',
            headers: { 'HX-Request': 'true' }
        }).then(function (response) {
            if (!response.ok) {
                throw new Error('info request failed');
            }
            return response.text();
        }).then(function (html) {
            cache[url] = { html: html, at: Date.now() };
            return html;
        });
    };

    // Beside the cover: to its right when there is room, else to its left,
    // top-aligned with it and kept inside the viewport vertically. Page
    // coordinates, so the panel scrolls with the card it belongs to.
    //
    // A trigger no longer in the page is never measured: a detached element
    // reports a zero rectangle, and the panel used to fly to the page's
    // top-left corner when "Read more" re-placed it after a silent refresh had
    // swapped the card for a new copy. The panel then stays where it is.
    var place = function (node, trigger) {
        node.classList.toggle('tracker-info-popover--sheet', narrowScreen());
        if (narrowScreen()) {
            node.style.left = '';
            node.style.top = '';
            return;
        }
        if (!trigger || !document.body.contains(trigger)) {
            return;
        }
        var rect = trigger.getBoundingClientRect();
        var width = node.offsetWidth;
        var height = node.offsetHeight;
        var viewportWidth = document.documentElement.clientWidth;
        var viewportHeight = window.innerHeight;

        var left = rect.right + PANEL_GAP;
        if (left + width > viewportWidth - 8) {
            left = rect.left - PANEL_GAP - width;
        }
        if (left < 8) {
            left = Math.max(8, Math.min(viewportWidth - width - 8, rect.left));
        }
        var top = rect.top;
        if (top + height > viewportHeight - 8) {
            top = viewportHeight - height - 8;
        }
        if (top < 8) {
            top = 8;
        }
        node.style.left = Math.round(left + window.scrollX) + 'px';
        node.style.top = Math.round(top + window.scrollY) + 'px';
    };

    // The card the panel belongs to, as it stands now. A silent refresh of the
    // list (covers arriving, a poll's new chapter) replaces every card with a
    // new copy; the copy carries the same info URL, so the panel follows it
    // instead of holding on to an element that has left the page.
    var liveTrigger = function () {
        if (current.trigger && document.body.contains(current.trigger)) {
            return current.trigger;
        }
        if (!current.url) {
            return null;
        }
        var fresh = document.querySelector('.tracker-card__info-trigger[data-info-url="' + current.url.replace(/["\\]/g, '\\$&') + '"]');
        if (fresh) {
            current.trigger = fresh;
            fresh.setAttribute('aria-expanded', 'true');
        }
        return fresh;
    };

    // The synopsis is clamped to a few lines; "Read more" appears only when
    // the clamp actually hid something.
    var setUpDescription = function (node) {
        var description = node.querySelector('.tracker-info__description');
        var toggle = node.querySelector('[data-toggle-description]');
        if (!description || !toggle) {
            return;
        }
        toggle.hidden = description.scrollHeight <= description.clientHeight + 2;
    };

    var show = function (trigger, pinned) {
        var node = panel();
        var url = trigger ? trigger.getAttribute('data-info-url') : '';
        if (!node || !url) {
            return;
        }
        clearTimers();
        current.request += 1;
        var request = current.request;
        // By URL, not element: the card may be a silent refresh's new copy.
        var sameCard = current.url === url && !node.hidden;
        current.trigger = trigger;
        current.url = url;
        current.pinned = pinned || (sameCard && current.pinned);
        trigger.setAttribute('aria-expanded', 'true');

        if (sameCard) {
            return;
        }
        node.innerHTML = '<p class="tracker-info__loading">Loading…</p>';
        node.hidden = false;
        place(node, trigger);

        fetchPanel(url).then(function (html) {
            if (request !== current.request || current.trigger !== trigger) {
                return;
            }
            node.innerHTML = html;
            setUpDescription(node);
            place(node, trigger);
        }).catch(function () {
            if (request !== current.request) {
                return;
            }
            node.innerHTML = '<p class="tracker-info__empty">Could not load the series info.</p>';
        });
    };

    var hide = function (restoreFocus) {
        var node = panel();
        clearTimers();
        current.request += 1;
        if (current.trigger) {
            current.trigger.removeAttribute('aria-expanded');
            if (restoreFocus && document.body.contains(current.trigger)) {
                current.trigger.focus({ preventScroll: true });
            }
        }
        current.trigger = null;
        current.url = '';
        current.pinned = false;
        if (node) {
            node.hidden = true;
            node.innerHTML = '';
        }
    };

    var scheduleClose = function () {
        if (current.pinned || !current.trigger) {
            return;
        }
        if (closeTimer) {
            window.clearTimeout(closeTimer);
        }
        closeTimer = window.setTimeout(function () {
            closeTimer = null;
            if (!current.pinned) {
                hide(false);
            }
        }, HOVER_CLOSE_DELAY);
    };

    var triggerFrom = function (target) {
        return target && target.closest ? target.closest('.tracker-card__info-trigger') : null;
    };

    // A modal or an open rating popover owns the reader's attention; hovering
    // a cover underneath them opens nothing.
    var somethingElseOpen = function () {
        return !!document.querySelector('#modal-zone .modal-backdrop, details.tracker-rating[open]');
    };

    document.addEventListener('pointerover', function (event) {
        if (event.pointerType && event.pointerType !== 'mouse') {
            return;
        }
        var node = panel();
        if (node && !node.hidden && node.contains(event.target)) {
            if (closeTimer) {
                window.clearTimeout(closeTimer);
                closeTimer = null;
            }
            return;
        }
        var trigger = triggerFrom(event.target);
        if (!trigger) {
            return;
        }
        if (closeTimer && trigger === current.trigger) {
            window.clearTimeout(closeTimer);
            closeTimer = null;
            return;
        }
        if (current.pinned || somethingElseOpen() || narrowScreen()) {
            return;
        }
        if (openTimer) {
            window.clearTimeout(openTimer);
        }
        openTimer = window.setTimeout(function () {
            openTimer = null;
            if (trigger.matches(':hover')) {
                show(trigger, false);
            }
        }, HOVER_OPEN_DELAY);
    });

    document.addEventListener('pointerout', function (event) {
        if (event.pointerType && event.pointerType !== 'mouse') {
            return;
        }
        var node = panel();
        var leaving = triggerFrom(event.target) || (node && node.contains(event.target) ? node : null);
        if (!leaving) {
            return;
        }
        var into = event.relatedTarget;
        var anchor = liveTrigger();
        if (into && (leaving.contains(into) || (node && node.contains(into)) || (anchor && anchor.contains(into)))) {
            return;
        }
        if (openTimer && leaving !== node) {
            window.clearTimeout(openTimer);
            openTimer = null;
        }
        scheduleClose();
    });

    document.addEventListener('click', function (event) {
        var node = panel();
        var trigger = triggerFrom(event.target);
        if (trigger) {
            event.preventDefault();
            if (current.url === trigger.getAttribute('data-info-url') && current.pinned && node && !node.hidden) {
                hide(false);
                return;
            }
            show(trigger, true);
            return;
        }
        if (!node || node.hidden) {
            return;
        }
        var close = event.target.closest ? event.target.closest('[data-info-close]') : null;
        if (close && node.contains(close)) {
            hide(true);
            return;
        }
        // A chip filters the list by its value and closes the panel.
        var chip = event.target.closest ? event.target.closest('[data-filter-name]') : null;
        if (chip && node.contains(chip)) {
            if (typeof window.applyTrackerFilter === 'function') {
                window.applyTrackerFilter(chip.getAttribute('data-filter-name'), chip.getAttribute('data-filter-value'), chip.getAttribute('data-filter-label'));
            }
            hide(false);
            return;
        }
        var more = event.target.closest ? event.target.closest('[data-toggle-description]') : null;
        if (more && node.contains(more)) {
            var description = node.querySelector('.tracker-info__description');
            if (description) {
                var collapsed = description.getAttribute('data-collapsed') === 'true';
                description.setAttribute('data-collapsed', collapsed ? 'false' : 'true');
                more.textContent = collapsed ? 'Show less' : 'Read more';
                // The panel stays put and grows downwards (it scrolls inside
                // itself past its maximum height): moving it as the reader
                // clicks inside it lost their place.
            }
            return;
        }
        if (!node.contains(event.target)) {
            hide(false);
        }
    });

    document.addEventListener('keydown', function (event) {
        if (event.key !== 'Escape' || event.defaultPrevented) {
            return;
        }
        var node = panel();
        if (!node || node.hidden || document.querySelector('#modal-zone .modal-backdrop')) {
            return;
        }
        event.preventDefault();
        hide(true);
    });

    // A list the reader asked for replaces the card the panel belongs to; a
    // silent refresh re-renders the same cards, and the panel stays (it is
    // placed in page coordinates, so the new copy sits where the old one was).
    document.body.addEventListener('htmx:beforeRequest', function (event) {
        var detail = event && event.detail;
        if (!detail || !detail.target || detail.target.id !== 'trackers-zone') {
            return;
        }
        if (typeof window.isQuietListRequest === 'function' && window.isQuietListRequest(detail)) {
            return;
        }
        hide(false);
    });

    // After a silent refresh the panel moves to the new copy of its card. A
    // card that left the list closes a hover panel; a pinned one stays where
    // the reader put it until they close it.
    document.body.addEventListener('htmx:afterSwap', function (event) {
        var target = event && event.target;
        var node = panel();
        if (!target || target.id !== 'trackers-zone' || !node || node.hidden) {
            return;
        }
        var anchor = liveTrigger();
        if (anchor) {
            place(node, anchor);
        } else if (!current.pinned) {
            hide(false);
        }
    });

    // A card saved or rated has its stored data changed; its panel is fetched
    // afresh next time.
    document.body.addEventListener('htmx:afterRequest', function (event) {
        var path = event && event.detail && event.detail.requestConfig ? String(event.detail.requestConfig.path || '') : '';
        var match = /\/dashboard\/trackers\/(\d+)(?:\/|$|\?)/.exec(path);
        if (match) {
            delete cache['/dashboard/trackers/' + match[1] + '/info'];
        }
    });

    window.addEventListener('resize', function () {
        var node = panel();
        if (node && !node.hidden && liveTrigger()) {
            place(node, current.trigger);
        }
    });
})();
