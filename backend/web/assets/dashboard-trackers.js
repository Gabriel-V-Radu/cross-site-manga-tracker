// Fetches one tracker's card fragment and resolves with the parsed card
// element. Rejects on network failure, an empty body, or markup whose first
// element carries no id — the id is what the insert logic keys on.
var fetchTrackerCardFragment = function (requestURL) {
    return fetch(requestURL, {
        credentials: 'same-origin',
        headers: { 'HX-Request': 'true' }
    })
        .then(function (response) {
            if (!response.ok) {
                throw new Error('card fragment request failed');
            }
            return response.text();
        })
        .then(function (html) {
            var markup = String(html || '').trim();
            if (!markup) {
                throw new Error('empty card fragment');
            }

            var buffer = document.createElement('div');
            buffer.innerHTML = markup;
            var card = buffer.firstElementChild;
            if (!card || !card.id) {
                throw new Error('invalid card fragment');
            }
            return card;
        });
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

    var listContainer = document.getElementById('cards-container-list');
    var gridContainer = document.getElementById('cards-container-grid');
    var activeContainer = listContainer || gridContainer;
    var viewMode = listContainer ? 'list' : 'grid';
    if (!activeContainer) {
        window.dispatchTrackersChanged('system');
        return;
    }

    var profileInput = document.getElementById('profile-filter');
    var profileKey = profileInput && profileInput.value ? String(profileInput.value).trim() : '';
    var requestURL = '/dashboard/trackers/' + encodeURIComponent(String(trackerID)) + '/card-fragment?view=' + encodeURIComponent(viewMode);
    if (profileKey) {
        requestURL += '&profile=' + encodeURIComponent(profileKey);
    }

    fetchTrackerCardFragment(requestURL)
        .then(function (card) {
            placeTrackerCardAtTop(activeContainer, card);
            window.__freezeTrackersOrder = true;
            window.__pinnedTrackerID = card.id;

            var shouldRetryCover = (viewMode === 'grid');
            if (!shouldRetryCover) {
                return;
            }

            var retryCount = 0;
            var maxRetries = 4;
            var retryDelayMs = 1200;
            var retryLoadCard = function () {
                if (window.__pinnedTrackerID !== card.id) {
                    return;
                }
                if (retryCount >= maxRetries) {
                    return;
                }
                retryCount += 1;

                fetchTrackerCardFragment(requestURL)
                    .then(function (refreshedCard) {
                        if (refreshedCard.id !== card.id) {
                            throw new Error('invalid refreshed card fragment');
                        }

                        placeTrackerCardAtTop(activeContainer, refreshedCard);

                        var hasCoverImage = !!refreshedCard.querySelector('.tracker-card__cover img');
                        if (!hasCoverImage && retryCount < maxRetries) {
                            window.setTimeout(retryLoadCard, retryDelayMs);
                        }
                    })
                    .catch(function () {
                        if (retryCount < maxRetries) {
                            window.setTimeout(retryLoadCard, retryDelayMs);
                        }
                    });
            };

            window.setTimeout(retryLoadCard, retryDelayMs);
        })
        .catch(function () {
            window.dispatchTrackersChanged('system');
        });
});

document.addEventListener('DOMContentLoaded', function () {
    var initialViewInput = document.getElementById('view-input');
    window.setDashboardViewMode(initialViewInput && initialViewInput.value ? initialViewInput.value : 'grid', false);
    window.updateFilterTagsSummary();
    window.updateFilterSitesSummary();

    var filtersForm = document.getElementById('tracker-filters');
    if (filtersForm) {
        var dispatchTrackersChanged = function () {
            window.dispatchTrackersChanged('user');
        };

        var searchDebounceTimer = null;
        var scheduleSearchRefresh = function () {
            if (searchDebounceTimer) {
                window.clearTimeout(searchDebounceTimer);
            }
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
            if (searchDebounceTimer) {
                window.clearTimeout(searchDebounceTimer);
                searchDebounceTimer = null;
            }
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
            } else if (target.name === 'tags' || target.name === 'sites') {
                shouldRefresh = true;
            }

            if (!shouldRefresh) {
                return;
            }

            var changedPageInput = document.getElementById('page-input');
            if (changedPageInput) {
                changedPageInput.value = '1';
            }
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

window.updateFilterTagsSummary = function () {
    var dropdown = document.getElementById('filter-tags-dropdown');
    var summary = document.getElementById('filter-tags-summary');
    if (!dropdown || !summary) {
        return;
    }

    var checks = dropdown.querySelectorAll('input[name="tags"]:checked');
    summary.textContent = String(checks ? checks.length : 0);
};

window.updateFilterSitesSummary = function () {
    var dropdown = document.getElementById('filter-sites-dropdown');
    var summary = document.getElementById('filter-sites-summary');
    if (!dropdown || !summary) {
        return;
    }

    var checks = dropdown.querySelectorAll('input[name="sites"]:checked');
    summary.textContent = String(checks ? checks.length : 0);
};

document.addEventListener('change', function (event) {
    var target = event.target;
    if (!target) {
        return;
    }
    if (target.name === 'tags') {
        window.updateFilterTagsSummary();
        return;
    }
    if (target.name === 'sites') {
        window.updateFilterSitesSummary();
    }
});

document.addEventListener('click', function (event) {
    var option = event.target && event.target.closest ? event.target.closest('[data-view-mode]') : null;
    if (!option) {
        return;
    }
    var nextMode = option.getAttribute('data-view-mode') || 'grid';
    var viewInput = document.getElementById('view-input');
    var currentMode = viewInput && viewInput.value ? viewInput.value : 'grid';
    if (currentMode === nextMode) {
        return;
    }
    window.setDashboardViewMode(nextMode, true);
});

document.addEventListener('click', function (event) {
    var closeDropdownIfOutside = function (dropdownID) {
        var dropdown = document.getElementById(dropdownID);
        if (!dropdown || !dropdown.hasAttribute('open')) {
            return;
        }
        if (dropdown.contains(event.target)) {
            return;
        }
        dropdown.removeAttribute('open');
    };

    closeDropdownIfOutside('filter-tags-dropdown');
    closeDropdownIfOutside('filter-sites-dropdown');

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
// carried across the swap by name, case-insensitively as the list filters
// them, and a ticked tag that is gone takes its filter with it.
var tickedTagNamesBeforeSwap = null;

document.body.addEventListener('htmx:beforeSwap', function (event) {
    var target = event && event.detail ? event.detail.target : null;
    if (!target || !target.closest || !target.closest('#filter-tags-dropdown')) {
        return;
    }

    // Prototype-less, so a tag called "constructor" is not found ticked.
    tickedTagNamesBeforeSwap = Object.create(null);
    Array.prototype.forEach.call(target.querySelectorAll('input[name="tags"]:checked'), function (box) {
        tickedTagNamesBeforeSwap[String(box.value).toLowerCase()] = true;
    });
});

var restoreTickedFilterTags = function (menu) {
    var wanted = tickedTagNamesBeforeSwap;
    tickedTagNamesBeforeSwap = null;
    if (!wanted) {
        return;
    }

    var present = Object.create(null);
    Array.prototype.forEach.call(menu.querySelectorAll('input[name="tags"]'), function (box) {
        var name = String(box.value).toLowerCase();
        if (wanted[name]) {
            box.checked = true;
            present[name] = true;
        }
    });

    var lostTickedTag = Object.keys(wanted).some(function (name) {
        return !present[name];
    });
    if (!lostTickedTag) {
        return;
    }

    // The list was fetched with the old names before the menu came back; ask
    // again so it matches the ticks the menu now shows.
    var lostTagPageInput = document.getElementById('page-input');
    if (lostTagPageInput) {
        lostTagPageInput.value = '1';
    }
    window.dispatchTrackersChanged('system');
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

window.renderTrackersSkeleton = function (viewMode) {
    var trackersZone = document.getElementById('trackers-zone');
    if (!trackersZone) {
        return;
    }

    var mode = (viewMode === 'list') ? 'list' : 'grid';
    var itemCount = 6;
    var items = [];

    for (var i = 0; i < itemCount; i += 1) {
        if (mode === 'list') {
            items.push('' +
                '<article class="tracker-row tracker-card tracker-row--skeleton">' +
                    '<div class="tracker-row__title-wrap"><div class="skeleton skeleton--title skeleton--title-wide"></div></div>' +
                    '<div class="tracker-row__status"><div class="skeleton skeleton--badge"></div></div>' +
                    '<div class="tracker-row__metric"><div class="skeleton skeleton--chip"></div><div class="skeleton skeleton--line skeleton--line-short"></div></div>' +
                    '<div class="tracker-row__metric"><div class="skeleton skeleton--chip"></div><div class="skeleton skeleton--line skeleton--line-short"></div></div>' +
                    '<div class="tracker-row__actions"><div class="skeleton skeleton--btn"></div><div class="skeleton skeleton--btn"></div><div class="skeleton skeleton--btn"></div></div>' +
                '</article>');
            continue;
        }

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
        '<div class="' + (mode === 'list' ? 'cards-list' : 'cards-grid') + '">' + items.join('') + '</div>';
};

// A silent cover refresh replaces the whole trackers region, and everything
// the user was holding open or pointing at goes with it: the connector menu
// and the rating popovers are <details> elements that come back closed, and a
// hovered link loses its highlight for the instant the element is swapped —
// which reads as the page flickering under the cursor for no reason. The
// covers being waited on are never urgent, so the refresh yields to whatever
// the user is doing and asks again shortly. Hover only counts over the
// connector strip: the pointer rests over the card grid most of the time, and
// deferring on that would stop covers from ever arriving.
window.trackersInteractionInProgress = function () {
    var zone = document.getElementById('trackers-zone');
    if (!zone) {
        return false;
    }

    if (zone.querySelector('details[open]')) {
        return true;
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

document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'trackers-zone') {
        return;
    }

    if (window.__silentCoverRefresh) {
        window.__silentCoverRefresh = false;
        return;
    }

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

    var viewInput = document.getElementById('view-input');
    var mode = viewInput && viewInput.value ? viewInput.value : 'grid';
    window.renderTrackersSkeleton(mode);
});

document.body.addEventListener('htmx:afterRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || detail.successful || !detail.target || detail.target.id !== 'trackers-zone') {
        return;
    }
    // Nothing is swapped in for a failed request. The skeleton only ever
    // meant "loading": left under the error strip, with "Loading trackers…"
    // above it, it read as a load still under way when none would come. It
    // goes, and so does the old list's height. A reload's scroll offset
    // belonged to the list that failed to come back, so it is not applied to
    // whichever list loads later.
    var failedZone = detail.target;
    Array.prototype.forEach.call(failedZone.querySelectorAll('.trackers-loading__label, .tracker-card--skeleton, .tracker-row--skeleton'), function (node) {
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
var dashboardScrollKey = 'dashboard-scroll:' + window.location.pathname + window.location.search;
var pendingDashboardScroll = null;

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

document.body.addEventListener('htmx:afterSwap', function (event) {
    var target = event && event.target;
    if (!target) {
        return;
    }

    if (target.closest && target.closest('#filter-tags-dropdown')) {
        restoreTickedFilterTags(target);
        window.updateFilterTagsSummary();
    }
    if (target.closest && target.closest('#filter-sites-dropdown')) {
        window.updateFilterSitesSummary();
    }

    if (target.id !== 'trackers-zone') {
        return;
    }

    target.style.minHeight = '';
    if (pendingDashboardScroll) {
        restoreDashboardScroll();
    }

    // A tracker created moments ago is pinned to the top of the container, and
    // that pin has to survive every re-render of the list (this used to be an
    // inline script inside the trackers partial, run on each swap).
    if (window.__freezeTrackersOrder && window.__pinnedTrackerID) {
        var pinnedContainer = document.getElementById('cards-container-list') || document.getElementById('cards-container-grid');
        var pinnedCard = document.getElementById(window.__pinnedTrackerID);
        if (pinnedContainer && pinnedCard) {
            if (pinnedContainer.firstElementChild !== pinnedCard) {
                pinnedContainer.insertBefore(pinnedCard, pinnedContainer.firstElementChild);
            }
            pinnedCard.style.order = '-9999';
        }
    }

    if (typeof window.syncTrackerCardHoverState === 'function') {
        window.syncTrackerCardHoverState();
    }
});
