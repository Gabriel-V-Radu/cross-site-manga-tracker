window.readTrackerTagsFromForm = function (form) {
    if (!form) {
        return [];
    }
    var rows = Array.prototype.slice.call(form.querySelectorAll('.tracker-tag-row'));
    return rows.map(function (row) {
        var nameInput = row.querySelector('input[name="tracker_tag_name"]');
        var iconInput = row.querySelector('input[name="tracker_tag_icon"]');
        var name = String((nameInput && nameInput.value) || '').trim();
        var iconKey = String((iconInput && iconInput.value) || '').trim();
        if (!name) {
            return null;
        }
        return {
            name: name,
            iconKey: iconKey || null
        };
    }).filter(function (item) { return !!item; });
};

window.getTagIconMeta = function (iconKey) {
    var key = String(iconKey || '').trim();
    if (key === 'icon_1') {
        return { key: key, label: 'Star', path: '/assets/tag-icons/icon-star-gold.svg' };
    }
    if (key === 'icon_2') {
        return { key: key, label: 'Heart', path: '/assets/tag-icons/icon-red-heart.svg' };
    }
    if (key === 'icon_3') {
        return { key: key, label: 'Flames', path: '/assets/tag-icons/icon-flames.svg' };
    }
    return { key: key, label: key || 'Icon', path: '' };
};

window.selectTrackerTagIcon = function (row, nextIcon) {
    if (!row) {
        return;
    }
    var form = row.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form');
    if (!form) {
        return;
    }

    var iconInput = row.querySelector('input[name="tracker_tag_icon"]');
    if (!iconInput) {
        return;
    }

    iconInput.value = String(nextIcon || '').trim();
    iconInput.dataset.selected = '';
    window.refreshTrackerTagRows(form);
};

window.collectTrackerTagPool = function (form) {
    if (!form) {
        return [];
    }

    var pool = [];
    var seen = {};

    var register = function (name) {
        var cleaned = String(name || '').trim();
        var normalized = cleaned.toLowerCase();
        if (!cleaned || seen[normalized]) {
            return;
        }
        seen[normalized] = true;
        pool.push(cleaned);
    };

    var profileTagsRaw = form.querySelector('#profile-tags-json');
    if (profileTagsRaw) {
        try {
            var profileTags = JSON.parse(profileTagsRaw.value || '[]');
            if (Array.isArray(profileTags)) {
                profileTags.forEach(function (item) {
                    register(item && item.name);
                });
            }
        } catch (_) { }
    }

    window.readTrackerTagsFromForm(form).forEach(function (item) {
        register(item.name);
    });

    return pool;
};

window.refreshTrackerTagRows = function (form) {
    if (!form) {
        return;
    }

    var rows = Array.prototype.slice.call(form.querySelectorAll('.tracker-tag-row'));
    var iconKeysRaw = form.querySelector('#tag-icon-keys-json');
    var iconKeys = [];
    try {
        iconKeys = JSON.parse((iconKeysRaw && iconKeysRaw.value) || '[]');
    } catch (_) {
        iconKeys = [];
    }
    if (!Array.isArray(iconKeys)) {
        iconKeys = [];
    }

    var tagPool = window.collectTrackerTagPool(form);

    rows.forEach(function (row) {
        var nameInput = row.querySelector('input[name="tracker_tag_name"]');
        var datalist = row.querySelector('datalist');
        if (datalist) {
            datalist.innerHTML = tagPool.map(function (name) {
                return '<option value="' + window.escapeHtml(name) + '"></option>';
            }).join('');
        }

        if (nameInput && !nameInput.dataset.bound) {
            nameInput.dataset.bound = '1';
            nameInput.addEventListener('input', function () {
                window.refreshTrackerTagRows(form);
            });
        }
    });

    var usedIcons = {};
    rows.forEach(function (row) {
        var iconInput = row.querySelector('input[name="tracker_tag_icon"]');
        var selected = String((iconInput && (iconInput.value || iconInput.dataset.selected)) || '').trim();
        if (selected) {
            usedIcons[selected] = true;
        }
    });

    rows.forEach(function (row) {
        var iconInput = row.querySelector('input[name="tracker_tag_icon"]');
        var picker = row.querySelector('.tracker-tag-icon-picker');
        if (!iconInput || !picker) {
            return;
        }

        var selected = String(iconInput.value || iconInput.dataset.selected || '').trim();
        var html = '<button type="button" class="tracker-tag-icon-btn' + (selected === '' ? ' tracker-tag-icon-btn--active' : '') + '" data-tag-icon="1" data-icon-key="" title="No icon">None</button>';

        iconKeys.forEach(function (iconKey) {
            var isUsedByAnother = usedIcons[iconKey] && selected !== iconKey;
            if (isUsedByAnother) {
                return;
            }
            var meta = window.getTagIconMeta(iconKey);
            var activeClass = selected === iconKey ? ' tracker-tag-icon-btn--active' : '';
            html += '' +
                '<button type="button" class="tracker-tag-icon-btn' + activeClass + '" data-tag-icon="1" data-icon-key="' + window.escapeHtml(iconKey) + '" title="' + window.escapeHtml(meta.label) + '">' +
                '<img src="' + window.escapeHtml(meta.path) + '" alt="' + window.escapeHtml(meta.label) + '">' +
                '</button>';
        });

        picker.innerHTML = html;
        iconInput.dataset.selected = '';

        var selectedStillAvailable = selected === '' || iconKeys.some(function (key) {
            return key === selected && (!usedIcons[key] || selected === key);
        });
        if (!selectedStillAvailable) {
            iconInput.value = '';
        }
    });

    var hidden = form.querySelector('#tracker-tags-json');
    if (hidden) {
        hidden.value = JSON.stringify(window.readTrackerTagsFromForm(form));
    }
};

window.addTrackerTagRow = function (button) {
    var form = button && (button.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form'));
    if (!form) {
        return;
    }

    var list = form.querySelector('#tracker-tags-list');
    if (!list) {
        return;
    }

    var rowId = 'tracker-tag-options-' + Date.now() + '-' + Math.floor(Math.random() * 1000);
    var row = document.createElement('div');
    row.className = 'tracker-tag-row';
    row.innerHTML = '' +
        '<label>' +
        'Tag name' +
        '<input type="text" name="tracker_tag_name" list="' + rowId + '" maxlength="40" placeholder="e.g. Favorite">' +
        '<datalist id="' + rowId + '"></datalist>' +
        '</label>' +
        '<label>' +
        'Icon' +
        '<div class="tracker-tag-icon-picker"></div>' +
        '<input type="hidden" name="tracker_tag_icon" value="">' +
        '</label>' +
        '<button type="button" class="linked-btn linked-btn--danger" onclick="window.removeTrackerTagRow(this)">Remove</button>';
    list.appendChild(row);
    window.refreshTrackerTagRows(form);
};

window.removeTrackerTagRow = function (button) {
    var row = button && button.closest('.tracker-tag-row');
    var form = button && (button.closest('.tracker-form') || document.querySelector('#modal-zone .tracker-form'));
    if (row) {
        row.remove();
    }
    if (form) {
        window.refreshTrackerTagRows(form);
    }
};

window.renderTrackerTagRows = function (form) {
    if (!form) {
        return;
    }

    var list = form.querySelector('#tracker-tags-list');
    var hidden = form.querySelector('#tracker-tags-json');
    if (!list || !hidden) {
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

    if (items.length === 0) {
        items = [{ name: '', iconKey: null }];
    }

    list.innerHTML = items.map(function (item, index) {
        var rowId = 'tracker-tag-options-static-' + index;
        return '' +
            '<div class="tracker-tag-row">' +
            '<label>Tag name<input type="text" name="tracker_tag_name" list="' + rowId + '" maxlength="40" placeholder="e.g. Favorite" value="' + window.escapeHtml(item && item.name || '') + '"><datalist id="' + rowId + '"></datalist></label>' +
            '<label>Icon<div class="tracker-tag-icon-picker"></div><input type="hidden" name="tracker_tag_icon" value="' + window.escapeHtml(item && item.iconKey || '') + '" data-selected="' + window.escapeHtml(item && item.iconKey || '') + '"></label>' +
            '<button type="button" class="linked-btn linked-btn--danger" onclick="window.removeTrackerTagRow(this)">Remove</button>' +
            '</div>';
    }).join('');

    window.refreshTrackerTagRows(form);
};

window.editProfileTagName = function (button) {
    if (!button) {
        return;
    }

    var form = button.closest('.profile-tag-rename-form');
    if (!form) {
        return;
    }

    var nameInput = form.querySelector('input[name="tag_name"]');
    if (!nameInput) {
        return;
    }

    var currentName = String(button.dataset.currentTagName || nameInput.value || '').trim();
    var nextName = window.prompt('Enter new tag name', currentName);
    if (nextName === null) {
        return;
    }

    nextName = String(nextName).trim();
    if (nextName === '') {
        window.alert('Tag name is required');
        return;
    }
    if (nextName.length > 40) {
        window.alert('Tag name must be 40 characters or less');
        return;
    }
    if (nextName.toLowerCase() === currentName.toLowerCase()) {
        return;
    }

    nameInput.value = nextName;
    form.requestSubmit();
};

document.body.addEventListener('htmx:afterSwap', function (event) {
    if (!event || !event.target || event.target.id !== 'modal-zone') {
        return;
    }
    var form = event.target.querySelector('.tracker-form');
    if (form) {
        window.renderLinkedSources(form);
        window.syncLinkedSourceSelect(form);
        window.renderTrackerTagRows(form);
    }
});

document.body.addEventListener('click', function (event) {
    var button = event.target && event.target.closest('[data-tag-icon="1"]');
    if (!button) {
        return;
    }

    var row = button.closest('.tracker-tag-row');
    if (!row) {
        return;
    }

    event.preventDefault();
    window.selectTrackerTagIcon(row, button.dataset.iconKey || '');
}, true);

document.body.addEventListener('click', function (event) {
    var button = event.target && event.target.closest('[data-menu-icon]');
    if (!button) {
        return;
    }

    var form = button.closest('form');
    if (!form) {
        return;
    }

    var hidden = form.querySelector('#menu-tag-icon-key');
    var picker = button.closest('.tracker-tag-icon-picker--menu');
    if (!hidden || !picker) {
        return;
    }

    event.preventDefault();
    hidden.value = String(button.dataset.menuIcon || '').trim();

    Array.prototype.forEach.call(picker.querySelectorAll('[data-menu-icon]'), function (candidate) {
        candidate.classList.remove('tracker-tag-icon-btn--active');
    });
    button.classList.add('tracker-tag-icon-btn--active');
}, true);
