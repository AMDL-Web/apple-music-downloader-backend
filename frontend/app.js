const API_BASE = 'http://127.0.0.1:18080';

// Global State
let activeJobId = null;
let currentEventSource = null;
let pollInterval = null;

// DOM Elements
const connDot = document.getElementById('conn-dot');
const connText = document.getElementById('conn-text');
const capabilitiesPills = document.getElementById('capabilities-pills');
const downloadForm = document.getElementById('download-form');
const musicUrlInput = document.getElementById('music-url');
const forceDownloadCheckbox = document.getElementById('force-download');
const submitBtn = document.getElementById('submit-btn');
const refreshBtn = document.getElementById('refresh-btn');
const jobsListContainer = document.getElementById('jobs-list');

// Stat Elements
const statTotal = document.getElementById('stat-total');
const statActive = document.getElementById('stat-active');
const statCompleted = document.getElementById('stat-completed');
const statFailed = document.getElementById('stat-failed');

// Modal DOM Elements
const detailsModal = document.getElementById('details-modal');
const modalCloseBtn = document.getElementById('modal-close-btn');
const modalCloseFooterBtn = document.getElementById('modal-close-footer-btn');
const modalJobTitle = document.getElementById('modal-job-title');
const modalJobSub = document.getElementById('modal-job-sub');
const modalHeroCircle = document.getElementById('modal-hero-circle');
const modalHeroPercent = document.getElementById('modal-hero-percent');
const modalHeroId = document.getElementById('modal-hero-id');
const modalHeroUrl = document.getElementById('modal-hero-url');
const modalHeroTracks = document.getElementById('modal-hero-tracks');
const modalHeroStatus = document.getElementById('modal-hero-status');
const modalTrackCount = document.getElementById('modal-track-count');
const modalTrackList = document.getElementById('modal-track-list');
const modalCancelJobBtn = document.getElementById('modal-cancel-job-btn');

// Page Load Initialization
document.addEventListener('DOMContentLoaded', () => {
    checkConnection();
    fetchJobs();
    
    // Setup form submit
    downloadForm.addEventListener('submit', handleTaskSubmit);
    
    // Setup list refresh
    refreshBtn.addEventListener('click', () => {
        fetchJobs();
        // Visual indicator for refresh
        const svg = refreshBtn.querySelector('svg');
        svg.classList.add('spin-slow');
        setTimeout(() => svg.classList.remove('spin-slow'), 800);
    });

    // Setup modal close handlers
    const closeModal = () => {
        detailsModal.classList.remove('active');
        activeJobId = null;
        if (currentEventSource) {
            currentEventSource.close();
            currentEventSource = null;
            console.log('SSE connection closed.');
        }
        fetchJobs(); // Refresh main dashboard when closing modal
    };
    
    modalCloseBtn.addEventListener('click', closeModal);
    modalCloseFooterBtn.addEventListener('click', closeModal);
    document.querySelector('.modal-backdrop').addEventListener('click', closeModal);
    
    // Escape key closes modal
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && detailsModal.classList.contains('active')) {
            closeModal();
        }
    });

    // Start background polling for the main jobs dashboard
    pollInterval = setInterval(fetchJobs, 4000);
});

// Check API connection status and get capabilities
async function checkConnection() {
    try {
        connDot.className = 'status-dot status-checking';
        connText.textContent = '正在连接后端...';
        
        // Fetch health and capabilities
        const [healthRes, capRes] = await Promise.all([
            fetch(`${API_BASE}/api/v1/health`).catch(e => ({ ok: false })),
            fetch(`${API_BASE}/api/v1/capabilities`).catch(e => ({ ok: false }))
        ]);
        
        if (!healthRes.ok || !capRes.ok) {
            throw new Error('Connection failed');
        }
        
        const health = await healthRes.json();
        const cap = await capRes.json();
        
        // Update connection status
        connDot.className = 'status-dot status-online';
        connText.textContent = '服务在线';
        
        // Render Capabilities pills (e.g. tools availability)
        capabilitiesPills.innerHTML = '';
        if (Array.isArray(cap.tools)) {
            cap.tools.forEach((info) => {
                const pill = document.createElement('span');
                const toolName = info.name || '未知工具';
                if (info && info.available) {
                    pill.className = 'pill success';
                    pill.textContent = toolName;
                    pill.title = `可用路径: ${info.path || '系统路径'}`;
                } else {
                    pill.className = 'pill danger';
                    pill.textContent = `${toolName} 缺失`;
                    pill.title = info ? `错误: ${info.error}` : '未找到二进制工具';
                }
                capabilitiesPills.appendChild(pill);
            });
        } else if (cap.tools) {
            Object.entries(cap.tools).forEach(([tool, info]) => {
                const pill = document.createElement('span');
                if (info && info.available) {
                    pill.className = 'pill success';
                    pill.textContent = tool;
                    pill.title = `可用路径: ${info.path || '系统路径'}`;
                } else {
                    pill.className = 'pill danger';
                    pill.textContent = `${tool} 缺失`;
                    pill.title = info ? `错误: ${info.error}` : '未找到二进制工具';
                }
                capabilitiesPills.appendChild(pill);
            });
        }
        
        // Render target codec and fallback if available
        if (cap.codec) {
            const pill = document.createElement('span');
            pill.className = 'pill success';
            pill.style.background = 'rgba(255, 45, 85, 0.1)';
            pill.style.color = 'var(--accent-color)';
            pill.style.borderColor = 'rgba(255, 45, 85, 0.2)';
            pill.textContent = `目标音质: ${cap.codec.toUpperCase()}${cap.fallback_codec ? ` (降级: ${cap.fallback_codec.toUpperCase()})` : ''}`;
            pill.title = '音质格式目标与自动降级策略';
            capabilitiesPills.appendChild(pill);
        }

        // Render retry policy if available
        if (cap.retry_policy) {
            const pill = document.createElement('span');
            pill.className = 'pill success';
            pill.style.background = 'rgba(10, 132, 255, 0.15)';
            pill.style.color = '#0a84ff';
            pill.style.borderColor = 'rgba(10, 132, 255, 0.2)';
            
            let retryText = `重试限制: 下载 ${cap.retry_policy.operation_retries || 0}次`;
            if (cap.retry_policy.codec_retries) {
                retryText += ` / 解密 ${cap.retry_policy.codec_retries}次`;
            }
            pill.textContent = retryText;
            pill.title = '下载操作重试次数配置';
            capabilitiesPills.appendChild(pill);
        }
        
        // If Go wrapper grpc connection fails, show warning
        if (health.wrapper_error) {
            const errPill = document.createElement('span');
            errPill.className = 'pill danger';
            errPill.textContent = 'gRPC 断开';
            errPill.title = `wrapper gRPC 错误: ${health.wrapper_error}`;
            capabilitiesPills.appendChild(errPill);
            connText.textContent = '后端连通, 但 gRPC 异常';
        }
        
    } catch (error) {
        console.error('Backend connection error:', error);
        connDot.className = 'status-dot status-offline';
        connText.textContent = '后端服务离线';
        capabilitiesPills.innerHTML = `<span class="pill danger" title="${error.message}">连接错误</span>`;
    }
}

// Handle Task Submission
async function handleTaskSubmit(e) {
    e.preventDefault();
    
    const url = musicUrlInput.value.trim();
    const force = forceDownloadCheckbox.checked;
    
    if (!url) return;
    
    // Disable inputs during submission
    submitBtn.disabled = true;
    const originalBtnText = submitBtn.innerHTML;
    submitBtn.innerHTML = '<span>提交中...</span><div class="spinner" style="width:16px;height:16px;border-width:2px;margin:0;"></div>';
    
    try {
        const response = await fetch(`${API_BASE}/api/v1/downloads`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify({ url, force })
        });
        
        if (!response.ok) {
            const errData = await response.json();
            throw new Error(errData.error || '提交任务失败');
        }
        
        const job = await response.json();
        console.log('Submitted job:', job);
        
        // Reset form
        musicUrlInput.value = '';
        forceDownloadCheckbox.checked = false;
        
        // Fetch jobs immediately and show details for the newly created job
        await fetchJobs();
        openJobDetails(job.id);
        
    } catch (error) {
        console.error('Submit error:', error);
        alert(`创建任务失败: ${error.message}`);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerHTML = originalBtnText;
    }
}

// Fetch all jobs for dashboard
async function fetchJobs() {
    try {
        const response = await fetch(`${API_BASE}/api/v1/downloads?limit=100`);
        if (!response.ok) {
            throw new Error('Get downloads list failed');
        }
        
        const jobs = await response.json();
        
        // Sort jobs by created_at descending (latest first)
        const sortedJobs = (jobs || []).sort((a, b) => {
            return new Date(b.created_at) - new Date(a.created_at);
        });
        
        renderStats(sortedJobs);
        renderJobsList(sortedJobs);
        
    } catch (error) {
        console.error('Fetch jobs error:', error);
        // Display connection error in jobs list if empty
        if (jobsListContainer.querySelector('.loading-state')) {
            jobsListContainer.innerHTML = `
                <div class="empty-state">
                    <svg class="text-red" style="width:48px;height:48px" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"></path></svg>
                    <p>无法连接到后端 API 服务。</p>
                    <p style="font-size:0.8rem;margin-top:-5px">请确认后端在 127.0.0.1:18080 正常运行。</p>
                </div>
            `;
        }
    }
}

// Render counters in stats cards
function renderStats(jobs) {
    let total = jobs.length;
    let active = 0;
    let completed = 0;
    let failed = 0;
    
    jobs.forEach(job => {
        if (job.status === 'running' || job.status === 'queued') {
            active++;
        } else if (job.status === 'completed') {
            completed++;
        } else if (job.status === 'failed' || job.status === 'cancelled') {
            failed++;
        }
    });
    
    statTotal.textContent = total;
    statActive.textContent = active;
    statCompleted.textContent = completed;
    statFailed.textContent = failed;
}

// Render Job Card List
function renderJobsList(jobs) {
    if (jobs.length === 0) {
        jobsListContainer.innerHTML = `
            <div class="empty-state">
                <svg style="width:40px;height:40px" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20 13V6a2 2 0 00-2-2H6a2 2 0 00-2 2v7m16 0v5a2 2 0 01-2 2H6a2 2 0 01-2-2v-5m16 0h-2.586a1 1 0 00-.707.293l-2.414 2.414a1 1 0 01-.707.293h-3.172a1 1 0 01-.707-.293l-2.414-2.414A1 1 0 006.586 13H4"></path></svg>
                <p>暂无任务历史</p>
                <p style="font-size:0.8rem;margin-top:-5px">在上方输入 Apple Music 链接提交您的第一个任务</p>
            </div>
        `;
        return;
    }
    
    jobsListContainer.innerHTML = '';
    
    jobs.forEach(job => {
        const card = document.createElement('div');
        card.className = 'job-item-card';
        card.style.cursor = 'pointer';
        card.addEventListener('click', () => openJobDetails(job.id));
        
        // Calculate progress percentage
        let progressPercent = 0;
        if (job.total_items > 0) {
            progressPercent = Math.round(((job.done_items + job.failed_items) / job.total_items) * 100);
        } else if (job.status === 'completed') {
            progressPercent = 100;
        }
        
        // Format dates
        const date = new Date(job.created_at);
        const dateStr = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) + ' ' + date.toLocaleDateString([], { month: '2-digit', day: '2-digit' });
        
        // Title display logic
        let title = job.input;
        if (title.length > 55) {
            // Try to extract readable parts or just truncate
            try {
                const url = new URL(title);
                title = decodeURIComponent(url.pathname);
            } catch(e) {}
        }
        
        // Progress Fill Class based on status
        let progressFillClass = '';
        if (job.status === 'completed') progressFillClass = 'completed';
        if (job.status === 'failed' || job.status === 'cancelled') progressFillClass = 'failed';
        
        card.innerHTML = `
            <div class="job-info-col">
                <div class="job-title-row">
                    <span class="job-id-lbl">${job.id}</span>
                    <span class="job-title-txt" title="${job.input}">${title}</span>
                </div>
                <div class="job-meta-row">
                    <div class="job-meta-item">
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>
                        <span>${dateStr}</span>
                    </div>
                    <div class="job-meta-item">
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19V6l12-3v13M9 19c0 1.105-1.343 2-3 2s-3-.895-3-2 1.343-2 3-2 3 .895 3 2zm12-3c0 1.105-1.343 2-3 2s-3-.895-3-2 1.343-2 3-2 3 .895 3 2zM9 10l12-3"></path></svg>
                        <span>${job.done_items} / ${job.total_items} 已完成 ${job.failed_items > 0 ? `(${job.failed_items} 失败)` : ''}</span>
                    </div>
                    ${job.error ? `
                    <div class="job-meta-item text-red" title="${job.error}">
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>
                        <span class="text-truncate" style="max-width:200px">${job.error}</span>
                    </div>` : ''}
                </div>
            </div>
            <div class="job-action-col" onclick="event.stopPropagation()">
                <div class="job-mini-progress" title="${progressPercent}% 已处理">
                    <div class="job-mini-progress-fill ${progressFillClass}" style="width: ${progressPercent}%"></div>
                </div>
                <span class="badge ${job.status}">${job.status}</span>
                <button class="btn-secondary btn-icon-only" onclick="openJobDetails('${job.id}')" title="查看详情">
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"></path></svg>
                </button>
            </div>
        `;
        
        jobsListContainer.appendChild(card);
    });
}

// Open Details Modal and start real-time updates via SSE
async function openJobDetails(jobId) {
    activeJobId = jobId;
    
    // Set initial display
    modalJobTitle.textContent = `任务详情`;
    modalJobSub.textContent = `任务 ID: ${jobId}`;
    modalHeroId.textContent = jobId;
    modalHeroUrl.textContent = '获取中...';
    modalHeroTracks.textContent = '0';
    modalHeroStatus.innerHTML = '<span class="spinner" style="width:14px;height:14px;border-width:2px;display:inline-block;margin:0"></span>';
    modalTrackCount.textContent = '0';
    modalTrackList.innerHTML = '<div class="loading-state"><div class="spinner"></div><p>正在获取音轨列表...</p></div>';
    
    setCircularProgress(0);
    detailsModal.classList.add('active');
    
    // 1. Fetch initial full snapshot
    await refreshJobDetails(jobId);
    
    // 2. Establish SSE Connection
    if (currentEventSource) {
        currentEventSource.close();
    }
    
    console.log(`[SSE] Connecting for job: ${jobId}`);
    const es = new EventSource(`${API_BASE}/api/v1/downloads/${jobId}/events`);
    currentEventSource = es;

    // item_progress: payload contains the full JobItem — update in-place, NO HTTP
    es.addEventListener('item_progress', (e) => {
        if (activeJobId !== jobId) return;
        try {
            const ev = JSON.parse(e.data);
            // The event's payload field contains the serialized JobItem
            const item = typeof ev.payload === 'string' ? JSON.parse(ev.payload) : ev.payload;
            if (!item || !item.id) return;
            applyItemUpdate(item);
        } catch(err) {
            console.warn('[SSE] item_progress parse error:', err);
        }
    });

    // State-change events: do ONE full refresh to sync job-level data (totals, status)
    // Throttled to 600ms to absorb rapid bursts (e.g. album where many items finish at once)
    let throttleTimer = null;
    const throttledRefresh = () => {
        if (throttleTimer) return;
        throttleTimer = setTimeout(() => {
            throttleTimer = null;
            if (activeJobId === jobId) refreshJobDetails(jobId);
        }, 600);
    };

    const stateEvents = [
        'job_queued', 'job_started', 'job_cancelled', 'job_failed', 'job_finished',
        'resolved_input', 'item_failed', 'item_skipped', 'item_completed',
        'codec_selected', 'codec_failed',
        'operation_retry', 'operation_recovered', 'codec_fallback',
    ];
    stateEvents.forEach(eventType => {
        es.addEventListener(eventType, (e) => {
            console.log(`[SSE] ${eventType}:`, e.data);
            if (activeJobId === jobId) throttledRefresh();
        });
    });
    
    es.onerror = (err) => {
        console.warn('[SSE] connection error/closed:', err);
    };
}

// Apply a single JobItem update directly to the rendered track list
// without triggering any HTTP requests.
function applyItemUpdate(item) {
    // Find the existing track card by item id data attribute
    let card = modalTrackList.querySelector(`[data-item-id="${item.id}"]`);
    if (!card) return; // card not rendered yet — initial render will catch it

    const prog = Math.round((item.progress || 0) * 100);

    // Update progress bar
    const fill = card.querySelector('.track-progressbar-fill');
    const pctLabel = card.querySelector('.track-pct');
    const progressBlock = card.querySelector('.track-progress-block');

    const isActive = item.status !== 'completed' && item.status !== 'failed' &&
                     item.status !== 'queued' && item.status !== 'cancelled';

    if (isActive) {
        if (progressBlock) {
            progressBlock.style.display = '';
            if (fill) fill.style.width = prog + '%';
            if (pctLabel) pctLabel.textContent = prog + '%';
        }
    }

    // Update status badge
    const badge = card.querySelector('.track-right-status .badge, .track-right-status [class*="badge"]');
    if (badge) {
        badge.textContent = item.status;
        badge.className = `badge ${item.status === 'completed' ? 'completed' : item.status === 'failed' ? 'failed' : 'running'}`;
    }

    // Update status message
    let msgEl = card.querySelector('.track-status-msg');
    if (item.status_message && item.status !== 'completed') {
        if (!msgEl) {
            msgEl = document.createElement('div');
            msgEl.className = `track-status-msg ${item.status === 'failed' ? 'danger' : 'warning'}`;
            card.appendChild(msgEl);
        }
        const attemptHtml = item.attempt
            ? `<span class="attempt-badge">尝试 ${item.attempt}/${item.max_attempts || 3}</span>`
            : '';
        msgEl.innerHTML = `${attemptHtml}<span>${item.status_message}</span>`;
    } else if (msgEl) {
        msgEl.remove();
    }
}


// Refresh job details card and tracks list
async function refreshJobDetails(jobId) {
    try {
        const response = await fetch(`${API_BASE}/api/v1/downloads/${jobId}`);
        if (!response.ok) {
            throw new Error('Fetch job details failed');
        }
        
        const data = await response.json(); // {job: Job, items: []JobItem}
        const job = data.job;
        const items = data.items || [];
        
        if (activeJobId !== jobId) return; // Prevent async race conditions
        
        // Update job headers
        modalJobTitle.textContent = job.type ? `下载: ${job.type.toUpperCase()}` : '下载任务';
        modalHeroUrl.textContent = job.input;
        modalHeroUrl.title = job.input;
        modalHeroTracks.textContent = `${job.total_items} 首音轨`;
        modalHeroStatus.innerHTML = `<span class="badge ${job.status}">${job.status}</span>`;
        
        // Show/hide cancel button based on status
        if (job.status === 'running' || job.status === 'queued') {
            modalCancelJobBtn.style.display = 'block';
            modalCancelJobBtn.onclick = () => cancelJob(jobId);
        } else {
            modalCancelJobBtn.style.display = 'none';
        }
        
        // Progress Calculation
        let progressPercent = 0;
        if (job.total_items > 0) {
            progressPercent = Math.round(((job.done_items + job.failed_items) / job.total_items) * 100);
        } else if (job.status === 'completed') {
            progressPercent = 100;
        }
        
        // Update Circle Progress
        setCircularProgress(progressPercent);
        
        // Render track list
        modalTrackCount.textContent = items.length;
        renderTrackList(items);
        
        // If job completed/failed/cancelled, and SSE is running, close it
        if (['completed', 'failed', 'cancelled'].includes(job.status)) {
            if (currentEventSource) {
                console.log(`Job finished with status: ${job.status}. Closing SSE.`);
                currentEventSource.close();
                currentEventSource = null;
            }
        }
        
    } catch (error) {
        console.error('Refresh job details error:', error);
        modalTrackList.innerHTML = `<div class="empty-state text-red"><p>无法刷新音轨状态: ${error.message}</p></div>`;
    }
}

// Draw Track Items List
function renderTrackList(items) {
    if (items.length === 0) {
        modalTrackList.innerHTML = `
            <div class="empty-state">
                <p>暂无音轨信息 (可能解析中...)</p>
            </div>
        `;
        return;
    }
    
    modalTrackList.innerHTML = '';
    
    items.sort((a, b) => a.index - b.index).forEach(item => {
        const card = document.createElement('div');
        card.className = 'track-card';
        card.dataset.itemId = item.id; // ← key: lets applyItemUpdate find this card
        
        // Status Badge Style
        let badgeClass = 'badge queued';
        if (item.status === 'completed') badgeClass = 'badge completed';
        if (item.status === 'failed' || item.status === 'cancelled') badgeClass = 'badge failed';
        if (item.status === 'downloading' || item.status === 'decrypting' || item.status === 'remuxing' || item.status === 'tagging' || item.status === 'saving') {
            badgeClass = 'badge running';
        }
        
        // Progress rendering (Progress is float 0-1)
        let prog = Math.round((item.progress || 0) * 100);
        
        let progBarClass = '';
        if (item.status === 'completed') progBarClass = 'completed';
        if (item.status === 'failed') progBarClass = 'failed';
        
        const isActive = item.status !== 'completed' && item.status !== 'failed' &&
                         item.status !== 'queued' && item.status !== 'cancelled';
        
        card.innerHTML = `
            <div class="track-main-info">
                <div class="track-title-block">
                    <div class="track-title">#${item.index} ${item.title || '未知标题'}</div>
                    <div class="track-artist">${item.artist || '未知艺术家'} &bull; ${item.album || '未知专辑'}</div>
                </div>
                <div class="track-right-status">
                    ${item.codec ? `<span class="pill success" style="font-size:0.65rem">${item.codec}</span>` : ''}
                    <span class="${badgeClass}" style="padding:0.15rem 0.45rem;font-size:0.7rem">${item.status}</span>
                </div>
            </div>

            <!-- Progress block: always in DOM so SSE can update it in-place -->
            <div class="track-progress-block" style="display:${isActive ? '' : 'none'}">
                <div class="track-progressbar-bg">
                    <div class="track-progressbar-fill ${progBarClass}" style="width: ${prog}%"></div>
                </div>
                <span class="track-pct">${prog}%</span>
            </div>

            ${item.status === 'completed' && item.output_path ? `
            <div class="track-artist text-truncate" style="font-size:0.72rem;background:rgba(255,255,255,0.03);padding:0.2rem 0.4rem;border-radius:4px;width:100%" title="${item.output_path}">
                保存路径: ${item.output_path}
            </div>` : ''}

            ${(item.status_message && item.status !== 'completed') ? `
            <div class="track-status-msg ${item.status === 'failed' ? 'danger' : 'warning'}">
                ${item.attempt ? `<span class="attempt-badge">尝试 ${item.attempt}/${item.max_attempts || 3}</span>` : ''}
                <span>${item.status_message}</span>
            </div>` : ''}

            ${item.error && !item.status_message ? `<div class="track-error-msg">${item.error}</div>` : ''}
        `;
        
        modalTrackList.appendChild(card);
    });
}


// Cancel active Job
async function cancelJob(jobId) {
    if (!confirm('确定要取消此下载任务吗？')) {
        return;
    }
    
    modalCancelJobBtn.disabled = true;
    modalCancelJobBtn.textContent = '正在取消...';
    
    try {
        const response = await fetch(`${API_BASE}/api/v1/downloads/${jobId}/cancel`, {
            method: 'POST'
        });
        
        if (!response.ok) {
            throw new Error('Cancel request failed');
        }
        
        console.log(`Cancelled job: ${jobId}`);
        // Refresh details modal
        await refreshJobDetails(jobId);
        
    } catch (error) {
        console.error('Cancel error:', error);
        alert(`取消任务失败: ${error.message}`);
        modalCancelJobBtn.disabled = false;
        modalCancelJobBtn.textContent = '取消此下载任务';
    }
}

// Circular progress helper
function setCircularProgress(percent) {
    const radius = 15.9155;
    const circumference = 2 * Math.PI * radius; // 100
    
    // Cap percent between 0 and 100
    const safePercent = Math.max(0, Math.min(100, percent));
    
    modalHeroCircle.setAttribute('stroke-dasharray', `${safePercent}, 100`);
    modalHeroPercent.textContent = `${safePercent}%`;
}
