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

    fetch(requestURL, {
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

            var existing = activeContainer.querySelector('#' + card.id);
            if (existing) {
                existing.remove();
            }

            activeContainer.insertBefore(card, activeContainer.firstElementChild);
            card.style.order = '-9999';
            window.__freezeTrackersOrder = true;
            window.__pinnedTrackerID = card.id;
            if (window.htmx && typeof window.htmx.process === 'function') {
                window.htmx.process(card);
            }

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

                fetch(requestURL, {
                    credentials: 'same-origin',
                    headers: { 'HX-Request': 'true' }
                })
                    .then(function (retryResponse) {
                        if (!retryResponse.ok) {
                            throw new Error('card refresh request failed');
                        }
                        return retryResponse.text();
                    })
                    .then(function (retryHTML) {
                        var retryMarkup = String(retryHTML || '').trim();
                        if (!retryMarkup) {
                            throw new Error('empty card refresh fragment');
                        }

                        var retryBuffer = document.createElement('div');
                        retryBuffer.innerHTML = retryMarkup;
                        var refreshedCard = retryBuffer.firstElementChild;
                        if (!refreshedCard || refreshedCard.id !== card.id) {
                            throw new Error('invalid refreshed card fragment');
                        }

                        var currentCard = activeContainer.querySelector('#' + card.id);
                        if (currentCard) {
                            currentCard.replaceWith(refreshedCard);
                        } else {
                            activeContainer.insertBefore(refreshedCard, activeContainer.firstElementChild);
                        }

                        refreshedCard.style.order = '-9999';
                        activeContainer.insertBefore(refreshedCard, activeContainer.firstElementChild);
                        if (window.htmx && typeof window.htmx.process === 'function') {
                            window.htmx.process(refreshedCard);
                        }

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
    var select = document.getElementById('profile-switch');
    if (select) {
        window.onProfileSwitch(select);
    }

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
});

window.dismissModalZone = function () {
    var modalZone = document.getElementById('modal-zone');
    if (!modalZone) {
        return;
    }
    modalZone.innerHTML = '';
};

document.addEventListener('click', function (event) {
    var target = event && event.target;
    if (!target || !target.closest) {
        return;
    }

    var backdrop = target.closest('#modal-zone .modal-backdrop');
    if (!backdrop) {
        return;
    }

    if (target.closest('.modal-card')) {
        return;
    }

    window.dismissModalZone();
});

var cancelPendingTrackersRefresh = function () {
    if (!window.__pendingTrackersRefreshTimer) {
        return;
    }
    window.clearTimeout(window.__pendingTrackersRefreshTimer);
    window.__pendingTrackersRefreshTimer = null;
};

document.addEventListener('pointerdown', cancelPendingTrackersRefresh, true);
document.addEventListener('keydown', cancelPendingTrackersRefresh, true);

document.addEventListener('click', function (event) {
    var button = event.target && event.target.closest ? event.target.closest('.js-page-btn') : null;
    if (!button) {
        return;
    }

    if (button.hasAttribute('disabled')) {
        return;
    }

    var explicitPage = Number(button.getAttribute('data-page-value') || '0');
    if (explicitPage > 0) {
        var explicitPageInput = document.getElementById('page-input');
        if (explicitPageInput) {
            explicitPageInput.value = String(explicitPage);
        }

        window.__scrollTrackersToTop = true;
        window.dispatchTrackersChanged('user');
        return;
    }

    var base = Number(button.getAttribute('data-page-target') || '1');
    var delta = Number(button.getAttribute('data-page-delta') || '0');
    var nextPage = Math.max(1, base + delta);
    var pageInput = document.getElementById('page-input');
    if (pageInput) {
        pageInput.value = String(nextPage);
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

document.body.addEventListener('htmx:beforeRequest', function (event) {
    var detail = event && event.detail;
    if (!detail || !detail.target || detail.target.id !== 'trackers-zone') {
        return;
    }

    if (window.__silentCoverRefresh) {
        window.__silentCoverRefresh = false;
        return;
    }

    var viewInput = document.getElementById('view-input');
    var mode = viewInput && viewInput.value ? viewInput.value : 'grid';
    window.renderTrackersSkeleton(mode);
});

document.body.addEventListener('htmx:afterSwap', function (event) {
    var target = event && event.target;
    if (!target) {
        return;
    }

    if (target.closest && target.closest('#filter-tags-dropdown')) {
        window.updateFilterTagsSummary();
    }
    if (target.closest && target.closest('#filter-sites-dropdown')) {
        window.updateFilterSitesSummary();
    }

    if (target.id !== 'trackers-zone') {
        return;
    }

    if (window.__scrollTrackersToTop) {
        var trackersZone = document.getElementById('trackers-zone');
        if (trackersZone) {
            trackersZone.scrollIntoView({ behavior: 'smooth', block: 'start' });
        }
        window.__scrollTrackersToTop = false;
    }
});
