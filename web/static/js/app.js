// Falco Dashboard - Alpine.js + HTMX Application

// Apply dark class immediately to prevent flash
(function() {
    if (localStorage.getItem('falco_theme') !== 'light') {
        document.documentElement.classList.add('dark');
    }
})();

function falcoApp() {
    return {
        showUpload: false,
        dark: localStorage.getItem('falco_theme') !== 'light',

        init() {
            // Sync dark class on html element
            this.$watch('dark', (val) => {
                document.documentElement.classList.toggle('dark', val);
                localStorage.setItem('falco_theme', val ? 'dark' : 'light');
            });

            // Inject API key into all HTMX requests
            document.body.addEventListener('htmx:configRequest', (e) => {
                const key = localStorage.getItem('falco_key') || '';
                if (key) {
                    e.detail.headers['X-API-Key'] = key;
                }
            });

            // Handle 401 responses - redirect to login
            document.body.addEventListener('htmx:responseError', (e) => {
                if (e.detail.xhr.status === 401) {
                    localStorage.removeItem('falco_key');
                    window.location.href = '/';
                }
            });
        },

        toggleTheme() {
            this.dark = !this.dark;
        },

        async logout() {
            localStorage.removeItem('falco_key');
            // La cookie es HttpOnly, solo el servidor puede expirarla.
            try {
                await fetch('/ui/logout', { method: 'POST', credentials: 'same-origin' });
            } catch (_) {
                // Ignorar — igualmente redirigimos al login.
            }
            window.location.href = '/';
        }
    };
}

function uploadForm() {
    return {
        files: [],
        uploading: false,
        progress: 0,
        dragover: false,
        uploadBucket: '',
        uploadPrefix: '',

        handleFileSelect(event) {
            this.files = Array.from(event.target.files);
        },

        handleDrop(event) {
            this.dragover = false;
            this.files = Array.from(event.dataTransfer.files).filter(f => f.type.startsWith('image/'));
        },

        async uploadFile() {
            if (!this.files.length) return;
            this.uploading = true;
            this.progress = 0;

            const key = localStorage.getItem('falco_key') || '';
            const total = this.files.length;
            let completed = 0;

            for (const file of this.files) {
                const formData = new FormData();
                formData.append('file', file);

                let url = '/api/v1/upload';
                const params = new URLSearchParams();
                if (this.uploadBucket) {
                    params.set('b', this.uploadBucket);
                }
                if (this.uploadPrefix) {
                    params.set('d', this.uploadPrefix);
                }
                if (params.toString()) {
                    url += '?' + params.toString();
                }

                try {
                    const resp = await fetch(url, {
                        method: 'POST',
                        headers: { 'X-API-Key': key },
                        body: formData
                    });

                    if (!resp.ok) {
                        const data = await resp.json().catch(() => ({}));
                        alert('Upload failed: ' + (data.error || resp.statusText));
                        break;
                    }
                } catch (err) {
                    alert('Upload error: ' + err.message);
                    break;
                }

                completed++;
                this.progress = Math.round((completed / total) * 100);
            }

            this.uploading = false;
            if (completed === total) {
                this.files = [];
                this.progress = 0;
                // Close modal and refresh
                this.showUpload = false;
                window.location.reload();
            }
        }
    };
}

// Lightbox functions (global)
function openLightbox(src, caption, size, date, format) {
    const lightbox = document.getElementById('lightbox');
    const lightboxImg = document.getElementById('lightbox-img');
    const lightboxCaption = document.getElementById('lightbox-caption');
    const lightboxMeta = document.getElementById('lightbox-meta');

    lightboxImg.src = src;
    lightboxCaption.textContent = caption;

    lightboxMeta.innerHTML = `
        <span class="flex items-center gap-1.5">
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" x2="12" y1="3" y2="15"/></svg>
            ${size}
        </span>
        <span class="flex items-center gap-1.5">
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="4" rx="2" ry="2"/><line x1="16" x2="16" y1="2" y2="6"/><line x1="8" x2="8" y1="2" y2="6"/><line x1="3" x2="21" y1="10" y2="10"/></svg>
            ${date}
        </span>
        <span class="px-2 py-0.5 rounded bg-violet-500/20 text-violet-300 text-[10px] font-bold uppercase tracking-widest border border-violet-500/30 ml-2">${format}</span>
    `;

    lightbox.classList.remove('hidden');
    lightbox.classList.add('flex');
    lightbox.classList.remove('pointer-events-none');

    void lightbox.offsetWidth;

    lightbox.classList.remove('opacity-0');
    lightbox.classList.add('opacity-100');
    lightboxImg.classList.remove('scale-95');
    lightboxImg.classList.add('scale-100');

    document.body.style.overflow = 'hidden';
}

function closeLightbox(e) {
    const lightbox = document.getElementById('lightbox');
    const lightboxImg = document.getElementById('lightbox-img');

    if (e && e.target === lightboxImg) return;

    lightbox.classList.remove('opacity-100');
    lightbox.classList.add('opacity-0');
    lightboxImg.classList.remove('scale-100');
    lightboxImg.classList.add('scale-95');
    lightbox.classList.add('pointer-events-none');

    setTimeout(() => {
        lightbox.classList.remove('flex');
        lightbox.classList.add('hidden');
        lightboxImg.src = '';
        document.body.style.overflow = '';
    }, 300);
}
