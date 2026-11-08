// Fetches one tracker's card fragment and resolves with the parsed card
// element and whether the server is still looking up its cover or chapter
// links (X-Card-Lookups-Pending). Rejects on network failure, an empty body, or
// markup whose first element carries no id — the id is what the insert logic
// keys on.
var fetchTrackerCardFragment = function (requestURL) {
    return fetch(requestURL, {
        credentials: 'same-origin',
        headers: { 'HX-Request': 'true' }
    })
        .then(function (response) {
            if (!response.ok) {
                throw new Error('card fragment request failed');
            }
            var pending = response.headers.get('X-Card-Lookups-Pending') === '1';
            return response.text().then(function (html) {
                return { html: html, pending: pending };
            });
        })
        .then(function (result) {
            var markup = String(result.html || '').trim();
            if (!markup) {
                throw new Error('empty card fragment');
            }

            var buffer = document.createElement('div');
            buffer.innerHTML = markup;
            var card = buffer.firstElementChild;
            if (!card || !card.id) {
                throw new Error('invalid card fragment');
            }
            return { card: card, pending: result.pending };
        });
};

var currentCardsContainer = function () {
    return document.getElementById('cards-container-grid');
};

var trackerCardFragmentURL = function (trackerID) {
    var profileInput = document.getElementById('profile-filter');
    var profileKey = profileInput && profileInput.value ? String(profileInput.value).trim() : '';
    var requestURL = '/dashboard/trackers/' + encodeURIComponent(String(trackerID)) + '/card-fragment';
    if (profileKey) {
        requestURL += '?profile=' + encodeURIComponent(profileKey);
    }
    return requestURL;
};

// The filters besides Status and Sort. Each narrows the list by something the
// card does not carry, so only the server can tell whether a card matches one;
// and with any of them on, Reading stops leaving out caught-up trackers
// (repository.TrackerListOptions.Narrowed).
var narrowingFilterNames = ['q', 'sites', 'tags', 'types', 'publication', 'genres'];

var listIsNarrowed = function (params) {
    return narrowingFilterNames.some(function (name) {
        return params.getAll(name).some(function (value) {
            return String(value || '').trim() !== '';
        });
    });
};

// What the list on screen was asked for, as the trackers partial records it in
// #trackers-list-state: the request URL, the page the server actually served,
// and whether covers were still being looked up. Null while the skeleton
// stands in for the list.
var shownTrackersList = function () {
    var marker = document.getElementById('trackers-list-state');
    if (!marker) {
        return null;
    }
    var listURL = marker.getAttribute('data-list-url') || '';
    var params;
    try {
        params = new URL(listURL, window.location.origin).searchParams;
    } catch (_) {
        return null;
    }
    return {
        refreshKey: listURL,
        params: params,
        page: Number(marker.getAttribute('data-page') || '1'),
        pendingCovers: marker.getAttribute('data-pending-covers') === 'true'
    };
};

var listStatusFilter = function (list) {
    // The server reads a missing status as Reading and an empty one as All.
    if (!list.params.has('status')) {
        return 'reading';
    }
    return String(list.params.get('status') || '').trim();
};

var cardIsCaughtUp = function (card) {
    var lastRead = parseFloat(card.getAttribute('data-last-read'));
    var latest = parseFloat(card.getAttribute('data-latest'));
    return !isNaN(lastRead) && !isNaN(latest) && lastRead >= latest;
};

// Whether a card, as just rendered, belongs in the list on screen. Status is
// on the card (with Reading's rule that a caught-up tracker is not on it); a
// search, a tag, a site, a type, a publication status or a genre depends on
// what the card does not carry, so with one of those active only the server
// can tell and the answer is no.
var cardMatchesShownList = function (card, list) {
    if (!card || !list) {
        return false;
    }
    if (listIsNarrowed(list.params)) {
        return false;
    }
    var status = listStatusFilter(list);
    if (status !== '' && status !== 'all' && card.getAttribute('data-status') !== status) {
        return false;
    }
    return !(status === 'reading' && cardIsCaughtUp(card));
};

// A card replaced in place keeps its position in the grid and the reader's
// focus: the button or link focused in the old card is focused again in the
// new one, without scrolling. (The OOB response's own refresh calls this too.)
var focusSignature = function (node) {
    var attrs = ['hx-post', 'hx-get', 'href'];
    for (var i = 0; i < attrs.length; i += 1) {
        var value = node.getAttribute && node.getAttribute(attrs[i]);
        if (value) {
            return node.tagName.toLowerCase() + '[' + attrs[i] + '="' + String(value).replace(/["\\]/g, '\\$&') + '"]';
        }
    }
    if (node.classList && node.classList.length) {
        return node.tagName.toLowerCase() + '.' + node.classList[0];
    }
    return '';
};

window.replaceTrackerCard = function (node, fresh) {
    if (!node || !fresh || !node.parentNode) {
        return null;
    }
    if (node.style && node.style.order) {
        fresh.style.order = node.style.order;
    }
    var focused = document.activeElement;
    var signature = (focused && focused !== document.body && node.contains(focused)) ? focusSignature(focused) : '';
    node.parentNode.replaceChild(fresh, node);
    if (window.htmx && typeof window.htmx.process === 'function') {
        window.htmx.process(fresh);
    }
    if (signature) {
        var match = fresh.querySelector(signature);
        if (match && typeof match.focus === 'function') {
            match.focus({ preventScroll: true });
        }
    }
    if (typeof window.syncTrackerCardHoverState === 'function') {
        window.syncTrackerCardHoverState();
    }
    return fresh;
};

// A card rendered while its cover or chapter lookups were still running asks
// for itself again until the server stops saying so (X-Card-Lookups-Pending),
// a bounded number of times. An open rating popover in it is waited for: the
// swap would close it.
var keepTrackerCardFresh = function (cardID, requestURL) {
    var attempts = 0;
    var maxAttempts = 8;
    var refresh;
    var schedule = function () {
        if (attempts >= maxAttempts) {
            return;
        }
        window.setTimeout(refresh, Math.min(3000, 1000 + (attempts * 400)));
    };
    refresh = function () {
        attempts += 1;
        var node = document.getElementById(cardID);
        if (!node) {
            return;
        }
        if (node.querySelector('details[open]')) {
            schedule();
            return;
        }
        fetchTrackerCardFragment(requestURL)
            .then(function (result) {
                var current = document.getElementById(cardID);
                if (!current || result.card.id !== cardID) {
                    return;
                }
                window.replaceTrackerCard(current, result.card);
                if (result.pending) {
                    schedule();
                }
            })
            .catch(schedule);
    };
    schedule();
};

// Puts a freshly fetched card at the top of the container, replacing any older
// render of the same tracker, and lets htmx wire up its attributes.
var placeTrackerCardAtTop = function (container, card) {
    var existing = container.querySelector('#' + card.id);
    if (existing && existing !== card) {
        existing.remove();
    }

    container.insertBefore(card, container.firstElementChild);
    card.style.order = '-9999';
    if (window.htmx && typeof window.htmx.process === 'function') {
        window.htmx.process(card);
    }
};

// A tracker created moments ago is pinned to the top of page 1, where the
// reader can see what they just added whatever the sort says, and the pin
// survives the list's own re-renders (afterSwap below) until the reader asks
// for another list. The pin remembers the filters it was made under: a list
// asked for with other filters does not get it.
var pinnedTrackerFilters = '';
var pinnedTrackerNode = null;

var pinTrackerCard = function (card) {
    window.__pinnedTrackerID = card.id;
    pinnedTrackerFilters = trackerFiltersState();
    pinnedTrackerNode = card;
};

var unpinTrackerCard = function () {
    window.__pinnedTrackerID = '';
    pinnedTrackerFilters = '';
    pinnedTrackerNode = null;
};

// The new card is put on top only where it belongs: page 1 of a list whose
// filters it matches. On a Completed filter a new Reading tracker was pinned on
// top anyway, on page 2 it went on top of page 2, the count kept saying "258
// results", and the next refresh made the card vanish. Either way the list is
// then refreshed quietly, so the count, the pages and the card's place in the
// order come from the server. Everything is read when the fragment arrives,
// not when it was asked for: the list may have been re-rendered in between,
// and a card put into the container that was current then went nowhere.
document.body.addEventListener('trackerCreated', function (event) {
    var detail = event && event.detail ? event.detail : {};
    var payload = detail;
    if (detail && detail.value && typeof detail.value === 'object') {
        payload = detail.value;
    }
    var trackerID = Number((payload && payload.id) || detail.id || 0);
    if (!trackerID) {
        return;
    }

    var requestURL = trackerCardFragmentURL(trackerID);

    fetchTrackerCardFragment(requestURL)
        .then(function (result) {
            var card = result.card;
            var container = currentCardsContainer();
            var list = shownTrackersList();
            if (container && list && list.page === 1 && cardMatchesShownList(card, list)) {
                placeTrackerCardAtTop(container, card);
                pinTrackerCard(card);
                if (result.pending) {
                    keepTrackerCardFresh(card.id, requestURL);
                }
            }
            window.dispatchTrackersChanged('silent');
        })
        .catch(function () {
            window.dispatchTrackersChanged('silent');
        });
});

// Before the list is swapped: the pinned card about to be replaced is kept,
// in case the new list does not carry it (sorted onto another page). A pinned
// card already gone from the page was deleted, and stays gone.
var holdPinnedCardBeforeSwap = function () {
    if (!window.__pinnedTrackerID) {
        return;
    }
    var node = document.getElementById(window.__pinnedTrackerID);
    if (!node) {
        unpinTrackerCard();
        return;
    }
    pinnedTrackerNode = node;
};

var restorePinnedCardAfterSwap = function () {
    if (!window.__pinnedTrackerID) {
        return;
    }
    var container = currentCardsContainer();
    var list = shownTrackersList();
    if (!container || !list || list.page !== 1 || trackerFiltersState() !== pinnedTrackerFilters) {
        unpinTrackerCard();
        return;
    }
    var card = document.getElementById(window.__pinnedTrackerID);
    if (!card && pinnedTrackerNode && cardMatchesShownList(pinnedTrackerNode, list)) {
        // htmx cleaned up the old node's handlers as it removed it, so a copy
        // goes back in and is processed afresh.
        card = pinnedTrackerNode.cloneNode(true);
        container.insertBefore(card, container.firstElementChild);
        if (window.htmx && typeof window.htmx.process === 'function') {
            window.htmx.process(card);
        }
    }
    if (!card) {
        unpinTrackerCard();
        return;
    }
    if (container.firstElementChild !== card) {
        container.insertBefore(card, container.firstElementChild);
    }
    card.style.order = '-9999';
    pinnedTrackerNode = card;
};

// The search box asks for the list 300ms after the last keystroke. While that
// timer runs, the form already holds text the reader is still typing, so a
// silent refresh in that window would apply it half-typed; it is held off
// until the search has gone (see trackersSearchPending's callers).
var searchDebounceTimer = null;

var trackersSearchPending = function () {
    return !!searchDebounceTimer;
};

var cancelSearchDebounce = function () {
    if (searchDebounceTimer) {
        window.clearTimeout(searchDebounceTimer);
        searchDebounceTimer = null;
    }
};

document.addEventListener('DOMContentLoaded', function () {
    window.updateFilterSummaries();

    var filtersForm = document.getElementById('tracker-filters');
    if (filtersForm) {
        var dispatchTrackersChanged = function () {
            window.dispatchTrackersChanged('user');
        };

        var scheduleSearchRefresh = function () {
            cancelSearchDebounce();
            searchDebounceTimer = window.setTimeout(function () {
                searchDebounceTimer = null;
                dispatchTrackersChanged();
            }, 300);
        };

        // The search box is the form's only text field and the form has no
        // submit button, so Enter in it is an implicit submission. htmx does not
        // catch it (the form's trigger is explicit), and with no action the
        // browser navigated to /dashboard?q=…: the page that came back had the
        // search, status, sort, tags and view all reset. The search is live
        // anyway, so Enter just runs it now instead of after the debounce.
        filtersForm.addEventListener('submit', function (event) {
            event.preventDefault();
            cancelSearchDebounce();
            var submitPageInput = document.getElementById('page-input');
            if (submitPageInput) {
                submitPageInput.value = '1';
            }
            dispatchTrackersChanged();
        });

        filtersForm.addEventListener('change', function (event) {
            var target = event && event.target;
            if (!target || target.name === 'page') {
                return;
            }

            var shouldRefresh = false;
            if (target.tagName === 'SELECT') {
                shouldRefresh = true;
            } else if (multiSelectFilterNames.indexOf(target.name) !== -1) {
                shouldRefresh = true;
            }

            if (!shouldRefresh) {
                return;
            }

            var changedPageInput = document.getElementById('page-input');
            if (changedPageInput) {
                changedPageInput.value = '1';
            }
            // This request carries the typed search too.
            cancelSearchDebounce();
            dispatchTrackersChanged();
        });

        var searchInput = filtersForm.querySelector('input[name="q"]');
        if (searchInput) {
            var searchClearButton = document.getElementById('dashboard-search-clear');
            var syncSearchClearButton = function () {
                if (!searchClearButton) {
                    return;
                }
                searchClearButton.hidden = !searchInput.value;
            };

            searchInput.addEventListener('input', function () {
                var inputPage = document.getElementById('page-input');
                if (inputPage) {
                    inputPage.value = '1';
                }
                syncSearchClearButton();
                scheduleSearchRefresh();
            });

            syncSearchClearButton();

            if (searchClearButton) {
                searchClearButton.addEventListener('click', function () {
                    if (!searchInput.value) {
                        return;
                    }

                    // The search still pending from the last keystroke would
                    // follow this request with one of its own.
                    cancelSearchDebounce();
                    searchInput.value = '';
                    var clearPageInput = document.getElementById('page-input');
                    if (clearPageInput) {
                        clearPageInput.value = '1';
                    }
                    syncSearchClearButton();
                    searchInput.focus();
                    dispatchTrackersChanged();
                });
            }
        }
    }
});

// The checkbox dropdowns of the filters form, by the name their boxes submit.
var multiSelectFilterNames = ['publication', 'types', 'genres', 'sites', 'tags'];

// A dropdown's summary says what is picked: "Any" for nothing, the names of
// one or two picks, a count past that. It used to show a bare count ("0"),
// which with eight filters left the reader opening each one to find what was
// still narrowing the list.
var summarizeFilterDropdown = function (dropdown) {
    var summary = dropdown ? dropdown.querySelector(':scope > summary') : null;
    var name = dropdown ? dropdown.getAttribute('data-filter-name') : '';
    if (!summary || !name) {
        return;
    }
    var labels = Array.prototype.map.call(dropdown.querySelectorAll('input[name="' + name + '"]:checked'), function (box) {
        var option = box.closest('.filter-multi-select__option');
        var label = option ? option.querySelector('span') : null;
        return label ? label.textContent.trim() : String(box.value);
    });
    if (labels.length === 0) {
        summary.textContent = 'Any';
    } else if (labels.length <= 2) {
        summary.textContent = labels.join(', ');
    } else {
        summary.textContent = labels.length + ' selected';
    }
    summary.title = labels.join(', ');
    dropdown.classList.toggle('filter-multi-select--active', labels.length > 0);
};

// Whether anything besides Status and Sort narrows the list, which is when
// the Clear filters button shows.
var syncFiltersResetButton = function () {
    var button = document.getElementById('filters-reset');
    var form = document.getElementById('tracker-filters');
    if (!button || !form || typeof window.FormData !== 'function') {
        return;
    }
    button.hidden = !listIsNarrowed(new window.URLSearchParams(new window.FormData(form)));
};

window.updateFilterSummaries = function () {
    Array.prototype.forEach.call(document.querySelectorAll('#tracker-filters details.filter-multi-select'), summarizeFilterDropdown);
    syncFiltersResetButton();
};

// Kept for the callers that only know about one menu (dashboard-tags.js).
window.updateFilterTagsSummary = window.updateFilterSummaries;
window.updateFilterSitesSummary = window.updateFilterSummaries;

// Ticks one option of a filter dropdown and asks for the list, the way a
// click in the menu would. Used by the info panel's genre and type chips; an
// option the menu does not carry yet (a genre that appeared since the page
// loaded) is added to it.
window.applyTrackerFilter = function (name, value, label) {
    var form = document.getElementById('tracker-filters');
    var dropdown = form ? form.querySelector('details.filter-multi-select[data-filter-name="' + name + '"]') : null;
    if (!dropdown || !value) {
        return false;
    }
    var box = Array.prototype.filter.call(dropdown.querySelectorAll('input[name="' + name + '"]'), function (candidate) {
        return candidate.value === value;
    })[0];
    if (!box) {
        var menu = dropdown.querySelector('.filter-multi-select__menu');
        if (!menu) {
            return false;
        }
        var empty = menu.querySelector('.filter-multi-select__empty');
        if (empty) {
            empty.remove();
        }
        var option = document.createElement('label');
        option.className = 'filter-multi-select__option';
        box = document.createElement('input');
        box.type = 'checkbox';
        box.name = name;
        box.value = value;
        var text = document.createElement('span');
        text.textContent = label || value;
        option.appendChild(box);
        option.appendChild(text);
        menu.appendChild(option);
    }
    if (box.checked) {
        return true;
    }
    box.checked = true;
    window.updateFilterSummaries();
    var pageInput = document.getElementById('page-input');
    if (pageInput) {
        pageInput.value = '1';
    }
    cancelSearchDebounce();
    window.dispatchTrackersChanged('user');
    return true;
};

document.addEventListener('change', function (event) {
    var target = event.target;
    if (!target || multiSelectFilterNames.indexOf(target.name) === -1) {
        return;
    }
    window.updateFilterSummaries();
});

document.addEventListener('input', function (event) {
    var target = event.target;
    if (target && target.name === 'q') {
        syncFiltersResetButton();
    }
    // The box at the top of the genre menu narrows the options shown; it has
    // no name, so it never reaches the request.
    if (!target || !target.matches || !target.matches('[data-option-search]')) {
        return;
    }
    var needle = String(target.value || '').trim().toLowerCase();
    var menu = target.closest('.filter-multi-select__menu');
    Array.prototype.forEach.call(menu ? menu.querySelectorAll('.filter-multi-select__option') : [], function (option) {
        option.hidden = needle !== '' && option.textContent.toLowerCase().indexOf(needle) === -1;
    });
});

// Enter in the genre finder would submit the filters form; it does nothing
// here instead, the ticks being what filters.
document.addEventListener('keydown', function (event) {
    if (event.key === 'Enter' && event.target && event.target.matches && event.target.matches('[data-option-search]')) {
        event.preventDefault();
    }
});

document.addEventListener('click', function (event) {
    var button = event.target && event.target.closest ? event.target.closest('#filters-reset') : null;
    if (!button) {
        return;
    }
    var form = document.getElementById('tracker-filters');
    if (!form) {
        return;
    }
    Array.prototype.forEach.call(form.querySelectorAll('details.filter-multi-select input[type="checkbox"]:checked'), function (box) {
        box.checked = false;
    });
    var search = form.querySelector('input[name="q"]');
    if (search) {
        search.value = '';
        search.dispatchEvent(new Event('input', { bubbles: true }));
    }
    cancelSearchDebounce();
    window.updateFilterSummaries();
    var pageInput = document.getElementById('page-input');
    if (pageInput) {
        pageInput.value = '1';
    }
    var clearButton = document.getElementById('dashboard-search-clear');
    if (clearButton) {
        clearButton.hidden = true;
    }
    window.dispatchTrackersChanged('user');
});

document.addEventListener('click', function (event) {
    // Every filter dropdown closes on a click outside it.
    Array.prototype.forEach.call(document.querySelectorAll('details.filter-multi-select[open]'), function (dropdown) {
        if (!dropdown.contains(event.target)) {
            dropdown.removeAttribute('open');
        }
    });

    // The connector strip's "+N" menu is a <details> too, and nothing closed
    // it: it stayed open over the cards, and while open it held off every
    // silent cover refresh until the deferral cap ran out.
    Array.prototype.forEach.call(document.querySelectorAll('details.site-links-more[open]'), function (menu) {
        if (!menu.contains(event.target)) {
            menu.removeAttribute('open');
        }
    });
});

// The tag menu is re-rendered by the server whenever a tag is created, renamed
// or deleted, and the server does not know which boxes were ticked: every box
// came back unticked while the list behind it stayed filtered by the old
// selection — and after a rename or delete, by a name that no longer exists,
// so the page read "No trackers found" with no filter showing. The ticks are
// carried across the swap by tag id, so a renamed tag stays ticked under its
// new name (by name, case-insensitively as the list filters them, for markup
// without ids), and a ticked tag that is gone takes its filter with it.
var tickedTagsBeforeSwap = null;

// A tag change answers with both trackersChanged and profileTagsChanged, and
// htmx raises them in that order: the list used to reload at once, filtered by
// the old tag name, then again once the menu came back with the new one —
// twice per rename, the second skeleton wiping the first list mid-settle. The
// list now waits for the menu: a non-user trackersChanged that arrives while
// the menu is being re-fetched is owed, and paid once its ticks are restored.
var tagMenuRefreshPending = false;
var tagMenuRefreshTimer = null;
var listRefreshOwed = false;

var settleTagMenuRefresh = function (ticksChanged) {
    var owed = listRefreshOwed;
    tagMenuRefreshPending = false;
    listRefreshOwed = false;
    if (tagMenuRefreshTimer) {
        window.clearTimeout(tagMenuRefreshTimer);
        tagMenuRefreshTimer = null;
    }
    if (ticksChanged) {
        var tagsPageInput = document.getElementById('page-input');
        if (tagsPageInput) {
            tagsPageInput.value = '1';
        }
    }
    if (owed || ticksChanged) {
        window.dispatchTrackersChanged('system');
    }
};

document.body.addEventListener('htmx:beforeOnLoad', function (event) {
    var xhr = event && event.detail ? event.detail.xhr : null;
    var header = xhr && typeof xhr.getResponseHeader === 'function' ? xhr.getResponseHeader('HX-Trigger') : null;
    if (!header || header.indexOf('profileTagsChanged') === -1 || !document.getElementById('filter-tags-dropdown')) {
        return;
    }
    tagMenuRefreshPending = true;
    if (tagMenuRefreshTimer) {
        window.clearTimeout(tagMenuRefreshTimer);
    }
    // Should the menu never ask, the list is not kept waiting.
    tagMenuRefreshTimer = window.setTimeout(function () {
        tagMenuRefreshTimer = null;
        if (tagMenuRefreshPending) {
            settleTagMenuRefresh(false);
        }
    }, 10000);
});

document.body.addEventListener('htmx:afterRequest', function (event) {
    var elt = event && event.detail ? event.detail.elt : null;
    if (!tagMenuRefreshPending || !elt || !elt.closest || !elt.closest('#filter-tags-dropdown')) {
        return;
    }
    // Reached only when the menu failed to come back (a swap settles it first).
    settleTagMenuRefresh(false);
});

document.body.addEventListener('htmx:beforeSwap', function (event) {
    var target = event && event.detail ? event.detail.target : null;
    if (!target || !target.closest || !target.closest('#filter-tags-dropdown')) {
        return;
    }

    tickedTagsBeforeSwap = [];
    Array.prototype.forEach.call(target.querySelectorAll('input[name="tags"]:checked'), function (box) {
        tickedTagsBeforeSwap.push({
            id: box.getAttribute('data-tag-id') || '',
            name: String(box.value).toLowerCase()
        });
    });
});

var restoreTickedFilterTags = function (menu) {
    var wanted = tickedTagsBeforeSwap || [];
    tickedTagsBeforeSwap = null;

    var boxes = Array.prototype.slice.call(menu.querySelectorAll('input[name="tags"]'));
    var ticksChanged = false;
    wanted.forEach(function (tick) {
        var box = null;
        if (tick.id) {
            box = boxes.filter(function (candidate) {
                return candidate.getAttribute('data-tag-id') === tick.id;
            })[0] || null;
        }
        if (!box) {
            box = boxes.filter(function (candidate) {
                return String(candidate.value).toLowerCase() === tick.name;
            })[0] || null;
        }
        if (!box) {
            ticksChanged = true;
            return;
        }
        box.checked = true;
        if (String(box.value).toLowerCase() !== tick.name) {
            ticksChanged = true;
        }
    });

    // The list was fetched with the old names (or not at all yet); one
    // request now matches the ticks the menu shows.
    if (tagMenuRefreshPending || ticksChanged) {
        settleTagMenuRefresh(ticksChanged);
    }
};

var normalizeRatingValue = function (value) {
    var numericValue = Number(value);
    if (!isFinite(numericValue)) {
        numericValue = 0;
    }

    if (numericValue < 0) {
        numericValue = 0;
    } else if (numericValue > 10) {
        numericValue = 10;
    }

    return Math.round(numericValue * 2) / 2;
};

var getRatingPopover = function (node) {
    if (!node || !node.closest) {
        return null;
    }
    return node.closest('.tracker-rating__popover');
};

var getRatingInput = function (popover) {
    if (!popover || !popover.querySelector) {
        return null;
    }
    return popover.querySelector('.js-rating-input');
};

var applyRatingUI = function (popover, value) {
    if (!popover) {
        return;
    }

    var numericValue = normalizeRatingValue(value);
    var valueLabel = numericValue.toFixed(1);

    var valueNode = popover.querySelector('.js-rating-value');
    if (valueNode) {
        valueNode.textContent = valueLabel;
    }

    var starsNode = popover.querySelector('.js-rating-stars');
    if (starsNode) {
        starsNode.style.setProperty('--rating-value', valueLabel);
    }

    var controlNode = popover.querySelector('.js-rating-control');
    if (controlNode) {
        controlNode.setAttribute('aria-valuenow', valueLabel);
    }
};

var getSelectedRatingValue = function (popover) {
    if (!popover) {
        return 0;
    }

    var selected = popover.getAttribute('data-selected-rating');
    if (selected === null || selected === '') {
        var input = getRatingInput(popover);
        if (!input) {
            return 0;
        }
        return normalizeRatingValue(input.value);
    }

    return normalizeRatingValue(selected);
};

var setSelectedRatingValue = function (popover, value) {
    if (!popover) {
        return;
    }

    var normalized = normalizeRatingValue(value);
    var valueLabel = normalized.toFixed(1);

    var ratingInput = getRatingInput(popover);
    if (ratingInput) {
        ratingInput.value = valueLabel;
    }

    popover.setAttribute('data-selected-rating', valueLabel);
    applyRatingUI(popover, normalized);
};

// The rating the server has stored, as the card was rendered with it. The
// hidden input cannot tell: it holds whatever was last picked.
var getSavedRatingValue = function (popover) {
    if (!popover) {
        return 0;
    }
    var saved = popover.getAttribute('data-saved-rating');
    if (saved === null || saved === '') {
        var input = getRatingInput(popover);
        return normalizeRatingValue(input ? input.value : 0);
    }
    return normalizeRatingValue(saved);
};

var revertRatingPreview = function (popover) {
    if (!popover) {
        return;
    }
    applyRatingUI(popover, getSelectedRatingValue(popover));
};

var previewRatingValue = function (popover, value) {
    applyRatingUI(popover, value);
};

var ratingFromPointer = function (controlNode, event) {
    if (!controlNode || !event) {
        return 0;
    }

    var rect = controlNode.getBoundingClientRect();
    if (!rect || rect.width <= 0) {
        return 0;
    }

    var relativeX = event.clientX - rect.left;
    if (relativeX < 0) {
        relativeX = 0;
    } else if (relativeX > rect.width) {
        relativeX = rect.width;
    }

    var rawValue = (relativeX / rect.width) * 10;
    return normalizeRatingValue(rawValue);
};

document.addEventListener('pointermove', function (event) {
    var target = event && event.target;
    var controlNode = target && target.closest ? target.closest('.js-rating-control') : null;
    if (!controlNode) {
        return;
    }

    if (event.pointerType === 'touch') {
        return;
    }

    var popover = getRatingPopover(controlNode);
    if (!popover) {
        return;
    }

    previewRatingValue(popover, ratingFromPointer(controlNode, event));
});

document.addEventListener('pointerleave', function (event) {
    var target = event && event.target;
    if (!target || !target.matches || !target.matches('.js-rating-control')) {
        return;
    }

    var popover = getRatingPopover(target);
    if (!popover) {
        return;
    }

    revertRatingPreview(popover);
}, true);

document.addEventListener('click', function (event) {
    var target = event && event.target;
    var controlNode = target && target.closest ? target.closest('.js-rating-control') : null;
    if (!controlNode) {
        return;
    }

    var popover = getRatingPopover(controlNode);
    if (!popover) {
        return;
    }

    setSelectedRatingValue(popover, ratingFromPointer(controlNode, event));
});

document.addEventListener('keydown', function (event) {
    var target = event && event.target;
    if (!target || !target.matches || !target.matches('.js-rating-control')) {
        return;
    }

    var popover = getRatingPopover(target);
    if (!popover) {
        return;
    }

    var current = getSelectedRatingValue(popover);
    var next = current;
    if (event.key === 'ArrowRight' || event.key === 'ArrowUp') {
        next = current + 0.5;
    } else if (event.key === 'ArrowLeft' || event.key === 'ArrowDown') {
        next = current - 0.5;
    } else if (event.key === 'Home') {
        next = 0;
    } else if (event.key === 'End') {
        next = 10;
    } else {
        return;
    }

    event.preventDefault();
    setSelectedRatingValue(popover, next);
});

// toggle does not bubble, so this listens in the capture phase (it never ran
// as a bubbling listener). A popover closed without saving — a click outside,
// Escape — goes back to the stored rating: a value picked with a star or End
// used to stay in the hidden input, came back on the next open, and was stored
// by whatever Save came after. A successful save replaces the whole card, so
// it never reaches here. A popover reopened by a silent refresh of the list
// brings back the value the reader had picked (data-restore-rating).
document.addEventListener('toggle', function (event) {
    var target = event && event.target;
    if (!target || !target.matches || !target.matches('.tracker-rating')) {
        return;
    }

    var popoverForm = target.querySelector('.tracker-rating__popover');

    if (!target.open) {
        if (popoverForm) {
            setSelectedRatingValue(popoverForm, getSavedRatingValue(popoverForm));
        }
        return;
    }

    var openPopovers = document.querySelectorAll('.tracker-rating[open]');
    openPopovers.forEach(function (popover) {
        if (popover !== target) {
            popover.removeAttribute('open');
        }
    });

    if (!popoverForm) {
        return;
    }

    var restore = popoverForm.getAttribute('data-restore-rating');
    if (restore !== null) {
        popoverForm.removeAttribute('data-restore-rating');
        setSelectedRatingValue(popoverForm, restore);
        return;
    }
    setSelectedRatingValue(popoverForm, getSavedRatingValue(popoverForm));
}, true);

document.addEventListener('click', function (event) {
    var openPopovers = document.querySelectorAll('.tracker-rating[open]');
    if (!openPopovers || openPopovers.length === 0) {
        return;
    }

    openPopovers.forEach(function (popover) {
        if (popover.contains(event.target)) {
            return;
        }
        popover.removeAttribute('open');
    });
});

var initializeVisibleRatingPopovers = function () {
    var popovers = document.querySelectorAll('.tracker-rating__popover');
    if (!popovers || popovers.length === 0) {
        return;
    }

    popovers.forEach(function (popover) {
        var input = getRatingInput(popover);
        if (!input) {
            return;
        }
        if (!popover.hasAttribute('data-saved-rating')) {
            popover.setAttribute('data-saved-rating', input.value);
        }
        setSelectedRatingValue(popover, input.value);
    });
};

document.addEventListener('DOMContentLoaded', initializeVisibleRatingPopovers);
document.body.addEventListener('htmx:afterSwap', function () {
    initializeVisibleRatingPopovers();
});

window.dismissModalZone = function () {
    var modalZone = document.getElementById('modal-zone');
    if (!modalZone) {
        return;
    }
    modalZone.innerHTML = '';
    if (typeof window.syncModalZone === 'function') {
        window.syncModalZone();
    }
};

var shouldDismissModalOnBackdropClick = false;

document.addEventListener('pointerdown', function (event) {
    var target = event && event.target;
    if (!target || !target.closest) {
        shouldDismissModalOnBackdropClick = false;
        return;
    }

    var backdrop = target.closest('#modal-zone .modal-backdrop');
    if (!backdrop) {
        shouldDismissModalOnBackdropClick = false;
        return;
    }

    shouldDismissModalOnBackdropClick = !target.closest('.modal-card');
}, true);

document.addEventListener('click', function (event) {
    var target = event && event.target;
    if (!target || !target.closest) {
        shouldDismissModalOnBackdropClick = false;
        return;
    }

    var backdrop = target.closest('#modal-zone .modal-backdrop');
    if (!backdrop) {
        shouldDismissModalOnBackdropClick = false;
        return;
    }

    if (target.closest('.modal-card')) {
        shouldDismissModalOnBackdropClick = false;
        return;
    }

    if (!shouldDismissModalOnBackdropClick) {
        return;
    }

    shouldDismissModalOnBackdropClick = false;
    window.dismissModalZone();
});

document.addEventListener('click', function (event) {
    var button = event.target && event.target.closest ? event.target.closest('.js-page-btn') : null;
    if (!button) {
        return;
    }

    if (button.hasAttribute('disabled')) {
        return;
    }

    var explicitPage = Number(button.getAttribute('data-page-value') || '0');
    if (explicitPage <= 0) {
        return;
    }
    var explicitPageInput = document.getElementById('page-input');
    if (explicitPageInput) {
        explicitPageInput.value = String(explicitPage);
    }

    window.__scrollTrackersToTop = true;
    window.dispatchTrackersChanged('user');
});

window.renderTrackersSkeleton = function () {
    var trackersZone = document.getElementById('trackers-zone');
    if (!trackersZone) {
        return;
    }

    var itemCount = 6;
    var items = [];

    for (var i = 0; i < itemCount; i += 1) {
        items.push('' +
            '<article class="tracker-card tracker-card--skeleton">' +
                '<div class="skeleton skeleton--title"></div>' +
                '<div class="skeleton skeleton--cover"></div>' +
                '<div class="skeleton skeleton--line"></div>' +
                '<div class="skeleton skeleton--line skeleton--line-short"></div>' +
            '</article>');
    }

    trackersZone.innerHTML = '' +
        '<p class="trackers-loading__label">Loading trackers…</p>' +
        '<div class="cards-grid">' + items.join('') + '</div>';
};

// A silent cover refresh replaces the whole trackers region. A hovered link
// loses its highlight for the instant the element is swapped — which reads as
// the page flickering under the cursor for no reason — and a focused control
// is taken from under the reader. The covers being waited on are never
// urgent, so the refresh yields to those and asks again shortly. Hover only
// counts over the connector strip: the pointer rests over the card grid most
// of the time, and deferring on that would stop covers from ever arriving. An
// open rating popover or connector menu no longer holds it off: it is carried
// across the swap instead (heldOpenMenus below), so it neither closes under the
// reader nor keeps the covers waiting.
window.trackersInteractionInProgress = function () {
    var zone = document.getElementById('trackers-zone');
    if (!zone) {
        return false;
    }

    var strip = zone.querySelector('.pagination-site-links');
    if (strip && strip.matches && strip.matches(':hover')) {
        return true;
    }

    var focused = document.activeElement;
    if (focused && focused !== document.body && zone.contains(focused)) {
        return true;
    }

    return false;
};

// The list request in flight, and whether it is a silent one.
var listRequest = null;
var silentRefreshOwed = false;

var isListTrigger = function (detail) {
    return !!(detail && detail.elt && detail.elt.id === 'tracker-filters');
};

// A silent refresh is sent by #trackers-quiet-refresh, never by the filters
// form, so the two never wait on each other: htmx lets one element have one
// request out and queues the rest, and the reader's click used to queue behind
// a silent refresh — nothing showed until the old list had been re-rendered,
// and only then did the skeleton come up. The silent one is held while a
// search is still being typed (it would apply the half-typed text), and owed
// while the reader's own request is out. One asked for while another silent
// one is out follows it, and the earlier answer is dropped: it may have been
// asked before what this one is for (the card a delete just removed).
var startQuietRefresh = function () {
    if (trackersSearchPending()) {
        return;
    }
    if (listRequest) {
        if (!listRequest.silent) {
            silentRefreshOwed = true;
            return;
        }
        listRequest.xhr.__trackersSuperseded = true;
    }
    var source = document.getElementById('trackers-quiet-refresh');
    if (source && window.htmx && typeof window.htmx.trigger === 'function') {
        window.htmx.trigger(source, 'trackersQuietRefresh');
    }
};

// Every trackersChanged reaches the filters form first.
//
// - A silent one is turned into a request from #trackers-quiet-refresh.
// - The reader's request goes ahead at once; a silent one still out is left to
//   finish, but its answer is not swapped in (htmx:beforeSwap below): it was
//   asked for a list the reader has just moved away from. Aborting it instead
//   had htmx log the abort as an error.
// - While the tag menu is re-rendering after a tag change, a list refresh
//   that did not come from the reader waits for it (see tagMenuRefreshPending).
document.body.addEventListener('htmx:confirm', function (event) {
    var detail = event && event.detail;
    if (!isListTrigger(detail)) {
        return;
    }
    var trigger = detail.triggeringEvent;
    var silent = window.isQuietTrigger(trigger);
    if (window.__silentCoverRefresh) {
        window.__silentCoverRefresh = false;
        silent = true;
    }
    var fromReader = !!(trigger && trigger.detail && trigger.detail.reason === 'user');

    if (tagMenuRefreshPending && trigger && trigger.type === 'trackersChanged' && !fromReader) {
        listRefreshOwed = true;
        event.preventDefault();
        return;
    }

    if (silent) {
        event.preventDefault();
        startQuietRefresh();
        return;
    }

    if (listRequest && listRequest.silent) {
        listRequest.xhr.__trackersSuperseded = true;
    }
});

// The cover refresh: a list rendered while covers or chapter links were still
// being looked up asks for itself again, quietly, until they have all landed
// (or a bounded number of times). This used to be an inline script in the
// trackers partial; a list request starting right after a swap wiped that
// script before htmx ran it ("Cannot read properties of null (reading
// 'insertBefore')"), so it lives here and reads the partial's
// #trackers-list-state instead.
var coverRefreshAttempts = {};
var silentRefreshDeferUntil = 0;
var quietRetryAttempts = 0;

var clearPendingSilentRefresh = function () {
    if (window.__pendingTrackersRefreshTimer) {
        window.clearTimeout(window.__pendingTrackersRefreshTimer);
        window.__pendingTrackersRefreshTimer = null;
    }
};

var runSilentRefresh = function () {
    window.__pendingTrackersRefreshTimer = null;

    var zone = document.getElementById('trackers-zone');
    var sel = window.getSelection ? window.getSelection() : null;
    if (sel && sel.toString() && zone && sel.anchorNode && zone.contains(sel.anchorNode)) {
        window.__pendingTrackersRefreshTimer = window.setTimeout(runSilentRefresh, 500);
        return;
    }

    // The search box's own request is about to replace the list anyway.
    if (trackersSearchPending()) {
        window.__pendingTrackersRefreshTimer = window.setTimeout(runSilentRefresh, 500);
        return;
    }

    // Bounded, so a control simply left focused eventually gives the covers
    // their turn.
    if (window.trackersInteractionInProgress()) {
        var now = new Date().getTime();
        if (!silentRefreshDeferUntil) {
            silentRefreshDeferUntil = now + 15000;
        }
        if (now < silentRefreshDeferUntil) {
            window.__pendingTrackersRefreshTimer = window.setTimeout(runSilentRefresh, 500);
            return;
        }
    }

    window.dispatchTrackersChanged('silent');
};

var queueSilentRefresh = function (delayMs) {
    clearPendingSilentRefresh();
    silentRefreshDeferUntil = 0;
    window.__pendingTrackersRefreshTimer = window.setTimeout(runSilentRefresh, delayMs);
};

var scheduleCoverRefresh = function (list) {
    if (!list || !list.refreshKey) {
        clearPendingSilentRefresh();
        return;
    }
    if (!list.pendingCovers) {
        clearPendingSilentRefresh();
        delete coverRefreshAttempts[list.refreshKey];
        return;
    }
    var attempts = Number(coverRefreshAttempts[list.refreshKey] || 0);
    if (attempts >= 30) {
        clearPendingSilentRefresh();
        return;
    }
    coverRefreshAttempts[list.refreshKey] = attempts + 1;
    queueSilentRefresh(Math.min(3000, 600 + (attempts * 200)));
};

// A silent refresh that fails is not the reader's to hear about: the list on
// screen stays as it is, and the refresh is tried again later, a few times,
// further apart each time. It used to put "Failed to load trackers" over a list
// the reader had done nothing to, and the covers it was waiting for stopped
// arriving for good.
var retryQuietlyLater = function () {
    if (quietRetryAttempts >= 5) {
        return;
    }
    var delayMs = Math.min(60000, 5000 * Math.pow(2, quietRetryAttempts));
    quietRetryAttempts += 1;
    queueSilentRefresh(delayMs);
};

document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'trackers-zone') {
        return;
    }

    var silent = window.isQuietListRequest(detail);
    listRequest = { xhr: detail.xhr, silent: silent };
    // Before the skeleton takes the pinned card off the page.
    holdPinnedCardBeforeSwap();
    if (silent) {
        return;
    }

    rememberFiltersInURL();

    var trackersZone = detail.target;

    // A page button below the list takes the reader to the top of the list
    // as it is pressed, so the skeleton and then the new page come in where
    // they are read. Scrolling after the swap left the reader at the bottom
    // for the whole request and then flew the page up from there. A button
    // already in sight (the top row) moves nothing.
    if (window.__scrollTrackersToTop) {
        window.__scrollTrackersToTop = false;
        if (trackersZone.getBoundingClientRect().top < 0) {
            trackersZone.scrollIntoView({ block: 'start' });
        }
    }

    // The skeleton is six cards; the list it stands in for can be many
    // screens long (a page of the grid is ~20000px on a phone). Swapping one
    // for the other shrank the document under the reader and the browser
    // clamped the scroll to the new, much shorter end: anyone scrolled down
    // was thrown to the bottom of the page while the list loaded, and left
    // somewhere unrelated when it arrived. The zone keeps its height until the
    // response is in (htmx:afterSwap, or htmx:afterRequest when it fails).
    trackersZone.style.minHeight = trackersZone.offsetHeight + 'px';

    window.renderTrackersSkeleton();
});

document.body.addEventListener('htmx:afterRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'trackers-zone') {
        return;
    }
    var xhr = detail.xhr;
    var wasSilent = window.isQuietListRequest(detail);
    if (listRequest && listRequest.xhr === xhr) {
        listRequest = null;
    }
    // Overtaken by the reader's own request, which is already on its way.
    if (xhr && xhr.__trackersSuperseded) {
        return;
    }
    if (silentRefreshOwed && !wasSilent) {
        silentRefreshOwed = false;
        // After htmx has started whatever it queued behind this request.
        window.setTimeout(function () {
            window.dispatchTrackersChanged('silent');
        }, 0);
    }
    if (detail.successful) {
        return;
    }
    if (wasSilent) {
        retryQuietlyLater();
        return;
    }
    // Nothing is swapped in for a failed request. The skeleton only ever
    // meant "loading": left under the error strip, with "Loading trackers…"
    // above it, it read as a load still under way when none would come. It
    // goes, and so does the old list's height. A reload's scroll offset
    // belonged to the list that failed to come back, so it is not applied to
    // whichever list loads later.
    var failedZone = detail.target;
    Array.prototype.forEach.call(failedZone.querySelectorAll('.trackers-loading__label, .tracker-card--skeleton'), function (node) {
        node.remove();
    });
    failedZone.style.minHeight = '';
    pendingDashboardScroll = null;
});

// A reload — and a phone browser bringing back a tab it discarded in the
// background is one — starts the page with an empty trackers zone, so the
// browser's own scroll restoration found a page a few hundred pixels tall
// (then the skeleton's) and clamped to its end: the reader came back to the
// bottom of a half-loaded page, and stayed at that offset once the list was
// in. The browser is told not to restore; the offset is put back here once the
// list has arrived — and only when it is the same list (the filters reset on a
// reload, so a reader who was on page 3 or searching starts at the top) and
// the reader has not already scrolled.
// Keyed by page and profile, not by the full URL: the URL now changes with the
// filters (rememberFiltersInURL), and the saved offset carries the filters it
// was taken under anyway, which restoreDashboardScroll compares.
var dashboardScrollKey = (function () {
    var profileInput = document.getElementById('profile-filter');
    var profileKey = profileInput && profileInput.value ? String(profileInput.value).trim() : '';
    return 'dashboard-scroll:' + window.location.pathname + ':' + profileKey;
})();
var pendingDashboardScroll = null;

// The filters a reader picks are written into the page's own URL (replacing
// the history entry, never adding one), and the server renders the form from
// it: a reload, a phone bringing a discarded tab back, or a Back from the Link
// review page all open on the same list instead of the defaults. Defaults are
// left out so the plain dashboard keeps its plain URL.
var filterURLDefaults = { status: 'reading', sort: 'latest_known_chapter', page: '1', q: '' };

var rememberFiltersInURL = function () {
    var form = document.getElementById('tracker-filters');
    if (!form || typeof window.FormData !== 'function' || !window.history || typeof window.history.replaceState !== 'function') {
        return;
    }
    var params = new window.URLSearchParams();
    new window.FormData(form).forEach(function (value, name) {
        var text = String(value || '').trim();
        if (text === '' || filterURLDefaults[name] === text) {
            return;
        }
        params.append(name, text);
    });
    var query = params.toString();
    var url = window.location.pathname + (query ? '?' + query : '');
    if (url !== window.location.pathname + window.location.search) {
        try {
            window.history.replaceState(window.history.state, '', url);
        } catch (_) {
            // A sandboxed page may refuse; the filters just are not remembered.
        }
    }
};

var trackerFiltersState = function () {
    var form = document.getElementById('tracker-filters');
    if (!form || typeof window.FormData !== 'function' || typeof window.URLSearchParams !== 'function') {
        return '';
    }
    return new window.URLSearchParams(new window.FormData(form)).toString();
};

try {
    if ('scrollRestoration' in window.history) {
        window.history.scrollRestoration = 'manual';
    }
    var savedDashboardScroll = JSON.parse(window.sessionStorage.getItem(dashboardScrollKey) || 'null');
    window.sessionStorage.removeItem(dashboardScrollKey);
    if (savedDashboardScroll && typeof savedDashboardScroll.y === 'number' && savedDashboardScroll.y > 0) {
        pendingDashboardScroll = savedDashboardScroll;
    }
} catch (_) {
    pendingDashboardScroll = null;
}

var saveDashboardScroll = function () {
    try {
        window.sessionStorage.setItem(dashboardScrollKey, JSON.stringify({
            y: Math.round(window.scrollY || 0),
            filters: trackerFiltersState()
        }));
    } catch (_) {
        // Storage refused (private mode, quota): the reload starts at the top.
    }
};

window.addEventListener('pagehide', saveDashboardScroll);
// A discarded background tab may never see pagehide; hidden is the last
// moment that is sure to run.
document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') {
        saveDashboardScroll();
    }
});

var restoreDashboardScroll = function () {
    var saved = pendingDashboardScroll;
    pendingDashboardScroll = null;
    if (!saved || window.scrollY > 0 || saved.filters !== trackerFiltersState()) {
        return;
    }
    window.scrollTo(0, saved.y);
};

// A silent refresh re-renders the same list, so what the reader holds open in
// it is opened again in the new copy: a rating popover with the value picked
// but not yet saved (and focus on its stars), the connector "+N" menu. They
// used to come back closed — after the 15s the refresh would wait for them —
// with the picked rating lost. Recorded in the capture phase, before
// dashboard-core.js takes focus out of the region.
var heldOpenMenus = null;

document.addEventListener('htmx:beforeSwap', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'trackers-zone' || detail.shouldSwap === false) {
        return;
    }
    heldOpenMenus = null;
    // A silent refresh overtaken by the reader's request answers for a list
    // that is no longer wanted; its answer is dropped, not swapped over the
    // reader's skeleton.
    if (detail.xhr && detail.xhr.__trackersSuperseded) {
        detail.shouldSwap = false;
        return;
    }
    if (!window.isQuietListRequest(detail)) {
        return;
    }
    // A silent refresh shows no skeleton, so the pinned card is still on the
    // page — unless it was deleted while the request was out.
    holdPinnedCardBeforeSwap();
    var zone = detail.target;
    var held = { ratings: [], connectorMenu: !!zone.querySelector('details.site-links-more[open]') };
    Array.prototype.forEach.call(zone.querySelectorAll('details.tracker-rating[open]'), function (menu) {
        var card = menu.closest('.tracker-card');
        var form = menu.querySelector('.tracker-rating__popover');
        if (!card || !card.id || !form) {
            return;
        }
        held.ratings.push({
            cardID: card.id,
            value: getSelectedRatingValue(form),
            focused: menu.contains(document.activeElement)
        });
    });
    heldOpenMenus = held;
}, true);

var reopenHeldMenus = function (zone) {
    var held = heldOpenMenus;
    heldOpenMenus = null;
    if (!held) {
        return;
    }
    held.ratings.forEach(function (rating) {
        var card = document.getElementById(rating.cardID);
        var menu = card ? card.querySelector('details.tracker-rating') : null;
        var form = menu ? menu.querySelector('.tracker-rating__popover') : null;
        if (!menu || !form) {
            return;
        }
        var label = normalizeRatingValue(rating.value).toFixed(1);
        form.setAttribute('data-restore-rating', label);
        setSelectedRatingValue(form, label);
        menu.open = true;
        if (rating.focused) {
            var control = form.querySelector('.js-rating-control');
            if (control) {
                control.focus({ preventScroll: true });
            }
        }
    });
    if (held.connectorMenu) {
        var connectorMenu = zone.querySelector('details.site-links-more');
        if (connectorMenu) {
            connectorMenu.open = true;
        }
    }
};

// "No trackers found — add your first source entry" was all a profile whose
// trackers are all caught up saw on opening: Reading, the default, leaves out
// the trackers whose latest chapter has been read. On Reading alone the empty
// state says so instead; with another filter on, Reading keeps them, so the
// plain hint is the right one.
var explainEmptyList = function (zone, list) {
    var empty = zone.querySelector('.empty-state');
    if (!empty || !list) {
        return;
    }
    var onReading = listStatusFilter(list) === 'reading' && !listIsNarrowed(list.params);
    Array.prototype.forEach.call(empty.querySelectorAll('[data-empty-hint]'), function (hint) {
        hint.hidden = (hint.getAttribute('data-empty-hint') === 'reading') !== onReading;
    });
};

// After an Edit save the card is swapped in place, where it stayed even when it
// no longer belonged to the list: a tracker moved from Reading to Completed, or
// stripped of the tag the list is filtered by, sat there until some other
// reload. When the saved card no longer matches the list on screen — or a
// search, tag or site filter is on, which the card cannot be checked against —
// the list is refreshed quietly.
var checkEditedCard = function (cardID) {
    var card = document.getElementById(cardID);
    if (!card) {
        return;
    }
    if (!cardMatchesShownList(card, shownTrackersList())) {
        window.dispatchTrackersChanged('silent');
    }
};

document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    var elt = detail && detail.elt;
    var xhr = detail && detail.xhr;
    if (!elt || !xhr || !elt.matches || !elt.matches('#modal-zone form.tracker-form')) {
        return;
    }
    var path = (detail.requestConfig && detail.requestConfig.path) || elt.getAttribute('hx-post') || '';
    var match = /\/dashboard\/trackers\/(\d+)(?:\?|$)/.exec(path);
    if (!match) {
        return;
    }
    // The card has been swapped by the time the request's own load handler
    // (htmx's) has run, also when the modal was closed in the meantime.
    xhr.addEventListener('load', function () {
        if (xhr.status >= 200 && xhr.status < 300) {
            checkEditedCard('tracker-card-' + match[1]);
        }
    });
});

document.body.addEventListener('htmx:afterSwap', function (event) {
    var target = event && event.target;
    if (!target) {
        return;
    }

    if (target.closest && target.closest('#filter-tags-dropdown')) {
        restoreTickedFilterTags(target);
        window.updateFilterSummaries();
    }

    if (target.id !== 'trackers-zone') {
        return;
    }

    target.style.minHeight = '';
    if (pendingDashboardScroll) {
        restoreDashboardScroll();
    }

    var list = shownTrackersList();
    quietRetryAttempts = 0;
    restorePinnedCardAfterSwap();
    reopenHeldMenus(target);
    explainEmptyList(target, list);
    scheduleCoverRefresh(list);

    if (typeof window.syncTrackerCardHoverState === 'function') {
        window.syncTrackerCardHoverState();
    }
});
