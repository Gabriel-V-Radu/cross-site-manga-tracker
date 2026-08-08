window.renderLinkedSources = function (form) {
    if (!form) {
        return;
    }

    var hidden = form.querySelector('#linked-sources-json');
    var list = form.querySelector('#linked-sources-list');
    if (!hidden || !list) {
        return;
    }

    var items = [];
    try {
        items = JSON.parse(hidden.value || '[]');
    } catch (_) {
        items = [];
    }

    if (!Array.isArray(items) || items.length === 0) {
        list.innerHTML = '<p class="search-message">No linked sites yet.</p>';
        return;
    }

    var html = items.map(function (item, index) {
        var sourceName = window.escapeHtml(item.sourceName || ('Source #' + item.sourceId));
        var sourceUrl = window.escapeHtml(item.sourceUrl || '');
        return '' +
            '<div class="linked-source-row">' +
            '<span class="linked-source-name">' + sourceName + '</span>' +
            '<a class="linked-btn" href="' + sourceUrl + '" target="_blank" rel="noopener noreferrer">Open</a>' +
            '<button type="button" class="linked-btn linked-btn--danger" onclick="window.removeTrackerLinkedSource(' + index + ', this)">Remove</button>' +
            '</div>';
    }).join('');

    list.innerHTML = html;
};

window.parseRelatedTitlesDataset = function (rawValue) {
    var trimmed = String(rawValue || '').trim();
    if (!trimmed) {
        return [];
    }

    try {
        var parsed = JSON.parse(trimmed);
        if (!Array.isArray(parsed)) {
            return [];
        }
        return parsed.filter(function (item) {
            return typeof item === 'string' && item.trim() !== '';
        });
    } catch (_) {
        return [];
    }
};

window.syncLinkedSourceSelect = function (form) {
    if (!form) {
        return;
    }

    var select = form.querySelector('#linked-source-id');
    var hidden = form.querySelector('#linked-sources-json');
    var allSourcesHidden = form.querySelector('#all-sources-json');
    if (!select || !hidden || !allSourcesHidden) {
        return;
    }

    var linkedItems = [];
    try {
        linkedItems = JSON.parse(hidden.value || '[]');
    } catch (_) {
        linkedItems = [];
    }
    if (!Array.isArray(linkedItems)) {
        linkedItems = [];
    }

    var allSources = [];
    try {
        allSources = JSON.parse(allSourcesHidden.value || '[]');
    } catch (_) {
        allSources = [];
    }
    if (!Array.isArray(allSources)) {
        allSources = [];
    }

    var linkedSourceIDs = {};
    linkedItems.forEach(function (item) {
        var sourceID = Number(item && item.sourceId);
        if (sourceID > 0) {
            linkedSourceIDs[sourceID] = true;
        }
    });

    var previousValue = select.value;
    var optionHtml = '<option value="">Select source</option>';
    var hasPreviousValue = false;

    allSources.forEach(function (source) {
        var sourceID = Number(source && source.id);
        if (!sourceID || linkedSourceIDs[sourceID]) {
            return;
        }

        var value = String(sourceID);
        if (value === previousValue) {
            hasPreviousValue = true;
        }

        optionHtml += '<option value="' + value + '">' + window.escapeHtml(source.name || ('Source #' + value)) + '</option>';
    });

    select.innerHTML = optionHtml;
    select.value = hasPreviousValue ? previousValue : '';
};

window.removeTrackerLinkedSource = function (index, button) {
    var form = button && (button.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form'));
    if (!form) {
        return;
    }

    var hidden = form.querySelector('#linked-sources-json');
    if (!hidden) {
        return;
    }

    var items = [];
    try {
        items = JSON.parse(hidden.value || '[]');
    } catch (_) {
        items = [];
    }
    if (!Array.isArray(items)) {
        items = [];
    }

    items.splice(index, 1);
    hidden.value = JSON.stringify(items);
    window.renderLinkedSources(form);
    window.syncLinkedSourceSelect(form);
};

window.addTrackerLinkedSource = function (button) {
    if (!button) {
        return;
    }

    var form = button.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form');
    if (!form) {
        return;
    }

    var hidden = form.querySelector('#linked-sources-json');
    if (!hidden) {
        return;
    }

    var sourceId = parseInt(button.dataset.sourceId || '0', 10);
    var sourceUrl = button.dataset.url || '';
    if (!sourceId || !sourceUrl) {
        return;
    }

    var items = [];
    try {
        items = JSON.parse(hidden.value || '[]');
    } catch (_) {
        items = [];
    }
    if (!Array.isArray(items)) {
        items = [];
    }

    var alreadyExists = items.some(function (item) {
        return Number(item.sourceId) === sourceId && String(item.sourceUrl || '').toLowerCase() === sourceUrl.toLowerCase();
    });
    if (alreadyExists) {
        return;
    }

    items.push({
        sourceId: sourceId,
        sourceName: button.dataset.sourceName || '',
        sourceItemId: button.dataset.sourceItemId || '',
        sourceUrl: sourceUrl
    });
    hidden.value = JSON.stringify(items);

    // Whether this source should take over as primary rests entirely on its
    // chapter number, so a listing that carries none has to be asked before the
    // question can be answered at all.
    var promoteIfAhead = function (latestRaw, latestReleaseAt) {
        var latestKnownField = form.querySelector('input[name="latest_known_chapter"]');
        var incomingLatest = parseFloat(String(latestRaw || '').trim());
        if (Number.isNaN(incomingLatest)) {
            return;
        }

        var currentLatest = NaN;
        if (latestKnownField) {
            currentLatest = parseFloat(String(latestKnownField.value || '').trim());
        }
        if (!Number.isNaN(currentLatest) && incomingLatest <= currentLatest) {
            return;
        }

        var setField = function (selector, value) {
            if (value === undefined || value === null) {
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

        setField('select[name="source_id"]', String(sourceId));
        setField('input[name="source_url"]', sourceUrl);
        setField('input[name="source_item_id"]', button.dataset.sourceItemId || '');
        setField('input[name="latest_known_chapter"]', String(incomingLatest));
        setField('input[name="related_titles_json"]', JSON.stringify(window.parseRelatedTitlesDataset(button.dataset.relatedTitles)));

        var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
        if (latestReleaseField && typeof latestReleaseAt !== 'undefined') {
            latestReleaseField.value = latestReleaseAt || '';
        }
    };

    if (String(button.dataset.latestChapter || '').trim()) {
        promoteIfAhead(button.dataset.latestChapter, button.dataset.latestReleaseAt);
    } else {
        window.requestSourceChapter(sourceId, sourceUrl).then(function (payload) {
            if (payload && !payload.error) {
                promoteIfAhead(payload.latestChapter, payload.latestReleaseAt);
            }
        });
    }

    window.renderLinkedSources(form);
    window.syncLinkedSourceSelect(form);
};

// Some sources cannot put a chapter number in their search listing — MangaFire's
// spans every language it hosts and MangaBuddy's carries no chapters at all — so
// the number is fetched for the one result the user picked, rather than left for
// the first poll after saving to discover. Resolves to null when the source has
// no number to give or could not be read; both leave the field to the user.
window.requestSourceChapter = function (sourceId, sourceUrl) {
    var endpoint = '/dashboard/trackers/source-chapter?source_id=' +
        encodeURIComponent(String(sourceId)) + '&url=' + encodeURIComponent(sourceUrl);

    return fetch(endpoint, { headers: { Accept: 'application/json' } })
        .then(function (response) {
            // A server that predates this endpoint answers 404 with an HTML
            // body, which would otherwise surface as an unexplained blank field.
            if (!response.ok && response.status !== 200) {
                return { error: 'This source lookup is not available on the server (HTTP ' + response.status + ')' };
            }
            return response.json();
        })
        .then(function (payload) {
            if (!payload) {
                return { error: 'Empty response from the source lookup' };
            }
            return payload;
        })
        .catch(function () {
            return { error: 'Could not reach the source lookup' };
        });
};

window.fetchTrackerSourceChapter = function (form, sourceId, sourceUrl) {
    if (!form || !sourceId || !sourceUrl) {
        return;
    }

    var chapterField = form.querySelector('input[name="latest_known_chapter"]');
    if (!chapterField) {
        return;
    }

    // The request outlives the click, so a second pick supersedes this one. The
    // token is what tells them apart: only the newest lookup owns the field's
    // "checking" state, so an older answer arriving late neither clears the
    // newer one's indicator nor writes over its result.
    var requestToken = (form.dataset.chapterLookupToken = String(Date.now()) + ':' + sourceUrl);
    var isCurrentRequest = function () {
        return form.dataset.chapterLookupToken === requestToken;
    };

    // Whatever the user does in the meantime also wins: a number typed by hand
    // or a changed URL leaves a late answer with nothing to fill.
    var stillWanted = function () {
        var urlField = form.querySelector('input[name="source_url"]');
        return String(chapterField.value || '').trim() === '' &&
            (!urlField || String(urlField.value || '').trim() === sourceUrl);
    };

    var note = form.querySelector('[data-chapter-lookup-note]');
    var say = function (message) {
        if (!note) {
            return;
        }
        note.textContent = message || '';
        note.hidden = !message;
    };

    chapterField.classList.add('is-loading');
    var previousPlaceholder = chapterField.placeholder;
    chapterField.placeholder = 'Checking…';
    say('');

    window.requestSourceChapter(sourceId, sourceUrl).then(function (payload) {
        if (!isCurrentRequest()) {
            return;
        }

        // Clearing the indicator is not conditional on the answer being usable —
        // otherwise a number typed while the lookup was in flight would leave
        // the field reading "Checking…" for good.
        chapterField.classList.remove('is-loading');
        chapterField.placeholder = previousPlaceholder;

        if (!stillWanted()) {
            return;
        }

        // A lookup that failed and a title with no chapters both leave the field
        // empty, but they are not the same thing and must not look the same:
        // silence here is what made a stale script indistinguishable from a
        // title with nothing to report.
        if (payload.error) {
            say(payload.error);
            return;
        }
        if (!payload.latestChapter) {
            say('No chapter number available for this title');
            return;
        }

        chapterField.value = payload.latestChapter;
        chapterField.dispatchEvent(new Event('input', { bubbles: true }));
        chapterField.dispatchEvent(new Event('change', { bubbles: true }));

        var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
        if (latestReleaseField && payload.latestReleaseAt) {
            latestReleaseField.value = payload.latestReleaseAt;
        }
    });
};

window.applyTrackerSearchResult = function (button) {
    if (!button) {
        return;
    }

    var form = button.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form');
    if (!form) {
        return;
    }

    var setField = function (selector, value) {
        if (value === undefined || value === null || value === '') {
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

    setField('input[name="title"]', button.dataset.title || '');
    setField('input[name="source_url"]', button.dataset.url || '');
    setField('input[name="source_item_id"]', button.dataset.sourceItemId || '');
    setField('input[name="related_titles_json"]', JSON.stringify(window.parseRelatedTitlesDataset(button.dataset.relatedTitles)));
    setField('input[name="latest_known_chapter"]', button.dataset.latestChapter || '');

    var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
    if (latestReleaseField) {
        latestReleaseField.value = button.dataset.latestReleaseAt || '';
    }

    if (!String(button.dataset.latestChapter || '').trim()) {
        window.fetchTrackerSourceChapter(form, button.dataset.sourceId, button.dataset.url || '');
    }
};
