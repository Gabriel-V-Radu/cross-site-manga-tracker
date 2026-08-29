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
    }
});

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
