// falco admin panel — Alpine + HTMX glue.
//
// Two rules run through all of this:
//
//   1. The API key never lives in the browser. Sign-in exchanges it for an
//      HttpOnly session cookie, and every request below rides on that cookie.
//      The previous panel kept the key in localStorage, which made the cookie's
//      HttpOnly flag meaningless.
//   2. Nothing reports success it did not get. A partial batch is shown per
//      item; an error is shown with the reason the server gave, never as an
//      alert() and never as silence.

function csrfToken() {
    const meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? meta.getAttribute('content') : '';
}

// post sends JSON with the CSRF token attached and always resolves to a
// {ok, status, data} shape, so callers never have to guess whether a rejection
// was a network failure or a refusal.
async function post(url, body) {
    try {
        const resp = await fetch(url, {
            method: 'POST',
            credentials: 'same-origin',
            headers: {
                'Content-Type': 'application/json',
                'X-CSRF-Token': csrfToken(),
            },
            body: body === undefined ? undefined : JSON.stringify(body),
        });
        const data = await resp.json().catch(() => ({}));
        return { ok: resp.ok, status: resp.status, data };
    } catch (err) {
        return { ok: false, status: 0, data: { error: err.message } };
    }
}

// errorText digs the server's own message out of a response body. falco answers
// {error: {code, message}} from the API and {error: "..."} from the panel.
function errorText(data, fallback) {
    if (!data) return fallback;
    if (typeof data.error === 'string') return data.error;
    if (data.error && data.error.message) return data.error.message;
    return fallback;
}

function falcoApp() {
    return {
        showUpload: false,
        dark: document.documentElement.classList.contains('dark'),
        selected: [],
        toasts: [],
        toastSeq: 0,

        init() {
            // HTMX requests are same-origin and carry the session cookie; the
            // CSRF token still goes along for anything that mutates.
            document.body.addEventListener('htmx:configRequest', (e) => {
                e.detail.headers['X-CSRF-Token'] = csrfToken();
            });

            // A dead session must send the browser back to the login instead of
            // swapping an error page into the content area.
            document.body.addEventListener('htmx:responseError', (e) => {
                if (e.detail.xhr.status === 401) {
                    window.location.href = '/';
                    return;
                }
                this.toast({ ok: false, title: 'Request failed', detail: e.detail.xhr.statusText || ('HTTP ' + e.detail.xhr.status) });
            });

            // Selections belong to the listing that was on screen; keeping them
            // across a navigation would let a later delete hit keys the user can
            // no longer see.
            document.body.addEventListener('htmx:afterSwap', () => this.clearSelection());
        },

        toast(t) {
            const id = ++this.toastSeq;
            this.toasts.push({ id, ok: !!t.ok, title: t.title, detail: t.detail || '', failed: t.failed || [] });
            // Failures stay until dismissed: the whole point is that they are
            // read, and a batch failure lists items worth looking at.
            if (t.ok) {
                setTimeout(() => this.dismissToast(id), 4000);
            }
        },

        dismissToast(id) {
            this.toasts = this.toasts.filter((t) => t.id !== id);
        },

        toggleTheme() {
            this.dark = !this.dark;
            document.documentElement.classList.toggle('dark', this.dark);
            // Persisted server-side so the next page renders correctly on the
            // first paint, with no flash and no inline script (which the
            // panel's CSP blocks).
            fetch('/ui/theme?theme=' + (this.dark ? 'dark' : 'light'), {
                method: 'POST',
                credentials: 'same-origin',
                headers: { 'X-CSRF-Token': csrfToken() },
            }).catch(() => {});
        },

        async logout() {
            // The cookie is HttpOnly and the session lives on the server, so
            // only the server can end it.
            await post('/ui/logout');
            window.location.href = '/';
        },

        // --- selection ---

        isSelected(key) {
            return this.selected.includes(key);
        },

        toggleSelection(key) {
            this.selected = this.isSelected(key)
                ? this.selected.filter((k) => k !== key)
                : [...this.selected, key];
        },

        clearSelection() {
            this.selected = [];
        },

        // --- deletion ---

        async deleteOne(bucket, key, redirectTo) {
            const name = key.split('/').pop();
            if (!window.confirm(`Delete ${name}? This cannot be undone.`)) return;
            await this.runDelete(bucket, [key], redirectTo);
        },

        async deleteSelected(bucket) {
            const keys = [...this.selected];
            if (!keys.length) return;
            if (!window.confirm(`Delete ${keys.length} object(s)? This cannot be undone.`)) return;
            await this.runDelete(bucket, keys);
        },

        async runDelete(bucket, keys, redirectTo) {
            const { ok, status, data } = await post('/ui/objects/delete', { bucket, keys });

            // 207 means the batch partly failed. Reporting that as success is
            // exactly the defect this panel was rebuilt to remove: the old one
            // sent a request that always 400'd and showed nothing at all.
            const deleted = (data && data.deleted) || [];
            const failed = (data && data.failed) || [];

            if (!ok && status !== 207) {
                this.toast({ ok: false, title: 'Delete failed', detail: errorText(data, 'HTTP ' + status) });
                return;
            }

            if (failed.length) {
                this.toast({
                    ok: false,
                    title: `Deleted ${deleted.length} of ${keys.length}`,
                    detail: 'These were not deleted:',
                    failed: failed.map((f) => (typeof f === 'string' ? { name: f, reason: 'refused' } : f)),
                });
            } else {
                this.toast({ ok: true, title: `Deleted ${deleted.length} object(s)` });
            }

            this.clearSelection();
            if (redirectTo) {
                window.location.href = redirectTo;
            } else {
                refreshExplorer();
            }
        },

        async purgeCache() {
            if (!window.confirm('Purge every cached variant? Transformations will be recomputed on demand.')) return;
            const { ok, status, data } = await post('/ui/cache/purge');
            if (!ok) {
                this.toast({ ok: false, title: 'Purge failed', detail: errorText(data, 'HTTP ' + status) });
                return;
            }
            // The count is the evidence that something happened. "Purged" with
            // no number cannot be told apart from a no-op.
            this.toast({ ok: true, title: `Purged ${data.purged} cache entr${data.purged === 1 ? 'y' : 'ies'}` });
            setTimeout(() => window.location.reload(), 600);
        },

        copy(text) {
            copyText(text).then((ok) => {
                this.toast(ok
                    ? { ok: true, title: 'Copied to clipboard' }
                    : { ok: false, title: 'Could not copy', detail: 'Select the text and copy it manually.' });
            });
        },

        // onUploadFinished receives the batch outcome from the upload form.
        //
        // A batch that partly failed is NOT a success: the modal stays open
        // with the per-file results visible, and the toast names what was not
        // stored.
        onUploadFinished(detail) {
            const { succeeded, failed } = detail;
            if (failed.length) {
                this.toast({
                    ok: false,
                    title: `Uploaded ${succeeded}, failed ${failed.length}`,
                    detail: 'These were not stored:',
                    failed,
                });
            } else {
                this.toast({ ok: true, title: `Uploaded ${succeeded} file(s)` });
                this.showUpload = false;
            }
            refreshExplorer();
        },
    };
}

// refreshExplorer re-fetches the current listing in place.
//
// A full page reload would throw away the HTMX navigation state — which is what
// the old upload flow did after every batch.
function refreshExplorer() {
    const explorer = document.getElementById('explorer');
    if (!explorer || !window.htmx) {
        window.location.reload();
        return;
    }
    const params = new URLSearchParams(window.location.search);
    const url = '/ui/explorer' + (params.toString() ? '?' + params.toString() : '');
    window.htmx.ajax('GET', url, { target: '#explorer', swap: 'outerHTML' });
}

async function copyText(text) {
    try {
        await navigator.clipboard.writeText(text);
        return true;
    } catch (_) {
        return false;
    }
}

function loginForm() {
    return {
        key: '',
        busy: false,
        error: '',

        async submit() {
            this.busy = true;
            this.error = '';
            const { ok, data } = await post('/ui/auth', { key: this.key });
            this.busy = false;
            if (!ok) {
                this.error = errorText(data, 'Sign-in failed');
                this.key = '';
                this.$refs.key.focus();
                return;
            }
            window.location.href = '/dashboard';
        },
    };
}

function uploadForm(bucket, prefix) {
    return {
        files: [],
        results: [],
        busy: false,
        progress: 0,
        dragover: false,
        bucket: bucket || '',
        prefix: prefix || '',

        onSelect(event) {
            this.files = Array.from(event.target.files);
            this.results = [];
        },

        onDrop(event) {
            this.dragover = false;
            this.files = Array.from(event.dataTransfer.files);
            this.results = [];
        },

        async upload() {
            if (!this.files.length) return;
            this.busy = true;
            this.progress = 0;
            this.results = [];

            const total = this.files.length;
            let done = 0;
            let failures = 0;

            // Every file is attempted and every outcome recorded. The old flow
            // stopped at the first failure and only closed the modal when the
            // whole batch succeeded, so a partial upload looked like nothing had
            // happened at all.
            for (const file of this.files) {
                const form = new FormData();
                form.append('file', file);

                const params = new URLSearchParams();
                if (this.bucket) params.set('bucket', this.bucket);
                if (this.prefix) params.set('prefix', this.prefix);

                try {
                    const resp = await fetch('/ui/objects/upload?' + params.toString(), {
                        method: 'POST',
                        credentials: 'same-origin',
                        headers: { 'X-CSRF-Token': csrfToken() },
                        body: form,
                    });
                    const data = await resp.json().catch(() => ({}));
                    if (resp.ok) {
                        this.results.push({ name: file.name, ok: true, message: (data.data && data.data.id) || 'stored' });
                    } else {
                        failures++;
                        this.results.push({ name: file.name, ok: false, message: errorText(data, 'HTTP ' + resp.status) });
                    }
                } catch (err) {
                    failures++;
                    this.results.push({ name: file.name, ok: false, message: err.message });
                }

                done++;
                this.progress = Math.round((done / total) * 100);
            }

            this.busy = false;
            this.files = [];

            // The toast list and the modal flag belong to the body's
            // falcoApp() scope, not to this form's. The old code assigned
            // `showUpload = false` from in here, which just created a new
            // property on the child scope and closed nothing — only the page
            // reload that followed hid the fact. Dispatching upward keeps each
            // piece of state owned by one scope.
            this.$dispatch('upload-finished', {
                succeeded: total - failures,
                failed: this.results.filter((r) => !r.ok).map((r) => ({ name: r.name, reason: r.message })),
            });
        },
    };
}

function signer(defaultTTL, requireExpiry) {
    return {
        path: '',
        ttl: defaultTTL,
        busy: false,
        error: '',
        result: '',
        expiryLabel: '',

        async sign() {
            this.busy = true;
            this.error = '';
            this.result = '';

            const body = { path: this.path };
            const ttl = parseInt(this.ttl, 10);
            if (ttl > 0) body.expires_in = ttl;

            const { ok, status, data } = await post('/ui/sign', body);
            this.busy = false;

            if (!ok) {
                this.error = errorText(data, 'Signing failed (HTTP ' + status + ')');
                return;
            }

            // Used verbatim: signing merges the expiry in and re-encodes the
            // query in sorted order, so rebuilding the URL breaks the signature.
            this.result = data.signed_url;
            this.expiryLabel = data.expires_at
                ? 'Expires ' + new Date(data.expires_at * 1000).toLocaleString()
                : (requireExpiry ? 'No expiry — delivery will refuse this URL' : 'Never expires');
        },

        copy(text) { copyText(text); },
    };
}

function playground(bucket, key) {
    return {
        bucket,
        key,
        params: {},
        previewURL: '',
        originalURL: '',
        signedURL: '',
        busy: false,
        error: '',
        result: { size: '', dimensions: '', contentType: '', fellBack: false },
        timer: null,

        init() {
            if (this.key) this.render();
        },

        reload() {
            const url = new URL(window.location.href);
            url.searchParams.set('bucket', this.bucket);
            url.searchParams.set('key', this.key);
            window.location.href = url.toString();
        },

        reset() {
            this.params = {};
            this.render();
        },

        // Debounced so dragging a number field does not queue a transform per
        // keystroke — each one is real libvips work.
        schedule() {
            clearTimeout(this.timer);
            this.timer = setTimeout(() => this.render(), 250);
        },

        buildQuery(includeTransforms) {
            const q = new URLSearchParams();
            q.set('b', this.bucket);
            if (!includeTransforms) return q;

            for (const [name, value] of Object.entries(this.params)) {
                if (value === '' || value === null || value === undefined || value === false) continue;
                q.set(name, value === true ? '1' : String(value));
            }
            return q;
        },

        async render() {
            if (!this.key) return;
            this.busy = true;
            this.error = '';

            const originalPath = '/api/v1/images/' + this.key + '?' + this.buildQuery(false).toString();
            const previewPath = '/api/v1/images/' + this.key + '?' + this.buildQuery(true).toString();

            const [orig, preview] = await Promise.all([
                post('/ui/sign', { path: originalPath, expires_in: 1800 }),
                post('/ui/sign', { path: previewPath, expires_in: 1800 }),
            ]);

            if (!orig.ok || !preview.ok) {
                this.busy = false;
                this.error = errorText(preview.data, 'Could not sign the preview URL');
                return;
            }

            this.originalURL = orig.data.signed_url;
            this.previewURL = preview.data.signed_url;
            this.signedURL = preview.data.signed_url;

            await this.measure(this.previewURL);
            this.busy = false;
        },

        // measure fetches the transformed image to report what the server
        // actually produced — the requested format is not the served one when
        // AVIF encoding fails and falls back to WebP.
        async measure(url) {
            try {
                const resp = await fetch(url, { credentials: 'same-origin' });
                if (!resp.ok) {
                    const data = await resp.json().catch(() => ({}));
                    this.error = errorText(data, 'Transformation failed (HTTP ' + resp.status + ')');
                    this.result = { size: '', dimensions: '', contentType: '', fellBack: false };
                    return;
                }

                const blob = await resp.blob();
                const contentType = resp.headers.get('Content-Type') || blob.type || '';
                const requested = this.params['f'];

                this.result = {
                    size: humanBytes(blob.size),
                    dimensions: '',
                    contentType,
                    fellBack: !!requested && !contentType.includes(requested),
                };

                const bitmap = await createImageBitmap(blob).catch(() => null);
                if (bitmap) {
                    this.result.dimensions = bitmap.width + ' × ' + bitmap.height;
                    bitmap.close();
                }
            } catch (err) {
                this.error = err.message;
            }
        },

        copy(text) { copyText(text); },
    };
}

function humanBytes(n) {
    if (n < 1024) return n + ' B';
    const units = ['KB', 'MB', 'GB'];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return v.toFixed(1) + ' ' + units[i];
}
