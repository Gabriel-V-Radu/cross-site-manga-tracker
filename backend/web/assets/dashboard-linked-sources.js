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

    var latestKnownField = form.querySelector('input[name="latest_known_chapter"]');
    var incomingLatestRaw = button.dataset.latestChapter;
    var incomingLatest = parseFloat(String(incomingLatestRaw || '').trim());
    var currentLatest = NaN;
    if (latestKnownField) {
        currentLatest = parseFloat(String(latestKnownField.value || '').trim());
    }

    var shouldPromoteAsPrimary = false;
    if (!Number.isNaN(incomingLatest)) {
        if (Number.isNaN(currentLatest) || incomingLatest > currentLatest) {
            shouldPromoteAsPrimary = true;
        }
    }

    if (shouldPromoteAsPrimary) {
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

        var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
        if (latestReleaseField && typeof button.dataset.latestReleaseAt !== 'undefined') {
            latestReleaseField.value = button.dataset.latestReleaseAt || '';
        }
    }

    window.renderLinkedSources(form);
    window.syncLinkedSourceSelect(form);
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
    setField('input[name="latest_known_chapter"]', button.dataset.latestChapter || '');

    var latestReleaseField = form.querySelector('input[name="latest_release_at"]');
    if (latestReleaseField) {
        latestReleaseField.value = button.dataset.latestReleaseAt || '';
    }
};
