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

// function logToPage(...args) {
//     let el = document.getElementById('mobile-log');
//     if (!el) {
//         el = document.createElement('div');
//         el.id = 'mobile-log';
//         el.style = 'position:fixed;bottom:0;left:0;right:0;max-height:30vh;overflow:auto;background:rgba(0,0,0,0.85);color:#00FF66;font:12px monospace;z-index:9999;padding:4px;';
//         document.body.appendChild(el);
//     }
//     const msg = args.map(x => (typeof x === 'object' ? JSON.stringify(x) : x)).join(' ');
//     el.textContent += msg + '\n';
//     el.scrollTop = el.scrollHeight;
// }

// (function() {
//     // Patch Logger to always log to page too
//     const origInfo = Logger.info, origWarn = Logger.warn, origError = Logger.error;
//     Logger.info  = (...a) => { logToPage('[INFO]', ...a);  origInfo(...a);  };
//     Logger.warn  = (...a) => { logToPage('[WARN]', ...a);  origWarn(...a);  };
//     Logger.error = (...a) => { logToPage('[ERROR]', ...a); origError(...a); };
// })();
