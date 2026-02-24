window.escapeHtml = function (value) {
    return String(value || '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/\"/g, '&quot;')
        .replace(/'/g, '&#39;');
};

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

window.onProfileSwitch = function (select) {
    if (!select) {
        return;
    }

    var selectedProfile = (select.value || '').trim();
    if (!selectedProfile) {
        return;
    }

    var hiddenProfile = document.getElementById('profile-filter');
    if (hiddenProfile) {
        hiddenProfile.value = selectedProfile;
    }

    var renameForm = document.getElementById('profile-rename-form');
    if (renameForm) {
        renameForm.setAttribute('action', '/dashboard/profile/rename?profile=' + encodeURIComponent(selectedProfile));
    }

    if (window.history && window.history.replaceState) {
        var nextURL = '/dashboard?profile=' + encodeURIComponent(selectedProfile);
        window.history.replaceState({}, '', nextURL);
    }

    window.dispatchTrackersChanged('user');
};

window.renameProfileOnce = function () {
    var select = document.getElementById('profile-switch');
    var hiddenInput = document.getElementById('profile-rename-value');
    var form = document.getElementById('profile-rename-form');
    if (!select || !hiddenInput || !form) {
        return;
    }

    var currentName = select.options && select.selectedIndex >= 0
        ? select.options[select.selectedIndex].text
        : hiddenInput.value;

    var nextName = window.prompt('Rename profile', currentName || '');
    if (nextName === null) {
        return;
    }

    nextName = String(nextName).trim();
    if (!nextName) {
        return;
    }

    hiddenInput.value = nextName;

    var selectedProfile = (select.value || '').trim();
    if (selectedProfile) {
        form.setAttribute('action', '/dashboard/profile/rename?profile=' + encodeURIComponent(selectedProfile));
    }

    form.submit();
};
