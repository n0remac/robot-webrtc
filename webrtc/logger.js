const Logger = (() => {
    let enabled = false;              // controlled by server
    let wsLog   = null;               // second websocket for log shipping
    let seq     = 0;                  // monotonic counter
    let backlog = [];

    function setDebug(value) { enabled = value; }

    function attachSocket(url) {
        wsLog = new WebSocket(url);
        wsLog.onerror = e => console.warn('[log‑socket] error', e);

        /* flush once open */
        wsLog.onopen = () => {
            enabled = true;
            backlog.forEach(e => wsLog.send(JSON.stringify(e)));
            backlog = [];
        };
    }

    /** core log fn: level, msg, ...meta */
    function log(level, msg, ...meta) {
        if (!enabled) return;

        const entry = {
            t: performance.now().toFixed(1),   // high‑res timestamp
            s: ++seq,                          // sequence number
            lvl: level,
            msg,
            meta,
        };

        // 1) Browser console (colour by level)
        const colour = {INFO:'', WARN:'orange', ERR:'red'}[level] ?? '';
        console.log(`%c${entry.t}ms #${entry.s} ${level}: ${msg}`,
                    `color:${colour}`, ...meta);

        // 2) Backend (fire‑and‑forget)
        if (wsLog?.readyState === WebSocket.OPEN){
            wsLog.send(JSON.stringify(entry));
        } else {
            backlog.push(entry);
        }
    }

    return {
        setDebug,
        attachSocket,
        info : (...a)=>log('INFO', ...a),
        warn : (...a)=>log('WARN', ...a),
        error: (...a)=>log('ERR' , ...a),
    };
})();

function logToPage(...args) {
    let wrapper = document.getElementById('mobile-log-wrapper');
    let logEl = document.getElementById('mobile-log');

    if (!wrapper) {
        // Create wrapper div
        wrapper = document.createElement('div');
        wrapper.id = 'mobile-log-wrapper';
        wrapper.style = `
            position:fixed;left:0;right:0;bottom:0;
            z-index:9999;font-family:monospace;
            pointer-events:none; /* only enable on children */
        `;

        // Toggle button
        const toggleBtn = document.createElement('button');
        toggleBtn.id = 'mobile-log-toggle';
        toggleBtn.textContent = 'Show Logs';
        toggleBtn.style = `
            width:100%;background:#111;color:#00FF66;
            font:14px monospace;border:none;padding:8px 0;
            border-top:1px solid #333;cursor:pointer;
            pointer-events:auto;
        `;

        // Log content
        logEl = document.createElement('div');
        logEl.id = 'mobile-log';
        logEl.style = `
            display:none;
            background:rgba(0,0,0,0.92);
            color:#00FF66;
            font:12px monospace;
            max-height:30vh;
            overflow-y:auto;
            border-top:1px solid #333;
            padding:8px;
            pointer-events:auto;
        `;

        // Toggle logic
        toggleBtn.addEventListener('click', () => {
            const shown = logEl.style.display !== 'none';
            logEl.style.display = shown ? 'none' : 'block';
            toggleBtn.textContent = shown ? 'Show Logs' : 'Hide Logs';
        });

        wrapper.appendChild(toggleBtn);
        wrapper.appendChild(logEl);
        document.body.appendChild(wrapper);
    }

    // Write logs
    const msg = args.map(x => (typeof x === 'object' ? JSON.stringify(x) : x)).join(' ');
    logEl.textContent += msg + '\n';
    logEl.scrollTop = logEl.scrollHeight;
}

// Patch Logger to always log to page too
(function() {
    const origInfo = Logger.info, origWarn = Logger.warn, origError = Logger.error;
    Logger.info  = (...a) => { logToPage('[INFO]', ...a);  origInfo(...a);  };
    Logger.warn  = (...a) => { logToPage('[WARN]', ...a);  origWarn(...a);  };
    Logger.error = (...a) => { logToPage('[ERROR]', ...a); origError(...a); };
})();

