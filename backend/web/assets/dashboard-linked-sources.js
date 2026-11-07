window.renderLinkedSources = function (form) {
    if (!form) {
        return;
    }

    var hidden = form.querySelector('#linked-sources-json');
    var list = form.querySelector('#linked-sources-list');
    if (!hidden || !list) {
        return;
    }

    var items = window.parseJSONArray(hidden.value);
    window.syncReadingSiteSelect(form);

    if (items.length === 0) {
        list.innerHTML = '<p class="search-message">No linked sites yet.</p>';
        return;
    }

    var html = items.map(function (item, index) {
        var sourceName = window.escapeHtml(item.sourceName || ('Source #' + item.sourceId));
        var sourceUrl = window.escapeHtml(window.safeHttpUrl(item.sourceUrl));
        return '' +
            '<div class="linked-source-row">' +
            '<span class="linked-source-name">' + sourceName + '</span>' +
            '<a class="linked-btn" href="' + sourceUrl + '" target="_blank" rel="noopener noreferrer">Open</a>' +
            '<button type="button" class="linked-btn linked-btn--danger" onclick="window.removeTrackerLinkedSource(' + index + ', this)">Remove</button>' +
            '</div>';
    }).join('');

    list.innerHTML = html;
};

// What the last search pick (not the user) wrote into the chapter and item-id
// fields, kept on the form so the next pick can tell its own leftovers from a
// value the user typed or the edit form opened with: only the former may be
// cleared (see applyTrackerSearchResult).
var pickedTrackerFieldKeys = {
    'input[name="latest_known_chapter"]': 'pickedLatestKnownChapter',
    'input[name="source_item_id"]': 'pickedSourceItemId'
};

var rememberPickedTrackerField = function (form, selector, wrote) {
    var field = form.querySelector(selector);
    form.dataset[pickedTrackerFieldKeys[selector]] = (wrote && field) ? field.value : '';
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

    var linkedItems = window.parseJSONArray(hidden.value);
    var allSources = window.parseJSONArray(allSourcesHidden.value);

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

// Reading Site offers the linked sites, but only the server filled it in, when
// the form opened: a site added here could not be chosen until the form was
// closed and opened again, and a removed one stayed on offer, so a save could
// pin reading to a site the tracker no longer links. The options follow the
// list as it changes. The current choice stays while its site is still linked
// and falls back to Auto when it is not.
window.syncReadingSiteSelect = function (form) {
    if (!form) {
        return;
    }

    var select = form.querySelector('#reading-source-id');
    var hidden = form.querySelector('#linked-sources-json');
    if (!select || !hidden) {
        return;
    }

    var sourceNames = {};
    var allSourcesHidden = form.querySelector('#all-sources-json');
    window.parseJSONArray(allSourcesHidden ? allSourcesHidden.value : '').forEach(function (source) {
        var sourceID = Number(source && source.id);
        if (sourceID > 0) {
            sourceNames[sourceID] = source.name || '';
        }
    });

    var previousValue = select.value;
    var hasPreviousValue = false;
    var listed = {};
    var optionHtml = '<option value="0">Auto (best available)</option>';

    window.parseJSONArray(hidden.value).forEach(function (item) {
        var sourceID = Number(item && item.sourceId);
        if (!(sourceID > 0) || listed[sourceID]) {
            return;
        }
        listed[sourceID] = true;

        var value = String(sourceID);
        if (value === previousValue) {
            hasPreviousValue = true;
        }

        var name = item.sourceName || sourceNames[sourceID] || ('Source #' + value);
        optionHtml += '<option value="' + value + '">' + window.escapeHtml(name) + '</option>';
    });

    select.innerHTML = optionHtml;
    select.value = hasPreviousValue ? previousValue : '0';
};

// A linked site whose chapter is ahead takes over the main fields the moment it
// is added (see addTrackerLinkedSource). Removing it again used to leave it
// there: the form still named it as the source, with its URL and chapter, and
// the save, which keeps the main fields as sent whenever the linked list ends up
// as it was stored, made the removed site the primary and linked it back. So
// each promotion remembers the fields as they stood before it, and removing the
// site puts back the ones that still hold what it wrote. A field the user has
// changed since, a chapter typed by hand say, is theirs and stays.
var promotedTrackerFields = [
    'select[name="source_id"]',
    'input[name="source_url"]',
    'input[name="source_item_id"]',
    'input[name="related_titles_json"]',
    'input[name="latest_release_at"]',
    'input[name="latest_known_chapter"]'
];

var promotedSourceURLField = 'input[name="source_url"]';

var linkedSourceKey = function (sourceId, sourceUrl) {
    return String(Number(sourceId) || 0) + '|' + String(sourceUrl || '').trim().toLowerCase();
};

// The pick markers (rememberPickedTrackerField) travel with the fields they
// describe, or a restored item id or chapter would still count as the
// promotion's leftover and be cleared by the next search pick.
var snapshotPromotedFields = function (form) {
    var snapshot = { values: {}, picked: {} };
    promotedTrackerFields.forEach(function (selector) {
        var field = form.querySelector(selector);
        snapshot.values[selector] = field ? field.value : null;
        if (pickedTrackerFieldKeys[selector]) {
            snapshot.picked[selector] = form.dataset[pickedTrackerFieldKeys[selector]] || '';
        }
    });
    return snapshot;
};

// The source select is written without events: the main title search re-runs
// when it changes (tracker_form_modal.html), and a promotion is not the user
// choosing a site to search. It fired that search, a live one on the site
// whenever the search box held a title.
var writePromotedField = function (form, selector, value) {
    var field = form.querySelector(selector);
    if (!field || value === null || value === undefined) {
        return;
    }
    if (field.tagName === 'SELECT') {
        field.value = value;
        return;
    }
    window.setTrackerFormField(form, selector, value, true);
};

var undoLinkedSourcePromotion = function (form, item) {
    if (!item) {
        return;
    }
    var key = linkedSourceKey(item.sourceId, item.sourceUrl);

    // A chapter lookup still under way for the removed site must not promote
    // it when it answers.
    if (form.__linkedSourceLookups) {
        delete form.__linkedSourceLookups[key];
    }

    var promotions = form.__linkedSourcePromotions || [];
    for (var index = promotions.length - 1; index >= 0; index--) {
        var promotion = promotions[index];
        if (promotion.key !== key) {
            continue;
        }
        promotions.splice(index, 1);

        // The URL says whose the main fields are: a later promotion, a search
        // pick or a URL typed by hand replaces it, and then this site is no
        // longer what the form saves as its source.
        var current = snapshotPromotedFields(form);
        if (current.values[promotedSourceURLField] === promotion.after.values[promotedSourceURLField]) {
            promotedTrackerFields.forEach(function (selector) {
                if (current.values[selector] !== promotion.after.values[selector]) {
                    return;
                }
                writePromotedField(form, selector, promotion.before.values[selector]);
                if (pickedTrackerFieldKeys[selector]) {
                    form.dataset[pickedTrackerFieldKeys[selector]] = promotion.before.picked[selector];
                }
            });
            continue;
        }

        // A later promotion took over from this one and would hand the fields
        // back to it, a site no longer linked; it hands them back to what this
        // one replaced instead.
        promotions.slice(index).forEach(function (later) {
            if (later.before.values[promotedSourceURLField] !== promotion.after.values[promotedSourceURLField]) {
                return;
            }
            promotedTrackerFields.forEach(function (selector) {
                if (later.before.values[selector] !== promotion.after.values[selector]) {
                    return;
                }
                later.before.values[selector] = promotion.before.values[selector];
                if (pickedTrackerFieldKeys[selector]) {
                    later.before.picked[selector] = promotion.before.picked[selector];
                }
            });
        });
    }
};

window.removeTrackerLinkedSource = function (index, button) {
    var form = window.findTrackerForm(button);
    if (!form) {
        return;
    }

    var hidden = form.querySelector('#linked-sources-json');
    if (!hidden) {
        return;
    }

    var items = window.parseJSONArray(hidden.value);

    var removed = items.splice(index, 1);
    hidden.value = JSON.stringify(items);
    undoLinkedSourcePromotion(form, removed[0]);
    window.renderLinkedSources(form);
    window.syncLinkedSourceSelect(form);

    // "<site> added" no longer holds once a site is removed again; results of
    // a search in progress are left where they are.
    var addedNotice = form.querySelector('#linked-search-results [data-linked-source-added]');
    if (addedNotice) {
        addedNotice.remove();
    }
};

window.addTrackerLinkedSource = function (button) {
    if (!button) {
        return;
    }

    var form = window.findTrackerForm(button);
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
    // Read now: the result list is replaced below, and the chapter lookup can
    // answer long after.
    var sourceName = button.dataset.sourceName || '';
    var sourceItemId = button.dataset.sourceItemId || '';
    var relatedTitles = window.parseRelatedTitlesDataset(button.dataset.relatedTitles);

    var items = window.parseJSONArray(hidden.value);

    var alreadyExists = items.some(function (item) {
        return Number(item.sourceId) === sourceId && String(item.sourceUrl || '').toLowerCase() === sourceUrl.toLowerCase();
    });
    if (alreadyExists) {
        return;
    }

    items.push({
        sourceId: sourceId,
        sourceName: sourceName,
        sourceItemId: sourceItemId,
        sourceUrl: sourceUrl
    });
    hidden.value = JSON.stringify(items);

    var key = linkedSourceKey(sourceId, sourceUrl);

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

        var before = snapshotPromotedFields(form);

        // Empty values are written too: an empty source_item_id must still
        // overwrite whatever the previous primary source left behind.
        writePromotedField(form, 'select[name="source_id"]', String(sourceId));
        writePromotedField(form, 'input[name="source_url"]', sourceUrl);
        writePromotedField(form, 'input[name="source_item_id"]', sourceItemId);
        writePromotedField(form, 'input[name="latest_known_chapter"]', String(incomingLatest));
        writePromotedField(form, 'input[name="related_titles_json"]', JSON.stringify(relatedTitles));
        rememberPickedTrackerField(form, 'input[name="source_item_id"]', true);
        rememberPickedTrackerField(form, 'input[name="latest_known_chapter"]', true);

        var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
        if (latestReleaseField && typeof latestReleaseAt !== 'undefined') {
            latestReleaseField.value = latestReleaseAt || '';
        }

        var promotions = form.__linkedSourcePromotions || (form.__linkedSourcePromotions = []);
        promotions.push({ key: key, before: before, after: snapshotPromotedFields(form) });
    };

    if (String(button.dataset.latestChapter || '').trim()) {
        promoteIfAhead(button.dataset.latestChapter, button.dataset.latestReleaseAt);
    } else {
        // The answer can come after the site was removed again, or removed and
        // added once more: only the lookup for the site as it is linked now may
        // promote it (undoLinkedSourcePromotion drops the entry on removal).
        var lookups = form.__linkedSourceLookups || (form.__linkedSourceLookups = {});
        var lookup = {};
        lookups[key] = lookup;
        window.requestSourceChapter(sourceId, sourceUrl).then(function (payload) {
            if (lookups[key] !== lookup) {
                return;
            }
            delete lookups[key];
            if (payload && !payload.error) {
                promoteIfAhead(payload.latestChapter, payload.latestReleaseAt);
            }
        });
    }

    window.renderLinkedSources(form);
    window.syncLinkedSourceSelect(form);

    // The results stayed on screen after the pick, all still clickable, though
    // the site had already left Site to Add: clicking the same one again did
    // nothing, and another one linked the same site a second time. They give
    // way to what just happened. The title stays in the search box, so picking
    // the next site searches it there straight away.
    var results = form.querySelector('#linked-search-results');
    if (results) {
        results.innerHTML = '<p class="search-message" data-linked-source-added>' +
            window.escapeHtml((sourceName || 'The site') + ' added. Pick another Site to Add to search it for this title too.') +
            '</p>';
    }
};

// Some sources cannot put a chapter number in their search listing — MangaFire's
// spans every language it hosts and MangaBuddy's carries no chapters at all — so
// the number is fetched for the one result the user picked, rather than left for
// the first poll after saving to discover. It always resolves: to the source's
// answer, or to { error } saying why there is none, which leaves the field to
// the user.
window.requestSourceChapter = function (sourceId, sourceUrl) {
    var endpoint = '/dashboard/trackers/source-chapter?source_id=' +
        encodeURIComponent(String(sourceId)) + '&url=' + encodeURIComponent(sourceUrl);

    return fetch(endpoint, { headers: { Accept: 'application/json' } })
        .then(function (response) {
            return response.text().then(function (body) {
                return sourceChapterAnswer(response, body);
            });
        })
        .catch(function () {
            return { error: 'Could not reach the source lookup' };
        });
};

// Every refusal used to read "not available on the server", a server that
// crashed on the lookup included, which sent the reader looking for a missing
// endpoint. The endpoint explains its own refusals in a JSON error, and that
// is what is shown. Otherwise only a 404 means the endpoint is missing (a
// server that predates it answers one with an HTML page); any other status is
// a lookup that failed, said with the server's own short text when it sent
// one, as the htmx requests do (dashboard-core.js).
var sourceChapterAnswer = function (response, body) {
    var text = String(body || '').trim();
    var payload = null;
    try {
        payload = JSON.parse(text);
    } catch (_) {
        payload = null;
    }
    var hasPayload = !!payload && typeof payload === 'object' && !Array.isArray(payload);

    if (response.ok) {
        if (!text) {
            return { error: 'Empty response from the source lookup' };
        }
        return hasPayload ? payload : { error: 'Unreadable response from the source lookup' };
    }

    if (hasPayload && typeof payload.error === 'string' && payload.error.trim()) {
        return { error: payload.error.trim() };
    }
    if (response.status === 404) {
        return { error: 'This source lookup is not available on the server (HTTP 404)' };
    }
    if (text && text.charAt(0) !== '<' && text.length <= 300 && !hasPayload) {
        return { error: 'The source lookup failed: ' + text };
    }
    return { error: 'The source lookup failed (HTTP ' + response.status + ')' };
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
        // Still the pick's number, not the user's: a later pick may replace it.
        rememberPickedTrackerField(form, 'input[name="latest_known_chapter"]', true);

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

    var form = window.findTrackerForm(button);
    if (!form) {
        return;
    }

    // No allowEmpty here: a search result missing a field must not blank out
    // what the user already has in the form.
    var setField = function (selector, value) {
        window.setTrackerFormField(form, selector, value, false);
    };

    // Except over the previous pick's own leftovers. A result whose listing
    // carries no chapter (MangaFire's never does) used to keep the number the
    // previous pick wrote, and the lookup below, which only fills an empty
    // field, then threw the right number away: the tracker was saved with the
    // first series' chapter against the second series' URL. A number the user
    // typed, or the edit form opened with, is still left alone.
    var replacePickedField = function (selector, incoming) {
        var field = form.querySelector(selector);
        if (!field) {
            return;
        }
        var value = String(incoming || '').trim();
        var leftover = form.dataset[pickedTrackerFieldKeys[selector]];
        if (value) {
            setField(selector, value);
        } else if (leftover && field.value === leftover) {
            window.setTrackerFormField(form, selector, '', true);
        }
        rememberPickedTrackerField(form, selector, value !== '');
    };

    setField('input[name="title"]', button.dataset.title || '');
    setField('input[name="source_url"]', button.dataset.url || '');
    replacePickedField('input[name="source_item_id"]', button.dataset.sourceItemId);
    setField('input[name="related_titles_json"]', JSON.stringify(window.parseRelatedTitlesDataset(button.dataset.relatedTitles)));
    replacePickedField('input[name="latest_known_chapter"]', button.dataset.latestChapter);

    var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
    if (latestReleaseField) {
        latestReleaseField.value = button.dataset.latestReleaseAt || '';
    }

    if (!String(button.dataset.latestChapter || '').trim()) {
        window.fetchTrackerSourceChapter(form, button.dataset.sourceId, button.dataset.url || '');
    }
};

// The two title searches are text fields inside the tracker form, which has a
// Save button, so Enter in either was an implicit submission: the edit form
// saved as it stood and closed before any result appeared, and the new-tracker
// form complained about empty required fields instead of searching. The search
// already runs as the user types, so Enter in these boxes does nothing.
document.addEventListener('keydown', function (event) {
    if (!event || event.key !== 'Enter' || event.isComposing) {
        return;
    }
    var target = event.target;
    if (!target || !target.matches || !target.matches('#source-search-input, #linked-search-input')) {
        return;
    }
    event.preventDefault();
});
