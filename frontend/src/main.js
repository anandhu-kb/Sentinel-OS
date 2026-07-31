import { 
    AddMonitorTarget, 
    EditMonitorTarget,
    RemoveMonitorTarget,
    GetMonitorStatuses, 
    GetTodayProductivityStats,
    GetProductivityHistory,
    AddWatcherRule,
    EditWatcherRule,
    RemoveWatcherRule,
    GetWatcherRules,
    CreateSnapshot, 
    GetSnapshotHistory,
    GetSnapshotDiff,
    AutoGenerateCommitMessage,
    AutoGenerateAIReview,
    UpdateSnapshot,
    DeleteSnapshot,
    RestoreSnapshot,
    GetUptimeLogs,
    GetUptimeStats,
    GetUniqueApps,
    GetUnmappedPrograms,
    GetDetailedLogs,
    GetProductivityHistoryByApp,
    GetSystemResources,
    StartPomodoro,
    CompletePomodoro,
    CancelPomodoro,
    GetPomodoroHistory,
    TriggerDistractionAlert,
    GetRunningApps,
    GetLogs,
    SelectDirectory
} from '../wailsjs/go/main/App.js';

// Wails runtime is available globally via window.runtime
// We can use it to listen to events securely.
const EventsOn = window.runtime.EventsOn;

document.addEventListener('DOMContentLoaded', () => {
    // ── Navigation ──
    const navItems = document.querySelectorAll('.nav-item');
    const views = document.querySelectorAll('.view');

    navItems.forEach(item => {
        item.addEventListener('click', () => {
            // Update active state
            navItems.forEach(nav => nav.classList.remove('active'));
            item.classList.add('active');

            // Show target view
            const targetId = item.getAttribute('data-target');
            views.forEach(view => {
                if (view.id === targetId) {
                    view.classList.add('active');
                    view.classList.remove('hidden');
                } else {
                    view.classList.remove('active');
                    view.classList.add('hidden');
                }
            });
            
            // Refresh view data when selected
            refreshCurrentView(targetId);
        });
    });

    // ── Clock ──
    setInterval(() => {
        document.getElementById('live-clock').innerText = new Date().toLocaleTimeString();
    }, 1000);

    // ── Modal Close ──
    document.getElementById('btn-close-diff').addEventListener('click', () => {
        const modal = document.getElementById('snapshot-diff-modal');
        modal.classList.add('hidden');
        modal.style.display = 'none';
    });

    // ── Snapshot Path Browse ──
    document.getElementById('btn-browse-snapshot-path').addEventListener('click', async () => {
        try {
            const dir = await SelectDirectory();
            if (dir) {
                document.getElementById('snapshot-path').value = dir;
                loadSnapshotsData(); // reload history for new path
            }
        } catch (err) {
            console.error("Failed to select directory:", err);
        }
    });

    // ── Initial Data Load ──
    refreshCurrentView('sentinel-view');

    // ── Register Wails Event Listeners ──
    setupEventListeners();
    
    // ── Setup form handlers ──
    setupFormHandlers();
    // ── Command Palette ──
    setupCommandPalette();
});

function refreshCurrentView(viewId) {
    if (viewId === 'sentinel-view') loadSentinelData();
    if (viewId === 'resources-view') startResourcePolling();
    if (viewId === 'watcher-view') {
        populateWatcherDropdown();
        populateUnmappedProgramsDropdown();
        loadWatcherRules();
        loadWatcherData();
        document.getElementById('tab-btn-timeline').click();
    }
    if (viewId === 'pomodoro-view') {
        loadPomodoroHistory();
        if (window.loadPomodoroRunningApps) window.loadPomodoroRunningApps();
    }
    if (viewId === 'snapshots-view') loadSnapshotsData();
    if (viewId === 'logs-view') startLogsPolling();
    else stopLogsPolling();
}

// ── Wails Event Listeners ──
function setupEventListeners() {
    EventsOn("sentinel:status_change", (data) => {
        // Refresh sentinel grid when a status changes (if view is active)
        if (!document.getElementById('sentinel-view').classList.contains('hidden')) {
            loadSentinelData();
        }
    });

    EventsOn("watcher:window_changed", (data) => {
        const titleEl = document.getElementById('active-window-title');
        const catEl = document.getElementById('active-window-category');
        
        // Truncate title if too long
        let title = data.title;
        if (title.length > 70) title = title.substring(0, 70) + '...';
        
        titleEl.innerText = title;
        catEl.innerText = data.category;
        
        // Pomodoro Distraction Blocker
        if (typeof pomodoroMode !== 'undefined' && pomodoroMode === 'focus' && typeof currentPomodoroId !== 'undefined' && currentPomodoroId !== null) {
            const mode = document.getElementById('pomodoro-blocker-mode').value;
            let isDistracted = false;

            if (mode === 'category') {
                const productiveCategories = ['IDE / Code', 'Browser / Docs', 'Terminal', 'Productivity'];
                if (!productiveCategories.includes(data.category)) isDistracted = true;
            } else if (mode === 'whitelist') {
                const opts = document.getElementById('pomodoro-whitelist').selectedOptions;
                const wl = Array.from(opts).map(opt => opt.value.toLowerCase());
                if (wl.length > 0) {
                    const match = wl.some(app => data.appName.toLowerCase() === app);
                    if (!match) isDistracted = true;
                }
            } else if (mode === 'blacklist') {
                const opts = document.getElementById('pomodoro-blacklist').selectedOptions;
                const bl = Array.from(opts).map(opt => opt.value.toLowerCase());
                if (bl.length > 0) {
                    const match = bl.some(app => data.appName.toLowerCase() === app);
                    if (match) isDistracted = true;
                }
            }

            if (isDistracted) {
                document.getElementById('distraction-app-name').innerText = data.appName + ' (' + data.category + ')';
                document.getElementById('distraction-modal').style.display = 'flex';
                // Trigger native Windows alert as well
                TriggerDistractionAlert().catch(e => console.error("Alert error:", e));
            }
        }
        
        // We also want to refresh the stats periodically or on change
        if (!document.getElementById('watcher-view').classList.contains('hidden')) {
            loadWatcherData();
        }
    });

    EventsOn("snapshot:created", (data) => {
        if (!document.getElementById('snapshots-view').classList.contains('hidden')) {
            loadSnapshotsData();
        }
    });
}

function setupFormHandlers() {
    // ── Sentinel Add/Edit Target ──
    document.getElementById('add-target-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const idStr = document.getElementById('target-id').value;
        const name = document.getElementById('target-name').value;
        const address = document.getElementById('target-address').value;
        const kind = document.getElementById('target-kind').value;
        
        let err;
        if (idStr) {
            err = await EditMonitorTarget(parseInt(idStr), name, address, kind);
        } else {
            err = await AddMonitorTarget(name, address, kind);
        }

        if (err) {
            alert("Error saving target: " + err);
        } else {
            resetSentinelForm();
            loadSentinelData();
        }
    });

    document.getElementById('btn-cancel-edit').addEventListener('click', () => {
        resetSentinelForm();
    });

    function resetSentinelForm() {
        document.getElementById('target-id').value = "";
        document.getElementById('target-name').value = "";
        document.getElementById('target-address').value = "";
        document.getElementById('btn-save-target').innerText = "Add Target";
        document.getElementById('btn-cancel-edit').style.display = "none";
    }

    // ── Watcher Add/Edit Rule ──
    document.getElementById('add-rule-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const idStr = document.getElementById('rule-id').value;
        const match = document.getElementById('rule-match').value;
        const name = document.getElementById('rule-name').value;
        const category = document.getElementById('rule-category').value;
        
        let err;
        if (idStr) {
            err = await EditWatcherRule(parseInt(idStr), match, name, category, false);
        } else {
            err = await AddWatcherRule(match, name, category, false);
        }

        if (err) {
            alert("Error saving rule: " + err);
        } else {
            resetRuleForm();
            populateUnmappedProgramsDropdown();
            loadWatcherRules();
            loadWatcherData();
        }
    });

    document.getElementById('btn-cancel-rule-edit').addEventListener('click', () => {
        resetRuleForm();
    });

    function resetRuleForm() {
        document.getElementById('rule-id').value = "";
        document.getElementById('rule-match').value = "";
        document.getElementById('rule-name').value = "";
        document.getElementById('rule-category').value = "";
        document.getElementById('btn-save-rule').innerText = "Track App";
        document.getElementById('btn-cancel-rule-edit').style.display = "none";
    }

    // ── Snapshots Create ──
    document.getElementById('btn-take-snapshot').addEventListener('click', async (e) => {
        const path = document.getElementById('snapshot-path').value;
        let msg = document.getElementById('snapshot-msg').value;
        if (!path) return alert("Please provide a project path.");
        
        try {
            const btn = e.currentTarget;
            btn.disabled = true;
            btn.innerText = "Taking...";

            // 1. Take snapshot (initially with empty or provided msg)
            const snap = await CreateSnapshot(path, msg);
            
            // 2. Auto-generate if msg was empty
            if (!msg) {
                const aiProvider = localStorage.getItem('ai_provider') || 'gemini';
                let aiConfig = '';
                if (aiProvider === 'gemini') {
                    aiConfig = localStorage.getItem('gemini_api_key');
                } else if (aiProvider === 'nvidia') {
                    const nvKey = localStorage.getItem('nvidia_api_key');
                    const nvMod = localStorage.getItem('nvidia_model');
                    aiConfig = nvKey ? `${nvKey}|${nvMod || 'deepseek-ai/deepseek-coder-33b-instruct'}` : '';
                } else {
                    aiConfig = localStorage.getItem('ollama_model') || 'llama3';
                }

                if ((aiProvider !== 'ollama' && !aiConfig) === false) {
                    // Let's get the previous ID to perform diff
                    const history = await GetSnapshotHistory(path, 2);
                    let prevId = 0;
                    if (history && history.length > 1) {
                        prevId = history[1].id;
                    }
                    await AutoGenerateCommitMessage(prevId, snap.id, aiProvider, aiConfig);
                } else {
                    alert("Snapshot taken, but no API key set for auto-summary.");
                }
            }
            
            document.getElementById('snapshot-msg').value = '';
            loadSnapshotsData();
            btn.disabled = false;
            btn.innerText = "Take Snapshot";
        } catch (err) {
            alert("Failed to create snapshot: " + err);
            document.getElementById('btn-take-snapshot').disabled = false;
            document.getElementById('btn-take-snapshot').innerText = "Take Snapshot";
        }
    });
    // ── Pomodoro Timer ──
    document.getElementById('btn-pomodoro-start').addEventListener('click', async () => {
        if (pomodoroInterval) return;
        
        const duration = parseInt(document.getElementById('pomodoro-duration-slider').value) || 25;
        const taskName = document.getElementById('pomodoro-task-name').value.trim();
        
        try {
            currentPomodoroId = await StartPomodoro(duration, taskName);
            pomodoroFocusDuration = duration;
            pomodoroMode = 'focus';
            pomodoroTimeLeft = duration * 60;
            
            document.getElementById('pomodoro-pre-controls').style.display = 'none';
            document.getElementById('pomodoro-running-controls').style.display = 'flex';
            document.getElementById('btn-pomodoro-cancel').innerText = '✕ Cancel';
            
            pomodoroInterval = setInterval(updatePomodoroTimer, 1000);
            updatePomodoroDisplay();
            loadPomodoroHistory();
        } catch (err) {
            alert("Failed to start pomodoro: " + err);
        }
    });

    document.getElementById('btn-pomodoro-cancel').addEventListener('click', async () => {
        if (!currentPomodoroId && pomodoroMode === 'break') {
            // Just cancel the break, no DB session to update
            resetPomodoroUI();
            return;
        }
        if (!currentPomodoroId) return;
        
        try {
            await CancelPomodoro(currentPomodoroId);
            resetPomodoroUI();
            loadPomodoroHistory();
        } catch (err) {
            alert("Failed to cancel: " + err);
        }
    });

    // --- Pomodoro Distraction UI ---
    document.getElementById('pomodoro-blocker-mode').addEventListener('change', (e) => {
        const mode = e.target.value;
        const wl = document.getElementById('pomodoro-whitelist-container');
        const bl = document.getElementById('pomodoro-blacklist-container');
        wl.style.display = mode === 'whitelist' ? 'block' : 'none';
        bl.style.display = mode === 'blacklist' ? 'block' : 'none';
    });

    document.getElementById('btn-dismiss-distraction').addEventListener('click', () => {
        document.getElementById('distraction-modal').style.display = 'none';
    });

    const apiKeyInput = document.getElementById('settings-gemini-key');
    const aiProviderSelect = document.getElementById('settings-ai-provider');
    const ollamaModelInput = document.getElementById('settings-ollama-model');
    const nvidiaKeyInput = document.getElementById('settings-nvidia-key');
    const nvidiaModelInput = document.getElementById('settings-nvidia-model');
    const geminiGroup = document.getElementById('settings-gemini-group');
    const ollamaGroup = document.getElementById('settings-ollama-group');
    const nvidiaGroup = document.getElementById('settings-nvidia-group');

    if (localStorage.getItem('gemini_api_key')) {
        apiKeyInput.value = localStorage.getItem('gemini_api_key');
    }
    if (localStorage.getItem('ai_provider')) {
        aiProviderSelect.value = localStorage.getItem('ai_provider');
    }
    if (localStorage.getItem('ollama_model')) {
        ollamaModelInput.value = localStorage.getItem('ollama_model');
    }
    if (localStorage.getItem('nvidia_api_key')) {
        nvidiaKeyInput.value = localStorage.getItem('nvidia_api_key');
    }
    if (localStorage.getItem('nvidia_model')) {
        nvidiaModelInput.value = localStorage.getItem('nvidia_model');
    }

    const updateProviderUI = () => {
        geminiGroup.style.display = 'none';
        ollamaGroup.style.display = 'none';
        nvidiaGroup.style.display = 'none';

        if (aiProviderSelect.value === 'ollama') {
            ollamaGroup.style.display = 'block';
        } else if (aiProviderSelect.value === 'nvidia') {
            nvidiaGroup.style.display = 'block';
        } else {
            geminiGroup.style.display = 'block';
        }
    };
    aiProviderSelect.addEventListener('change', updateProviderUI);
    updateProviderUI();

    document.getElementById('btn-save-settings').addEventListener('click', () => {
        localStorage.setItem('gemini_api_key', apiKeyInput.value.trim());
        localStorage.setItem('ai_provider', aiProviderSelect.value);
        localStorage.setItem('ollama_model', ollamaModelInput.value.trim());
        localStorage.setItem('nvidia_api_key', nvidiaKeyInput.value.trim());
        localStorage.setItem('nvidia_model', nvidiaModelInput.value.trim());
        alert('Settings saved!');
    });

    document.getElementById('btn-diff-ai-review').addEventListener('click', async (e) => {
        const aiProvider = localStorage.getItem('ai_provider') || 'gemini';
        let aiConfig = '';
        if (aiProvider === 'gemini') {
            aiConfig = localStorage.getItem('gemini_api_key');
            if (!aiConfig) {
                alert("Please save your Gemini API key in the Settings tab first.");
                return;
            }
        } else if (aiProvider === 'nvidia') {
            const nvKey = localStorage.getItem('nvidia_api_key');
            const nvMod = localStorage.getItem('nvidia_model');
            if (!nvKey) {
                alert("Please save your Nvidia API key in the Settings tab first.");
                return;
            }
            aiConfig = `${nvKey}|${nvMod || 'deepseek-ai/deepseek-coder-33b-instruct'}`;
        } else {
            aiConfig = localStorage.getItem('ollama_model') || 'llama3';
        }

        if (!window._currentDiffIds) return;

        const { prevId, id } = window._currentDiffIds;
        const resultBox = document.getElementById('snapshot-diff-ai-result');
        const originalHTML = e.currentTarget.innerHTML;
        
        try {
            e.currentTarget.disabled = true;
            e.currentTarget.innerHTML = "⏳ Generating AI Review...";
            resultBox.innerHTML = "<p>Analyzing changes...</p>";
            resultBox.style.display = "block";
            
            const htmlResult = await AutoGenerateAIReview(prevId, id, aiProvider, aiConfig);
            resultBox.innerHTML = htmlResult;
        } catch(err) {
            resultBox.innerHTML = `<p style="color:var(--danger-color);">Error: ${err}</p>`;
        } finally {
            e.currentTarget.disabled = false;
            e.currentTarget.innerHTML = originalHTML;
        }
    });
}

// ── Pomodoro Logic ──
let pomodoroInterval = null;
let pomodoroTimeLeft = 25 * 60;
let pomodoroFocusDuration = 25;
let currentPomodoroId = null;
let pomodoroMode = 'focus'; // 'focus' | 'break'

function playPomodoroChime() {
    try {
        const audio = document.getElementById('pomodoro-audio-tone');
        if (audio) {
            audio.currentTime = 0;
            audio.play().catch(e => console.log("Audio play prevented:", e));
        }
    } catch(e) {}
}

function updatePomodoroDisplay() {
    const minutes = Math.floor(pomodoroTimeLeft / 60);
    const seconds = pomodoroTimeLeft % 60;
    document.getElementById('pomodoro-display').innerText =
        `${minutes.toString().padStart(2, '0')}:${seconds.toString().padStart(2, '0')}`;
    
    const modeLabel = document.getElementById('pomodoro-mode-label');
    const displayEl = document.getElementById('pomodoro-display');
    if (pomodoroMode === 'break') {
        displayEl.style.color = 'var(--accent-success)';
        modeLabel.innerText = '☕ BREAK TIME';
        modeLabel.style.color = 'var(--accent-success)';
    } else {
        displayEl.style.color = 'var(--accent-primary)';
        modeLabel.innerText = '🎯 FOCUS SESSION';
        modeLabel.style.color = 'var(--text-secondary)';
    }
}

function updatePomodoroTimer() {
    pomodoroTimeLeft--;
    updatePomodoroDisplay();
    
    if (pomodoroTimeLeft <= 0) {
        clearInterval(pomodoroInterval);
        pomodoroInterval = null;
        playPomodoroChime();
        
        if (pomodoroMode === 'focus') {
            CompletePomodoro(currentPomodoroId).then(() => {
                currentPomodoroId = null;
                loadPomodoroHistory();
                // Auto-start 5-min break
                pomodoroMode = 'break';
                pomodoroTimeLeft = 5 * 60;
                document.getElementById('btn-pomodoro-cancel').innerText = '⏭ Skip Break';
                pomodoroInterval = setInterval(updatePomodoroTimer, 1000);
                updatePomodoroDisplay();
            }).catch(err => { console.error("CompletePomodoro error:", err); });
        } else {
            // Break over — auto-start new focus
            const taskName = document.getElementById('pomodoro-task-name').value.trim();
            StartPomodoro(pomodoroFocusDuration, taskName).then(id => {
                currentPomodoroId = id;
                pomodoroMode = 'focus';
                pomodoroTimeLeft = pomodoroFocusDuration * 60;
                document.getElementById('btn-pomodoro-cancel').innerText = '✕ Cancel';
                pomodoroInterval = setInterval(updatePomodoroTimer, 1000);
                updatePomodoroDisplay();
                loadPomodoroHistory();
            }).catch(err => { console.error("StartPomodoro error:", err); });
        }
    }
}

function resetPomodoroUI() {
    clearInterval(pomodoroInterval);
    pomodoroInterval = null;
    currentPomodoroId = null;
    pomodoroMode = 'focus';
    
    const slider = document.getElementById('pomodoro-duration-slider');
    pomodoroFocusDuration = parseInt(slider?.value || 25);
    pomodoroTimeLeft = pomodoroFocusDuration * 60;
    
    const displayEl = document.getElementById('pomodoro-display');
    const modeLabel = document.getElementById('pomodoro-mode-label');
    const mins = pomodoroFocusDuration.toString().padStart(2, '0');
    displayEl.innerText = `${mins}:00`;
    displayEl.style.color = 'var(--accent-primary)';
    modeLabel.innerText = '🎯 FOCUS SESSION';
    modeLabel.style.color = 'var(--text-secondary)';
    
    document.getElementById('pomodoro-pre-controls').style.display = 'flex';
    document.getElementById('pomodoro-running-controls').style.display = 'none';
}

async function loadPomodoroHistory() {
    try {
        const history = await GetPomodoroHistory();
        const tbody = document.getElementById('pomodoro-history-table');
        if (!tbody) return;
        
        tbody.innerHTML = '';
        if (!history || history.length === 0) {
            tbody.innerHTML = '<tr><td colspan="4" style="text-align: center; color: var(--text-secondary);">No sessions recorded yet.</td></tr>';
            return;
        }

        history.forEach(s => {
            // Clean up stale sessions
            if (s.status === 'running' && s.id !== currentPomodoroId) {
                CancelPomodoro(s.id).then(() => loadPomodoroHistory());
                return;
            }

            const tr = document.createElement('tr');
            let statusColor = 'var(--text-secondary)';
            if (s.status === 'completed') statusColor = 'var(--accent-success)';
            if (s.status === 'cancelled') statusColor = 'var(--accent-danger)';
            if (s.status === 'running') statusColor = 'var(--accent-primary)';

            tr.innerHTML = `
                <td><strong>${s.taskName || '—'}</strong></td>
                <td>${new Date(s.startTime).toLocaleString()}</td>
                <td>${s.durationMinutes} min</td>
                <td style="color: ${statusColor}; font-weight: bold;">${s.status.toUpperCase()}</td>
            `;
            tbody.appendChild(tr);
        });
    } catch (e) {
        console.error("Failed to load pomodoro history", e);
    }
}

// ── Module Data Loaders ──

async function loadSentinelData() {
    try {
        const statuses = await GetMonitorStatuses();
        const stats = await GetUptimeStats() || {};
        const grid = document.getElementById('sentinel-grid');
        grid.innerHTML = '';
        
        if (!statuses || statuses.length === 0) {
            grid.innerHTML = '<div class="card"><p style="color: var(--text-secondary)">No monitoring targets configured.</p></div>';
            return;
        }

        statuses.forEach(s => {
            const isUp = s.isUp;
            const statusClass = isUp ? 'up' : 'down';
            const statusText = isUp ? 'UP' : 'DOWN';
            const latencyStr = isUp ? `${s.latencyMs}ms` : '-';
            const upPct = stats[s.address] || 0;
            const downPct = 100 - upPct;
            
            const card = document.createElement('div');
            card.className = 'card';
            card.style.cursor = 'pointer';
            card.innerHTML = `
                <div class="card-header" style="display: flex; justify-content: space-between; align-items: center;">
                    <span class="target-kind">${s.kind}</span>
                    <span class="status-badge ${statusClass}">${statusText}</span>
                </div>
                <div style="display:flex; align-items:center; margin-top: 15px; gap: 15px;">
                    <div title="Reliability: ${upPct.toFixed(1)}%" style="min-width: 50px; height: 50px; border-radius: 50%; background: conic-gradient(var(--up-color) ${upPct}%, var(--down-color) ${upPct}% 100%);"></div>
                    <div>
                        <div class="target-name" style="font-weight:bold; color:var(--text-primary); font-size:1.1rem;">${s.name}</div>
                        <div class="target-address" style="font-size: 0.85rem; color:var(--text-secondary)">${s.address}</div>
                    </div>
                </div>
                <div style="color: var(--text-secondary); font-size: 0.85rem; margin-top: 15px;">
                    Latency: ${latencyStr} <br>
                    Last checked: ${new Date(s.checkedAt).toLocaleTimeString()}
                </div>
                <div style="display:flex; gap: 8px; margin-top: 10px;">
                    <button class="btn warning btn-edit-target" data-id="${s.id}" data-name="${s.name}" data-address="${s.address}" data-kind="${s.kind}" style="padding: 4px 8px; font-size: 0.8rem;">Edit</button>
                    <button class="btn danger btn-delete-target" data-id="${s.id}" style="padding: 4px 8px; font-size: 0.8rem;">Delete</button>
                </div>
            `;
            
            // Clicking the card loads its history (excluding clicks on edit/delete buttons)
            card.addEventListener('click', (e) => {
                if(e.target.tagName !== 'BUTTON') {
                    loadUptimeHistory(s.address, s.name);
                }
            });
            
            grid.appendChild(card);
        });

        // Add event listeners for edit and delete
        document.querySelectorAll('.btn-edit-target').forEach(btn => {
            btn.addEventListener('click', (e) => {
                const b = e.target;
                document.getElementById('target-id').value = b.getAttribute('data-id');
                document.getElementById('target-name').value = b.getAttribute('data-name');
                document.getElementById('target-address').value = b.getAttribute('data-address');
                document.getElementById('target-kind').value = b.getAttribute('data-kind');
                document.getElementById('btn-save-target').innerText = "Save Changes";
                document.getElementById('btn-cancel-edit').style.display = "inline-block";
                document.getElementById('target-name').focus();
            });
        });

        document.querySelectorAll('.btn-delete-target').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                if (confirm("Delete this monitoring target?")) {
                    const id = parseInt(e.target.getAttribute('data-id'));
                    try {
                        await RemoveMonitorTarget(id);
                        loadSentinelData();
                    } catch(err) {
                        alert("Error deleting: " + err);
                    }
                }
            });
        });
    } catch (e) {
        console.error("Failed to load sentinel data", e);
    }
}

let resourcePollInterval = null;
let lastNetStats = { up: 0, down: 0, time: 0 };

function formatBytes(bytes, decimals = 1) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
}

function startResourcePolling() {
    if (resourcePollInterval) clearInterval(resourcePollInterval);
    resourcePollInterval = setInterval(pollResources, 1000);
    pollResources();
}

async function pollResources() {
    if (document.getElementById('resources-view').classList.contains('hidden')) {
        clearInterval(resourcePollInterval);
        resourcePollInterval = null;
        lastNetStats = { up: 0, down: 0, time: 0 };
        return;
    }
    try {
        const stats = await GetSystemResources();
        
        // Host Info
        const uptimeDays = Math.floor(stats.hostUptime / 86400);
        const uptimeHrs = Math.floor((stats.hostUptime % 86400) / 3600);
        const bootDate = new Date(stats.hostBootTime * 1000).toLocaleString();
        document.getElementById('sys-host-info').innerText = `${stats.hostOs}  •  Uptime: ${uptimeDays}d ${uptimeHrs}h  •  Booted: ${bootDate}`;

        // SVG Ring calculations (Dash array is 314)
        const calcOffset = (percent) => Math.max(0, 314 - (percent / 100) * 314);

        document.getElementById('sys-cpu-text').innerText = stats.cpuPercent.toFixed(1) + '%';
        document.getElementById('sys-cpu-ring').style.strokeDashoffset = calcOffset(stats.cpuPercent);
        
        document.getElementById('sys-ram-text').innerText = stats.ramPercent.toFixed(1) + '%';
        document.getElementById('sys-ram-ring').style.strokeDashoffset = calcOffset(stats.ramPercent);
        
        document.getElementById('sys-disk-text').innerText = stats.diskPercent.toFixed(1) + '%';
        document.getElementById('sys-disk-ring').style.strokeDashoffset = calcOffset(stats.diskPercent);
        
        const now = Date.now();
        if (lastNetStats.time > 0) {
            const dt = (now - lastNetStats.time) / 1000;
            const upSpeed = (stats.netUpload - lastNetStats.up) / dt;
            const downSpeed = (stats.netDownload - lastNetStats.down) / dt;
            document.getElementById('sys-net-up').innerText = formatBytes(Math.max(0, upSpeed)) + '/s';
            document.getElementById('sys-net-down').innerText = formatBytes(Math.max(0, downSpeed)) + '/s';
        }
        lastNetStats = { up: stats.netUpload, down: stats.netDownload, time: now };
        
        const tbody = document.getElementById('sys-top-processes');
        tbody.innerHTML = '';
        if (stats.topProcesses && stats.topProcesses.length > 0) {
            stats.topProcesses.forEach((p, idx) => {
                const isHighCpu = p.cpu > 20;
                const isHighMem = p.memory > 20;
                const cpuColor = isHighCpu ? 'var(--accent-danger)' : 'var(--accent-primary)';
                const memColor = isHighMem ? 'var(--accent-danger)' : 'var(--accent-purple)';
                
                tbody.innerHTML += `
                <div style="display: flex; justify-content: space-between; align-items: center; padding: 12px; background: rgba(0,0,0,0.2); border-radius: 8px; border-left: 3px solid ${isHighCpu || isHighMem ? 'var(--accent-danger)' : 'rgba(255,255,255,0.1)'};">
                    <div style="display: flex; align-items: center; gap: 12px;">
                        <span style="color: var(--text-secondary); font-weight: bold; width: 20px;">#${idx+1}</span>
                        <span style="font-weight: 500; font-size: 1.05rem;">${p.name}</span>
                    </div>
                    <div style="display: flex; gap: 20px; font-size: 0.95rem;">
                        <div style="width: 80px; text-align: right;">
                            <span style="color: var(--text-secondary); font-size: 0.8rem; margin-right: 5px;">CPU</span>
                            <span style="color: ${cpuColor}; font-weight: bold;">${p.cpu.toFixed(1)}%</span>
                        </div>
                        <div style="width: 80px; text-align: right;">
                            <span style="color: var(--text-secondary); font-size: 0.8rem; margin-right: 5px;">MEM</span>
                            <span style="color: ${memColor}; font-weight: bold;">${p.memory.toFixed(1)}%</span>
                        </div>
                    </div>
                </div>`;
            });
        } else {
            tbody.innerHTML = '<div style="text-align: center; color: var(--text-secondary); padding: 20px;">No processes found or access denied.</div>';
        }
    } catch(e) {
        console.error("Resource poll error:", e);
    }
}

document.getElementById('btn-close-uptime-history').addEventListener('click', () => {
    document.getElementById('uptime-history-container').style.display = 'none';
});

async function loadUptimeHistory(targetAddress, targetName) {
    try {
        const logs = await GetUptimeLogs(targetAddress, 50); // Get last 50 logs
        const container = document.getElementById('uptime-history-container');
        const title = document.getElementById('uptime-history-title');
        const chartDiv = document.getElementById('uptime-chart');
        const tableBody = document.getElementById('uptime-table-body');
        
        container.style.display = 'block';
        title.innerText = `History: ${targetName} (${targetAddress})`;
        
        if (!logs || logs.length === 0) {
            chartDiv.innerHTML = '<p style="color: var(--text-secondary)">No history recorded yet.</p>';
            tableBody.innerHTML = '';
            return;
        }

        // Draw Pie Chart & Basic HTML chart
        const upCount = logs.filter(l => l.status === "UP").length;
        const downCount = logs.filter(l => l.status === "DOWN").length;
        const upPct = (upCount / logs.length) * 100;
        
        let headerHTML = `
            <div style="display:flex; justify-content:center; align-items:center; gap: 40px; margin-bottom: 20px;">
                <div style="display:flex; flex-direction:column; align-items:center;">
                    <div style="width: 80px; height: 80px; border-radius: 50%; background: conic-gradient(var(--up-color) ${upPct}%, var(--down-color) ${upPct}% 100%); margin-bottom: 10px; box-shadow: 0 4px 10px rgba(0,0,0,0.3);"></div>
                    <strong style="color:var(--text-primary); font-size:1.1rem;">${upPct.toFixed(1)}% Uptime</strong>
                </div>
                <div style="font-size:1.1rem; line-height: 1.6;">
                    <div><span style="display:inline-block; width:12px; height:12px; background:var(--up-color); border-radius:50%; margin-right:8px;"></span> <strong>${upCount}</strong> UP</div>
                    <div><span style="display:inline-block; width:12px; height:12px; background:var(--down-color); border-radius:50%; margin-right:8px;"></span> <strong>${downCount}</strong> DOWN</div>
                    <div style="font-size:0.85rem; color:var(--text-secondary); margin-top:5px;">(Based on last ${logs.length} checks)</div>
                </div>
            </div>
        `;
        
        let chartHTML = `<div style="display: flex; gap: 2px; align-items: flex-end; height: 100px; padding: 10px 0; border-bottom: 1px solid var(--border-color);">`;
        // We want oldest to newest for chart, so reverse logs since they are DESC
        const chartLogs = [...logs].reverse();
        const maxLatency = Math.max(...chartLogs.map(l => l.latencyMs), 10); // avoid div by 0
        
        chartLogs.forEach(log => {
            const heightPct = log.status === "UP" ? (log.latencyMs / maxLatency) * 100 : 100;
            const color = log.status === "UP" ? "var(--up-color)" : "var(--down-color)";
            const titleStr = `${new Date(log.checkedAt).toLocaleTimeString()}: ${log.status === "UP" ? log.latencyMs+'ms' : 'DOWN'}`;
            chartHTML += `<div style="flex: 1; min-width: 5px; height: ${heightPct}%; background-color: ${color}; opacity: 0.8;" title="${titleStr}"></div>`;
        });
        chartHTML += `</div><div style="font-size: 0.8rem; color: var(--text-secondary); text-align: center; margin-top: 5px;">Latency Trend (Last 50 checks)</div>`;
        chartDiv.innerHTML = headerHTML + chartHTML;

        // Populate Table
        tableBody.innerHTML = '';
        logs.forEach(log => {
            const tr = document.createElement('tr');
            tr.innerHTML = `
                <td><span style="color: ${log.status === "UP" ? 'var(--accent-color)' : 'var(--error-color)'}">${log.status}</span></td>
                <td>${log.status === "UP" ? log.latencyMs + ' ms' : '-'}</td>
                <td>${new Date(log.checkedAt).toLocaleString()}</td>
            `;
            tableBody.appendChild(tr);
        });
        
    } catch (e) {
        console.error("Failed to load uptime history", e);
    }
}

async function loadWatcherRules() {
    try {
        const rules = await GetWatcherRules();
        const grid = document.getElementById('rules-grid');
        grid.innerHTML = '';
        
        if (!rules || rules.length === 0) {
            grid.innerHTML = '<p style="color: var(--text-secondary)">No rules configured yet.</p>';
            return;
        }

        rules.forEach(r => {
            const card = document.createElement('div');
            card.className = 'card';
            card.innerHTML = `
                <div style="font-weight: bold; font-size: 1.1rem; color: var(--accent-color); margin-bottom: 5px;">${r.Name}</div>
                <div style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 10px;">Category: ${r.Category}</div>
                <div style="font-size: 0.85rem; word-break: break-all; margin-bottom: 15px;">Match: <code>${r.MatchString}</code></div>
                <div style="display: flex; gap: 10px;">
                    <button class="btn secondary btn-edit-rule" data-id="${r.ID}" data-match="${r.MatchString}" data-name="${r.Name}" data-category="${r.Category}" style="flex:1; padding: 5px;">Edit</button>
                    <button class="btn warning btn-delete-rule" data-id="${r.ID}" style="flex:1; padding: 5px;">Delete</button>
                </div>
            `;
            grid.appendChild(card);
        });

        document.querySelectorAll('.btn-delete-rule').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                if(confirm("Are you sure you want to delete this tracked app?")) {
                    const id = parseInt(e.currentTarget.getAttribute('data-id'));
                    const err = await RemoveWatcherRule(id);
                    if (err) alert("Failed to delete: " + err);
                    else {
                        loadWatcherRules();
                        loadWatcherData();
                    }
                }
            });
        });

        document.querySelectorAll('.btn-edit-rule').forEach(btn => {
            btn.addEventListener('click', (e) => {
                const b = e.currentTarget;
                document.getElementById('rule-id').value = b.getAttribute('data-id');
                // The dropdown option might not exist for this match anymore if it's already mapped, 
                // so we add it temporarily if missing.
                const matchVal = b.getAttribute('data-match');
                const matchSelect = document.getElementById('rule-match');
                let found = false;
                for(let i=0; i<matchSelect.options.length; i++) {
                    if(matchSelect.options[i].value === matchVal) found = true;
                }
                if(!found) {
                    const opt = document.createElement('option');
                    opt.value = matchVal;
                    opt.innerText = matchVal;
                    matchSelect.appendChild(opt);
                }
                matchSelect.value = matchVal;
                document.getElementById('rule-name').value = b.getAttribute('data-name');
                document.getElementById('rule-category').value = b.getAttribute('data-category');
                document.getElementById('btn-save-rule').innerText = "Save Changes";
                document.getElementById('btn-cancel-rule-edit').style.display = "inline-block";
                document.getElementById('rule-name').focus();
            });
        });

    } catch (e) {
        console.error("Failed to load watcher rules", e);
    }
}

async function loadWatcherData() {
    try {
        const stats = await GetTodayProductivityStats();
        // Generate conic gradient colors
            const colors = ['#8A2BE2', '#00FFFF', '#FF1493', '#FFD700', '#32CD32', '#FF4500', '#FF6347', '#4682B4'];
            
            // ── Load Today's Detailed Logs ──
            const legendContainer = document.getElementById('watcher-pie-legend');
            if (legendContainer) legendContainer.innerHTML = '';
            
            try {
                const detailedLogs = await GetDetailedLogs(1); // 1 day
                if (!detailedLogs || detailedLogs.length === 0) {
                    if (legendContainer) legendContainer.innerHTML = '<p style="color: var(--text-secondary)">No tasks recorded today.</p>';
                } else {
                    // Aggregate logs by App Name
                    const appAggregates = {};
                    detailedLogs.forEach(log => {
                        if (!appAggregates[log.appName]) {
                            appAggregates[log.appName] = {
                                appName: log.appName,
                                category: log.category,
                                firstStarted: log.startedAt,
                                lastEnded: log.endedAt,
                                totalSecs: 0
                            };
                        }
                        appAggregates[log.appName].totalSecs += log.durationSecs;
                        if (new Date(log.startedAt) < new Date(appAggregates[log.appName].firstStarted)) {
                            appAggregates[log.appName].firstStarted = log.startedAt;
                        }
                        if (new Date(log.endedAt) > new Date(appAggregates[log.appName].lastEnded)) {
                            appAggregates[log.appName].lastEnded = log.endedAt;
                        }
                    });
                    
                    const apps = Object.values(appAggregates).sort((a, b) => b.totalSecs - a.totalSecs);
                    const totalTodaySecs = apps.reduce((sum, app) => sum + app.totalSecs, 0);
                    
                    // Draw Pie Chart & Table
                    let gradientStops = [];
                    let currentPercent = 0;
                    
                    apps.forEach((app, idx) => {
                        const color = colors[idx % colors.length];
                        const percent = (app.totalSecs / totalTodaySecs) * 100;
                        const hrs = Math.floor(app.totalSecs / 3600);
                        const mins = Math.floor((app.totalSecs % 3600) / 60);
                        const timeStr = hrs > 0 ? `${hrs}h ${mins}m` : `${mins}m`;
                        
                        const row = document.createElement('div');
                        row.style = "display: flex; flex-direction: column; gap: 5px; margin-bottom: 10px;";
                        row.innerHTML = `
                            <div style="display: flex; justify-content: space-between; align-items: center; font-size: 0.9rem;">
                                <div style="display:flex; align-items:center; gap:8px;">
                                    <div style="width:10px; height:10px; background:${color}; border-radius:3px; box-shadow: 0 0 5px ${color}80;"></div>
                                    <span style="font-weight: 500;">${app.appName}</span>
                                </div>
                                <div style="display:flex; gap:10px; color: var(--text-secondary);">
                                    <span>${timeStr}</span>
                                    <span style="font-weight: bold; width: 35px; text-align: right;">${Math.round(percent)}%</span>
                                </div>
                            </div>
                            <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.05); border-radius: 3px; overflow: hidden;">
                                <div style="width: ${percent}%; height: 100%; background: ${color}; border-radius: 3px;"></div>
                            </div>
                        `;
                        if (legendContainer) legendContainer.appendChild(row);
                    });
                }
            } catch(e) {
                console.error("Failed to load today's tasks", e);
            }
            
            // ── Load All Time Summary ──
            try {
                const allLogs = await GetDetailedLogs(365); // basically all time
                const allTimePie = document.getElementById('watcher-all-time-pie');
                const allTimeTbody = document.getElementById('all-time-summary-tbody');
                allTimeTbody.innerHTML = '';
                
                if (!allLogs || allLogs.length === 0) {
                    allTimeTbody.innerHTML = '<tr><td colspan="5" style="text-align:center;">No history recorded yet.</td></tr>';
                } else {
                    const appAggregates = {};
                    allLogs.forEach(log => {
                        if (!appAggregates[log.appName]) {
                            appAggregates[log.appName] = {
                                appName: log.appName,
                                category: log.category,
                                firstStarted: log.startedAt,
                                lastEnded: log.endedAt,
                                totalSecs: 0
                            };
                        }
                        appAggregates[log.appName].totalSecs += log.durationSecs;
                        if (new Date(log.startedAt) < new Date(appAggregates[log.appName].firstStarted)) {
                            appAggregates[log.appName].firstStarted = log.startedAt;
                        }
                        if (new Date(log.endedAt) > new Date(appAggregates[log.appName].lastEnded)) {
                            appAggregates[log.appName].lastEnded = log.endedAt;
                        }
                    });
                    
                    const apps = Object.values(appAggregates).sort((a, b) => b.totalSecs - a.totalSecs);
                    const totalAllSecs = apps.reduce((sum, app) => sum + app.totalSecs, 0);
                    
                    apps.forEach((app, idx) => {
                        const color = colors[idx % colors.length];
                        const percent = (app.totalSecs / totalAllSecs) * 100;
                        const hrs = Math.floor(app.totalSecs / 3600);
                        const mins = Math.floor((app.totalSecs % 3600) / 60);
                        const timeStr = hrs > 0 ? `${hrs}h ${mins}m` : `${mins}m`;
                        
                        const row = document.createElement('div');
                        row.style = "display: flex; flex-direction: column; gap: 5px; margin-bottom: 10px;";
                        row.innerHTML = `
                            <div style="display: flex; justify-content: space-between; align-items: center; font-size: 0.9rem;">
                                <div style="display:flex; align-items:center; gap:8px;">
                                    <div style="width:10px; height:10px; background:${color}; border-radius:3px; box-shadow: 0 0 5px ${color}80;"></div>
                                    <span style="font-weight: 500;">${app.appName}</span>
                                </div>
                                <div style="display:flex; gap:10px; color: var(--text-secondary);">
                                    <span>${timeStr}</span>
                                    <span style="font-weight: bold; width: 35px; text-align: right;">${Math.round(percent)}%</span>
                                </div>
                            </div>
                            <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.05); border-radius: 3px; overflow: hidden;">
                                <div style="width: ${percent}%; height: 100%; background: ${color}; border-radius: 3px;"></div>
                            </div>
                        `;
                        allTimeTbody.appendChild(row);
                    });
                }
            } catch(e) {
                console.error("Failed to load all time summary", e);
            }

    // Load History based on Dropdown
    const taskSelect = document.getElementById('watcher-task-select');
    const selectedTask = taskSelect.value;
        const historyContainer = document.getElementById('watcher-history-content');
        historyContainer.innerHTML = '';
        
        if (selectedTask === 'all') {
            const logs = await GetDetailedLogs(7);
            if (!logs || logs.length === 0) {
                historyContainer.innerHTML = '<p style="color: var(--text-secondary)">No history data available.</p>';
            } else {
                // Group by date
                const days = {};
                logs.forEach(log => {
                    const dateKey = new Date(log.startedAt).toLocaleDateString();
                    if (!days[dateKey]) days[dateKey] = {};
                    if (!days[dateKey][log.appName]) {
                        days[dateKey][log.appName] = { category: log.category, totalSecs: 0, firstStarted: log.startedAt, lastEnded: log.endedAt };
                    }
                    days[dateKey][log.appName].totalSecs += log.durationSecs;
                    if (new Date(log.startedAt) < new Date(days[dateKey][log.appName].firstStarted)) days[dateKey][log.appName].firstStarted = log.startedAt;
                    if (new Date(log.endedAt) > new Date(days[dateKey][log.appName].lastEnded)) days[dateKey][log.appName].lastEnded = log.endedAt;
                });
                
                // Sort dates descending
                const sortedDates = Object.keys(days).sort((a,b) => new Date(b) - new Date(a));
                
                sortedDates.forEach(date => {
                    const appAgg = days[date];
                    const apps = Object.keys(appAgg).map(k => ({ appName: k, ...appAgg[k] })).sort((a,b) => b.totalSecs - a.totalSecs);
                    const totalSecs = apps.reduce((acc, curr) => acc + curr.totalSecs, 0);
                    
                    const hrs = Math.floor(totalSecs / 3600);
                    const mins = Math.floor((totalSecs % 3600) / 60);
                    const timeStr = hrs > 0 ? `${hrs}h ${mins}m` : `${mins}m`;
                    
                    let dayHtml = `
                        <div style="margin-bottom: 20px; border: 1px solid var(--border-color); border-radius: 8px; padding: 15px; background: var(--bg-darker);">
                            <div style="display: flex; justify-content: space-between; font-weight: bold; margin-bottom: 15px; font-size: 1.1rem;">
                                <span>${date}</span>
                                <span>Total: ${timeStr}</span>
                            </div>
                            <div style="display: flex; gap: 20px; align-items: flex-start;">
                    `;
                    
                    let gradientStops = [];
                    let currentPct = 0;
                    
                    // Table part
                    dayHtml += `<div style="flex: 2;"><table class="table" style="width: 100%; font-size: 0.9rem;">
                        <thead><tr><th>App Name</th><th>Category</th><th>Time</th><th>%</th></tr></thead><tbody>`;
                    
                    apps.forEach((app, i) => {
                        const sHrs = Math.floor(app.totalSecs / 3600);
                        const sMins = Math.floor((app.totalSecs % 3600) / 60);
                        const sTimeStr = sHrs > 0 ? `${sHrs}h ${sMins}m` : `${sMins}m`;
                        const pct = (app.totalSecs / totalSecs) * 100;
                        const c = colors[i % colors.length];
                        
                        gradientStops.push(`${c} ${currentPct}% ${currentPct + pct}%`);
                        currentPct += pct;
                        
                        dayHtml += `<tr>
                            <td><span style="display:inline-block; width:10px; height:10px; background:${c}; border-radius:50%; margin-right:5px;"></span><strong>${app.appName}</strong></td>
                            <td style="color:var(--text-secondary)">${app.category}</td>
                            <td>${sTimeStr}</td>
                            <td>${Math.round(pct)}%</td>
                        </tr>`;
                    });
                    
                    dayHtml += `</tbody></table></div>`;
                    
                    // Pie chart part
                    let pieGradient = gradientStops.length > 0 ? `conic-gradient(${gradientStops.join(', ')})` : `var(--bg-tertiary)`;
                    dayHtml += `<div style="flex: 1; display:flex; justify-content:center; align-items:center;">
                        <div style="width: 120px; height: 120px; border-radius: 50%; background: ${pieGradient}; box-shadow: 0 4px 10px rgba(0,0,0,0.3);"></div>
                    </div></div>
                    
                    <div style="margin-top: 15px; padding-top: 15px; border-top: 1px solid var(--border-color);">
                        <h4 style="margin-bottom:10px; color:var(--text-secondary);">Daily Timeline</h4>
                        <table class="table" style="width: 100%; font-size: 0.85rem;">
                            <thead><tr><th>Name</th><th>Time Started</th><th>Time Ended</th><th>Focus Time</th></tr></thead>
                            <tbody>
                    `;
                    
                    // Also output the specific timeline for that day (aggregated by first/last per app)
                    apps.forEach(app => {
                        const sHrs = Math.floor(app.totalSecs / 3600);
                        const sMins = Math.floor((app.totalSecs % 3600) / 60);
                        const sTimeStr = sHrs > 0 ? `${sHrs}h ${sMins}m` : `${sMins}m`;
                        dayHtml += `<tr>
                            <td>${app.appName}</td>
                            <td>${new Date(app.firstStarted).toLocaleTimeString()}</td>
                            <td>${new Date(app.lastEnded).toLocaleTimeString()}</td>
                            <td style="color:var(--accent-color); font-weight:bold;">${sTimeStr}</td>
                        </tr>`;
                    });
                    
                    dayHtml += `</tbody></table></div></div>`;
                    
                    historyContainer.innerHTML += dayHtml;
                });
            }
        } else {
            // Filter by specific task
            const history = await GetProductivityHistoryByApp(selectedTask, 7);
            if (!history || history.length === 0) {
                historyContainer.innerHTML = '<p style="color: var(--text-secondary)">No history data available for this task.</p>';
            } else {
                
                // Draw chart for this task
                let chartHTML = `<div style="display: flex; gap: 2px; align-items: flex-end; height: 100px; padding: 10px 0; border-bottom: 1px solid var(--border-color);">`;
                const maxSecs = Math.max(...history.map(h => h.totalSecs), 60);
                
                const reversedHistory = [...history].reverse(); // oldest to newest for chart
                reversedHistory.forEach(day => {
                    const heightPct = (day.totalSecs / maxSecs) * 100;
                    const hrs = Math.floor(day.totalSecs / 3600);
                    const mins = Math.floor((day.totalSecs % 3600) / 60);
                    const timeStr = hrs > 0 ? `${hrs}h ${mins}m` : `${mins}m`;
                    chartHTML += `<div style="flex: 1; min-width: 10px; height: ${heightPct}%; background-color: var(--accent-color); opacity: 0.8;" title="${day.date}: ${timeStr}"></div>`;
                });
                chartHTML += `</div>`;
                historyContainer.innerHTML += chartHTML;

                history.forEach(day => {
                    const hrs = Math.floor(day.totalSecs / 3600);
                    const mins = Math.floor((day.totalSecs % 3600) / 60);
                    const timeStr = hrs > 0 ? `${hrs}h ${mins}m` : `${mins}m`;
                    
                    let dayHtml = `
                        <div style="margin-bottom: 10px; border-bottom: 1px solid var(--border-color); padding-bottom: 10px;">
                            <div style="display: flex; justify-content: space-between; font-weight: bold; margin-bottom: 5px;">
                                <span>${day.date}</span>
                                <span>${timeStr}</span>
                            </div>
                        </div>
                    `;
                    historyContainer.innerHTML += dayHtml;
                });
            }
        }
    } catch (e) {
        console.error("Failed to load watcher stats", e);
    }
}

// Ensure the dropdown triggers a reload of Watcher Data when changed
document.getElementById('watcher-task-select').addEventListener('change', loadWatcherData);

// Function to populate dropdown options once
let watcherDropdownPopulated = false;
async function populateWatcherDropdown() {
    if (watcherDropdownPopulated) return;
    try {
        const apps = await GetUniqueApps();
        const select = document.getElementById('watcher-task-select');
        if (apps && apps.length > 0) {
            apps.forEach(app => {
                const opt = document.createElement('option');
                opt.value = app;
                opt.innerText = app;
                select.appendChild(opt);
            });
            watcherDropdownPopulated = true;
        }
    } catch(e) {
        console.error("Failed to populate watcher dropdown", e);
    }
}

async function populateUnmappedProgramsDropdown() {
    try {
        const unmapped = await GetUnmappedPrograms();
        const select = document.getElementById('rule-match');
        select.innerHTML = '<option value="" disabled selected>Select an unmapped program...</option>';
        if (unmapped && unmapped.length > 0) {
            unmapped.forEach(app => {
                const opt = document.createElement('option');
                opt.value = app;
                opt.innerText = app;
                select.appendChild(opt);
            });
        }
    } catch(e) {
        console.error("Failed to populate unmapped programs dropdown", e);
    }
}

// History Tabs Logic
document.getElementById('tab-btn-summary').addEventListener('click', () => {
    document.getElementById('tab-btn-summary').className = 'btn primary';
    document.getElementById('tab-btn-detailed').className = 'btn secondary';
    document.getElementById('tab-btn-timeline').className = 'btn secondary';
    document.getElementById('watcher-history-content').style.display = 'block';
    document.getElementById('watcher-detailed-logs-content').style.display = 'none';
    document.getElementById('watcher-timeline-content').style.display = 'none';
});

document.getElementById('tab-btn-detailed').addEventListener('click', async () => {
    document.getElementById('tab-btn-summary').className = 'btn secondary';
    document.getElementById('tab-btn-detailed').className = 'btn primary';
    document.getElementById('tab-btn-timeline').className = 'btn secondary';
    document.getElementById('watcher-history-content').style.display = 'none';
    document.getElementById('watcher-timeline-content').style.display = 'none';
    document.getElementById('watcher-detailed-logs-content').style.display = 'block';
    
    // Load Detailed Logs
    const grid = document.getElementById('detailed-logs-grid');
    grid.innerHTML = '<div style="color:var(--text-secondary); text-align:center; grid-column: 1 / -1; padding: 20px;">Loading...</div>';
    try {
        // Just fetch last 7 days for the detailed logs
        const logs = await GetDetailedLogs(7); 
        grid.innerHTML = '';
        if (!logs || logs.length === 0) {
            grid.innerHTML = '<div style="color:var(--text-secondary); text-align:center; grid-column: 1 / -1; padding: 20px;">No detailed logs available.</div>';
            return;
        }
        
        // Filter by the task select dropdown if a specific task is selected
        const taskSelect = document.getElementById('watcher-task-select').value;
        const filteredLogs = taskSelect === 'all' ? logs : logs.filter(l => l.appName === taskSelect);
        
        if (filteredLogs.length === 0) {
            grid.innerHTML = '<div style="color:var(--text-secondary); text-align:center; grid-column: 1 / -1; padding: 20px;">No detailed logs for this task.</div>';
            return;
        }

        // Aggregate by date and appName
        const aggregated = {};
        filteredLogs.forEach(log => {
            const dateKey = new Date(log.startedAt).toLocaleDateString();
            const key = dateKey + '_' + log.appName;
            if (!aggregated[key]) {
                aggregated[key] = {
                    appName: log.appName,
                    category: log.category,
                    date: dateKey,
                    firstStarted: log.startedAt,
                    lastEnded: log.endedAt,
                    totalSecs: 0
                };
            }
            aggregated[key].totalSecs += log.durationSecs;
            if (new Date(log.startedAt) < new Date(aggregated[key].firstStarted)) aggregated[key].firstStarted = log.startedAt;
            if (new Date(log.endedAt) > new Date(aggregated[key].lastEnded)) aggregated[key].lastEnded = log.endedAt;
        });

        // Sort by date descending, then time descending
        const aggArray = Object.values(aggregated).sort((a, b) => new Date(b.date) - new Date(a.date) || b.totalSecs - a.totalSecs);

        aggArray.forEach(log => {
            const card = document.createElement('div');
            card.className = 'card';
            card.style = 'padding: 15px; display: flex; flex-direction: column; justify-content: space-between;';
            const hrs = Math.floor(log.totalSecs / 3600);
            const mins = Math.floor((log.totalSecs % 3600) / 60);
            const timeStr = hrs > 0 ? `${hrs}h ${mins}m` : `${mins}m`;
            
            card.innerHTML = `
                <div style="display: flex; justify-content: space-between; margin-bottom: 15px;">
                    <div>
                        <div style="font-weight: bold; font-size: 1.1rem; color: var(--text-primary);">${log.appName}</div>
                        <div style="font-size: 0.85rem; color: var(--text-secondary);">${log.category}</div>
                    </div>
                    <div style="text-align: right;">
                        <div style="font-weight: bold; color: var(--accent-color); font-size: 1.1rem;">${timeStr}</div>
                        <div style="font-size: 0.8rem; color: var(--text-secondary);">Focus Time</div>
                    </div>
                </div>
                <div style="font-size: 0.85rem; color: var(--text-secondary); background: var(--bg-dark); padding: 8px; border-radius: 4px;">
                    <div style="margin-bottom: 4px;"><strong>Date:</strong> ${log.date}</div>
                    <div style="display: flex; justify-content: space-between;">
                        <span><strong>In:</strong> ${new Date(log.firstStarted).toLocaleTimeString()}</span>
                        <span><strong>Out:</strong> ${new Date(log.lastEnded).toLocaleTimeString()}</span>
                    </div>
                </div>
            `;
            grid.appendChild(card);
        });
    } catch (e) {
        console.error("Failed to load detailed logs", e);
    }
});

document.getElementById('tab-btn-timeline').addEventListener('click', async () => {
    document.getElementById('tab-btn-summary').className = 'btn secondary';
    document.getElementById('tab-btn-detailed').className = 'btn secondary';
    document.getElementById('tab-btn-timeline').className = 'btn primary';
    document.getElementById('watcher-history-content').style.display = 'none';
    document.getElementById('watcher-detailed-logs-content').style.display = 'none';
    document.getElementById('watcher-timeline-content').style.display = 'block';
    
    const container = document.getElementById('timeline-container');
    container.innerHTML = '<div style="text-align:center; padding:20px; color:var(--text-secondary);">Loading...</div>';
    
    try {
        const logs = await GetDetailedLogs(1);
        if (!logs || logs.length === 0) {
            container.innerHTML = '<div style="text-align:center; padding:20px; color:var(--text-secondary);">No data for today.</div>';
            return;
        }
        
        const todayStr = new Date().toLocaleDateString();
        const todayLogs = logs.filter(l => new Date(l.startedAt).toLocaleDateString() === todayStr);
        
        if (todayLogs.length === 0) {
            container.innerHTML = '<div style="text-align:center; padding:20px; color:var(--text-secondary);">No data for today.</div>';
            return;
        }

        const startOfDay = new Date();
        startOfDay.setHours(0,0,0,0);
        const dayMs = 24 * 60 * 60 * 1000;
        
        let html = '';
        todayLogs.forEach(log => {
            const startMs = new Date(log.startedAt).getTime();
            const endMs = new Date(log.endedAt).getTime();
            
            let leftPerc = ((startMs - startOfDay.getTime()) / dayMs) * 100;
            let widthPerc = ((endMs - startMs) / dayMs) * 100;
            
            if (leftPerc < 0) leftPerc = 0;
            if (leftPerc + widthPerc > 100) widthPerc = 100 - leftPerc;
            
            let color = 'var(--accent-warning)';
            if (log.category === 'Productive') color = 'var(--accent-success)';
            if (log.category === 'Unproductive') color = 'var(--accent-danger)';
            
            html += `<div title="${log.appName} (${log.category})\nFrom: ${new Date(log.startedAt).toLocaleTimeString()}\nTo: ${new Date(log.endedAt).toLocaleTimeString()}" style="position: absolute; left: ${leftPerc}%; width: ${widthPerc}%; height: 100%; background: ${color}; border-right: 1px solid rgba(0,0,0,0.2); cursor: pointer; transition: opacity 0.2s;" onmouseover="this.style.opacity='0.8'" onmouseout="this.style.opacity='1'"></div>`;
        });
        container.innerHTML = html;
        
    } catch(e) {
        container.innerHTML = '<div style="color:var(--accent-danger); padding:20px;">Error loading timeline.</div>';
        console.error("Timeline error:", e);
    }
});

async function loadSnapshotsData() {
    try {
        const path = document.getElementById('snapshot-path').value;
        if (!path) return;
        
        const history = await GetSnapshotHistory(path, 15);
        const tbody = document.getElementById('snapshots-table-body');
        tbody.innerHTML = '';
        
        if (!history || history.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7" style="color: var(--text-secondary); text-align: center;">No snapshots found for this project.</td></tr>';
            return;
        }

        history.forEach((s, i) => {
            const sizeMB = (s.totalBytes / (1024*1024)).toFixed(2) + " MB";
            const date = new Date(s.createdAt).toLocaleString();
            
            const tr = document.createElement('tr');
            tr.innerHTML = `
                <td><span class="hash-badge" title="${s.fullHash}">${s.commitHash}</span></td>
                <td>
                    <span id="snap-msg-${s.id}">${s.message || '-'}</span>
                    <button class="btn-icon btn-edit-snap" data-id="${s.id}" data-msg="${s.message}" title="Edit Message" style="margin-left:5px;">✏️</button>
                    <button class="btn-icon btn-ai-snap" data-id="${s.id}" data-prev="${i < history.length-1 ? history[i+1].id : 0}" title="Auto Generate Message with Gemini" style="margin-left:5px;">✨</button>
                </td>
                <td title="${s.projectPath}">${s.projectPath.length > 25 ? '...' + s.projectPath.substring(s.projectPath.length - 22) : s.projectPath}</td>
                <td>${s.fileCount}</td>
                <td>${sizeMB}</td>
                <td>${date}</td>
                <td style="display:flex; gap: 5px;">
                    <button class="btn primary btn-diff-snap" data-id="${s.id}" data-prev="${i < history.length-1 ? history[i+1].id : 0}" title="View Diff">Diff</button>
                    <button class="btn warning btn-restore-snap" data-id="${s.id}" data-path="${s.projectPath}" title="Rollback to this snapshot">Rollback</button>
                    <button class="btn danger btn-delete-snap" data-id="${s.id}" title="Delete Snapshot">Del</button>
                </td>
            `;
            tbody.appendChild(tr);
        });

        // Event listeners
        document.querySelectorAll('.btn-edit-snap').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                const id = parseInt(e.currentTarget.getAttribute('data-id'));
                const oldMsg = e.currentTarget.getAttribute('data-msg');
                const newMsg = prompt("Enter new message:", oldMsg);
                if (newMsg !== null && newMsg !== oldMsg) {
                    try {
                        await UpdateSnapshot(id, newMsg);
                        loadSnapshotsData();
                    } catch(err) {
                        alert("Update failed: " + err);
                    }
                }
            });
        });

        document.querySelectorAll('.btn-ai-snap').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                const id = parseInt(e.currentTarget.getAttribute('data-id'));
                const prevId = parseInt(e.currentTarget.getAttribute('data-prev'));
                
                const apiKey = prompt("Enter Gemini API Key (Free tier):");
                if (!apiKey) return;

                const originalHTML = e.currentTarget.innerHTML;
                e.currentTarget.innerHTML = "⏳";
                e.currentTarget.disabled = true;

                try {
                    const aiProvider = localStorage.getItem('ai_provider') || 'gemini';
                    let aiConfig = '';
                    if (aiProvider === 'gemini') {
                        aiConfig = localStorage.getItem('gemini_api_key');
                        if (!aiConfig) throw new Error("Gemini API key missing in settings");
                    } else if (aiProvider === 'nvidia') {
                        const nvKey = localStorage.getItem('nvidia_api_key');
                        const nvMod = localStorage.getItem('nvidia_model');
                        if (!nvKey) throw new Error("Nvidia API key missing in settings");
                        aiConfig = `${nvKey}|${nvMod || 'deepseek-ai/deepseek-coder-33b-instruct'}`;
                    } else {
                        aiConfig = localStorage.getItem('ollama_model') || 'llama3';
                    }

                    await AutoGenerateCommitMessage(prevId, id, aiProvider, aiConfig);
                    loadSnapshotsData();
                } catch(err) {
                    alert("AI Generation failed: " + err);
                } finally {
                    e.currentTarget.innerHTML = originalHTML;
                    e.currentTarget.disabled = false;
                }
            });
        });

        document.querySelectorAll('.btn-delete-snap').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                const id = parseInt(e.currentTarget.getAttribute('data-id'));
                if (confirm("Delete this snapshot permanently?")) {
                    try {
                        await DeleteSnapshot(id);
                        loadSnapshotsData();
                    } catch(err) {
                        alert("Delete failed: " + err);
                    }
                }
            });
        });

        document.querySelectorAll('.btn-restore-snap').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                const id = parseInt(e.currentTarget.getAttribute('data-id'));
                const targetPath = e.currentTarget.getAttribute('data-path');
                if (confirm("WARNING: This will overwrite current files in " + targetPath + ". A stash will be attempted. Proceed?")) {
                    try {
                        e.currentTarget.innerText = "...";
                        await RestoreSnapshot(id, targetPath);
                        alert("Snapshot restored successfully.");
                        loadSnapshotsData();
                    } catch(err) {
                        alert("Restore failed: " + err);
                    } finally {
                        e.currentTarget.innerText = "Rollback";
                    }
                }
            });
        });

        document.querySelectorAll('.btn-diff-snap').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                const id = parseInt(e.currentTarget.getAttribute('data-id'));
                const prevId = parseInt(e.currentTarget.getAttribute('data-prev'));
                if (prevId === 0) {
                    alert("This is the first snapshot, no previous snapshot to diff against.");
                    return;
                }
                
                try {
                    const diff = await GetSnapshotDiff(prevId, id);
                    const modal = document.getElementById('snapshot-diff-modal');
                    const fileList = document.getElementById('snapshot-diff-file-list');
                    const content = document.getElementById('snapshot-diff-content');
                    
                    let fileListHTML = '';
                    const renderFileDiff = (f) => {
                        let html = `<div style="margin-bottom: 15px;">
                            <div style="font-weight: bold; font-size: 1.1rem; margin-bottom: 10px;">${f.relPath}</div>
                            <div style="background: var(--bg-dark); border: 1px solid var(--border-color); border-radius: 6px; padding: 10px; overflow-x: auto;">
                        `;
                        if (f.chunks && f.chunks.length > 0) {
                            f.chunks.forEach(c => {
                                let text = c.text;
                                if (text.endsWith('\n')) text = text.slice(0, -1);
                                const lines = text.split('\n');
                                
                                let displayLines = lines;
                                if (c.type === 0 && lines.length > 6) {
                                    displayLines = [...lines.slice(0, 3), "...", ...lines.slice(-3)];
                                }

                                displayLines.forEach(line => {
                                    if (line === "...") {
                                        html += `<div style="padding: 5px; color: var(--text-secondary); text-align: center; background: rgba(0,0,0,0.2); margin: 2px 0;">...</div>`;
                                        return;
                                    }
                                    const escaped = line.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
                                    if (c.type === -1) {
                                        html += `<div style="background: rgba(255,0,0,0.15); color: #ff8888; padding: 0 5px;"><span style="user-select:none; display:inline-block; width: 20px;">-</span>${escaped}</div>`;
                                    } else if (c.type === 1) {
                                        html += `<div style="background: rgba(0,255,0,0.15); color: #88ff88; padding: 0 5px;"><span style="user-select:none; display:inline-block; width: 20px;">+</span>${escaped}</div>`;
                                    } else {
                                        html += `<div style="padding: 0 5px;"><span style="user-select:none; display:inline-block; width: 20px; color: var(--text-secondary);"> </span><span style="color: var(--text-secondary);">${escaped}</span></div>`;
                                    }
                                });
                            });
                        }
                        html += `</div></div>`;
                        content.innerHTML = html;
                    };

                    window._currentDiffData = diff;
                    window._currentDiffIds = { prevId, id };
                    document.getElementById('snapshot-diff-ai-result').style.display = 'none';
                    
                    if (diff.addedFiles) {
                        diff.addedFiles.forEach(f => {
                            fileListHTML += `<div class="diff-file-item" style="padding: 8px 15px; cursor: pointer; color: var(--accent-success); border-bottom: 1px solid var(--border-color); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" onclick="document.getElementById('snapshot-diff-content').innerHTML = '<div style=\\'color:var(--accent-success); font-size:1.1rem; margin-top:20px;\\'>New File: ' + '${f}' + '</div>'">+ ${f}</div>`;
                        });
                    }
                    if (diff.deletedFiles) {
                        diff.deletedFiles.forEach(f => {
                            fileListHTML += `<div class="diff-file-item" style="padding: 8px 15px; cursor: pointer; color: var(--accent-danger); border-bottom: 1px solid var(--border-color); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" onclick="document.getElementById('snapshot-diff-content').innerHTML = '<div style=\\'color:var(--accent-danger); font-size:1.1rem; margin-top:20px;\\'>Deleted File: ' + '${f}' + '</div>'">- ${f}</div>`;
                        });
                    }
                    if (diff.modifiedFiles) {
                        diff.modifiedFiles.forEach((f, idx) => {
                            fileListHTML += `<div class="diff-file-item" style="padding: 8px 15px; cursor: pointer; border-bottom: 1px solid var(--border-color); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" onclick="window._renderFileDiff(${idx})">~ ${f.relPath}</div>`;
                        });
                        window._renderFileDiff = (idx) => {
                            renderFileDiff(diff.modifiedFiles[idx]);
                        };
                    }

                    if (fileListHTML === '') {
                        fileListHTML = '<div style="padding: 15px; color: var(--text-secondary);">No changes</div>';
                    }

                    fileList.innerHTML = fileListHTML;
                    content.innerHTML = '<div style="color: var(--text-secondary); text-align: center; margin-top: 50px;">Select a file to view its diff</div>';
                    modal.classList.remove('hidden');
                    modal.style.display = 'flex';
                } catch(err) {
                    alert("Diff failed: " + err);
                }
            });
        });

    } catch (e) {
        console.error("Failed to load snapshots", e);
    }
}

// ── Command Palette Logic ──

const COMMANDS = [
    { name: "Go to Sentinel (Uptime)", category: "Navigation", action: () => document.getElementById('nav-sentinel').click() },
    { name: "Go to Resources (System)", category: "Navigation", action: () => document.getElementById('nav-resources').click() },
    { name: "Go to Watcher (Productivity)", category: "Navigation", action: () => document.getElementById('nav-watcher').click() },
    { name: "Go to Pomodoro (Focus)", category: "Navigation", action: () => document.getElementById('nav-pomodoro').click() },
    { name: "Go to Snapshots", category: "Navigation", action: () => document.getElementById('nav-snapshots').click() },
    
    { name: "Add Uptime Monitor", category: "Sentinel", action: () => { document.getElementById('nav-sentinel').click(); setTimeout(()=>document.getElementById('target-name').focus(), 100); } },
    { name: "Track New App", category: "Watcher", action: () => { document.getElementById('nav-watcher').click(); setTimeout(()=>document.getElementById('rule-name').focus(), 100); } },
    { name: "Start Pomodoro", category: "Pomodoro", action: () => { document.getElementById('nav-pomodoro').click(); document.getElementById('btn-pomodoro-start').click(); } },
    { name: "Take Project Snapshot", category: "Snapshots", action: () => { document.getElementById('nav-snapshots').click(); setTimeout(()=>document.getElementById('snapshot-msg').focus(), 100); } },
];

function setupCommandPalette() {
    const overlay = document.getElementById('command-palette-overlay');
    const searchInput = document.getElementById('command-search');
    const resultsContainer = document.getElementById('command-results');
    
    let selectedIndex = 0;
    let filteredCommands = [];

    function renderResults() {
        resultsContainer.innerHTML = '';
        if (filteredCommands.length === 0) {
            resultsContainer.innerHTML = '<div style="padding: 20px; color: var(--text-secondary); text-align: center;">No matching commands found.</div>';
            return;
        }

        filteredCommands.forEach((cmd, idx) => {
            const div = document.createElement('div');
            div.className = 'command-item' + (idx === selectedIndex ? ' selected' : '');
            div.innerHTML = `
                <span>${cmd.name}</span>
                <span class="cmd-category">${cmd.category}</span>
            `;
            
            div.addEventListener('mouseenter', () => {
                selectedIndex = idx;
                renderResults();
            });

            div.addEventListener('click', () => {
                executeCommand(cmd);
            });
            
            resultsContainer.appendChild(div);
        });
        
        // Auto scroll
        const selectedEl = resultsContainer.querySelector('.selected');
        if (selectedEl) {
            selectedEl.scrollIntoView({ block: 'nearest' });
        }
    }

    function executeCommand(cmd) {
        overlay.classList.add('hidden');
        cmd.action();
    }

    function openPalette() {
        overlay.classList.remove('hidden');
        searchInput.value = '';
        filteredCommands = [...COMMANDS];
        selectedIndex = 0;
        renderResults();
        setTimeout(() => searchInput.focus(), 50);
    }

    // Global Key Listener for Ctrl+K
    document.addEventListener('keydown', (e) => {
        if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
            e.preventDefault();
            if (overlay.classList.contains('hidden')) {
                openPalette();
            } else {
                overlay.classList.add('hidden');
            }
        }
        
        if (!overlay.classList.contains('hidden')) {
            if (e.key === 'Escape') {
                overlay.classList.add('hidden');
            } else if (e.key === 'ArrowDown') {
                e.preventDefault();
                selectedIndex = (selectedIndex + 1) % filteredCommands.length;
                renderResults();
            } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                selectedIndex = (selectedIndex - 1 + filteredCommands.length) % filteredCommands.length;
                renderResults();
            } else if (e.key === 'Enter') {
                e.preventDefault();
                if (filteredCommands[selectedIndex]) {
                    executeCommand(filteredCommands[selectedIndex]);
                }
            }
        }
    });

    searchInput.addEventListener('input', (e) => {
        const query = e.target.value.toLowerCase();
        filteredCommands = COMMANDS.filter(cmd => 
            cmd.name.toLowerCase().includes(query) || 
            cmd.category.toLowerCase().includes(query)
        );
        selectedIndex = 0;
        renderResults();
    });
    
    // Close on click outside
    overlay.addEventListener('click', (e) => {
        if (e.target === overlay) overlay.classList.add('hidden');
    });
}

window.loadPomodoroRunningApps = async function() {
    try {
        const apps = await window.go.main.App.GetRunningApps();
        const wl = document.getElementById('pomodoro-whitelist');
        const bl = document.getElementById('pomodoro-blacklist');
        
        // Preserve selected
        const wlSelected = Array.from(wl.selectedOptions).map(o => o.value);
        const blSelected = Array.from(bl.selectedOptions).map(o => o.value);

        let html = '';
        apps.forEach(app => {
            html += `<option value="${app}">${app}</option>`;
        });

        wl.innerHTML = html;
        bl.innerHTML = html;

        // Restore selected
        Array.from(wl.options).forEach(o => { if(wlSelected.includes(o.value)) o.selected = true; });
        Array.from(bl.options).forEach(o => { if(blSelected.includes(o.value)) o.selected = true; });
    } catch(err) {
        console.error("Failed to load running apps:", err);
    }
};

// ── Logs Module ──────────────────────────────────────────────────────────────
// Uses a fixed-size ring buffer on the backend (max 500 entries).
// We poll every 2s ONLY while the Logs tab is active, to avoid wasting resources.

let _logsInterval = null;
let _logsCache = []; // local copy of all fetched entries

function stopLogsPolling() {
    if (_logsInterval) {
        clearInterval(_logsInterval);
        _logsInterval = null;
    }
}

function startLogsPolling() {
    stopLogsPolling(); // clear any existing interval first
    renderLogs();      // immediate first load
    _logsInterval = setInterval(renderLogs, 2000);

    // Attach UI control handlers (idempotent — ok to re-attach)
    document.getElementById('logs-level-filter').onchange = renderLogsFromCache;
    document.getElementById('btn-clear-logs').onclick = () => {
        _logsCache = [];
        document.getElementById('logs-stream').innerHTML = '<span style="color:#555;">Cleared. New entries will appear shortly...</span>';
    };
    document.getElementById('btn-copy-logs').onclick = () => {
        const text = _logsCache.map(e => `[${e.time}] [${e.level}] (${e.caller}) ${e.message}`).join('\n');
        navigator.clipboard.writeText(text).then(() => alert('Logs copied to clipboard!'));
    };
}

function levelColor(level) {
    switch (level) {
        case 'DEBUG': return '#3abff8';
        case 'INFO':  return '#36d399';
        case 'WARN':  return '#fbbd23';
        case 'ERROR': return '#f87272';
        case 'FATAL': return '#f87272';
        default:      return '#aaa';
    }
}

async function renderLogs() {
    try {
        const entries = await GetLogs(500);
        if (!entries) return;

        // Merge new entries (avoid duplicates by checking last entry time+message)
        if (entries.length > _logsCache.length || 
            (entries.length > 0 && _logsCache.length > 0 && 
             entries[entries.length-1].message !== _logsCache[_logsCache.length-1].message)) {
            _logsCache = entries;
        }

        renderLogsFromCache();

        // Update last-updated timestamp
        document.getElementById('logs-last-updated').textContent = 
            'Updated: ' + new Date().toLocaleTimeString();
    } catch(err) {
        console.error('GetLogs failed:', err);
    }
}

function renderLogsFromCache() {
    const filter = document.getElementById('logs-level-filter').value;
    const stream = document.getElementById('logs-stream');

    let counts = { DEBUG: 0, INFO: 0, WARN: 0, ERROR: 0, FATAL: 0 };
    let html = '';

    const entries = filter === 'ALL'
        ? _logsCache
        : _logsCache.filter(e => e.level === filter);

    _logsCache.forEach(e => {
        if (counts[e.level] !== undefined) counts[e.level]++;
    });

    entries.forEach(entry => {
        const col = levelColor(entry.level);
        const msg = entry.message
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;');

        html += `<div style="display:flex; gap:10px; padding:1px 0; border-bottom:1px solid rgba(255,255,255,0.03);">` +
            `<span style="color:#555; flex-shrink:0; min-width:75px;">${entry.time}</span>` +
            `<span style="color:${col}; font-weight:700; flex-shrink:0; min-width:42px;">${entry.level}</span>` +
            `<span style="color:#444; flex-shrink:0; font-size:0.72rem; min-width:170px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;" title="${entry.caller}">${entry.caller}</span>` +
            `<span style="color:#ddd; word-break:break-word;">${msg}</span>` +
            `</div>`;
    });

    stream.innerHTML = html || '<span style="color:#555;">No entries matching filter.</span>';

    // Auto-scroll if checked
    if (document.getElementById('logs-auto-scroll').checked) {
        stream.scrollTop = stream.scrollHeight;
    }

    // Update stats
    document.getElementById('logs-count-total').textContent = _logsCache.length;
    document.getElementById('logs-count-info').textContent = counts.INFO;
    document.getElementById('logs-count-debug').textContent = counts.DEBUG;
    document.getElementById('logs-count-warn').textContent = counts.WARN;
    document.getElementById('logs-count-error').textContent = (counts.ERROR || 0) + (counts.FATAL || 0);
}
