// sfu.js (with perfect negotiation)

const ROOM = new URLSearchParams(location.search).get("room") || "default";

let myUUID = generateUUID();
let myName = "";
let ws, pc, localStream;
let globalIceServers = [];
let candidateQueue = [];
const MAX_CAND_QUEUE = 2048;
let remoteDescSet = false;
const remoteTrackMap = {};
const remoteByStream = new Map();
const overlays = new Map();
const latestBoxes = new Map();
const cursors = new Map();

window.addEventListener("beforeunload", () => {
    try { if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "leave" })); } catch { }
    try { ws?.close(); } catch { }
});

document.addEventListener("DOMContentLoaded", () => {
    document.getElementById("test-camera").addEventListener("click", testCamera);
    document.getElementById("test-mic").addEventListener("click", testMic);
    document.getElementById("join-btn").addEventListener("click", joinSFUSession);
});

async function joinSFUSession() {
    myName = document.getElementById("name").value || "";
    myUUID = generateUUID();
    Logger.info("SFU join click", { uuid: myUUID, name: myName, room: ROOM });

    document.getElementById("join-screen").style.display = "none";
    document.getElementById("participant-view").style.display = "block";

    const turnData = await fetchTurnCredentials();
    setupIceServers(turnData);
    await setupLocalMedia();
    showLocalVideo();
    await connectSFUSocket();
}

async function fetchTurnCredentials() {
    try {
        const res = await fetch("/turn-credentials");
        if (!res.ok) return null;
        return await res.json().catch(() => null);
    } catch { return null; }
}

function setupIceServers(turnData) {
    globalIceServers = [{ urls: "stun:stun.l.google.com:19302" }];
    if (turnData?.username && turnData?.password) {
        globalIceServers.push(
            { urls: "turn:turn.noremac.dev:3478?transport=udp", username: turnData.username, credential: turnData.password },
            { urls: "turns:turn.noremac.dev:443?transport=tcp", username: turnData.username, credential: turnData.password },
        );
    }
}

async function setupLocalMedia() {
    try {
        localStream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true });
        return;
    } catch { }
    let audio = null, video = null;
    try { audio = await navigator.mediaDevices.getUserMedia({ audio: true }); } catch { }
    try { video = await navigator.mediaDevices.getUserMedia({ video: true }); } catch { }
    localStream = new MediaStream([
        ...(video ? video.getVideoTracks() : []),
        ...(audio ? audio.getAudioTracks() : []),
    ]);
    if (localStream.getTracks().length === 0) {
        const warn = document.getElementById("no-media-warning");
        if (warn) warn.textContent = "⚠️ Joined without camera/mic (permissions or devices).";
    }
}

function showLocalVideo() {
    const video = Object.assign(document.createElement("video"), {
        id: "local-video", srcObject: localStream, autoplay: true, playsInline: true, muted: true,
    });
    video.classList.add("local-video");
    document.getElementById("videos").appendChild(video);
}

/* ----------------------- Perfect Negotiation bits ----------------------- */
let makingOffer = false;
const polite = true;

async function maybeMakeOffer() {
    if (makingOffer || pc.signalingState !== "stable") return;
    makingOffer = true;
    try {
        // Let the browser create & set the right offer in one step
        await pc.setLocalDescription(); // implicit createOffer + setLocalDescription
        const ld = pc.localDescription;
        ws.send(JSON.stringify({
            type: "offer",
            offer: { type: ld.type, sdp: ld.sdp },
            name: myName
        }));
    } catch (e) {
        Logger.error("[SFU] offer flow failed", e);
    } finally {
        makingOffer = false;
    }
}

/* ----------------------------------------------------------------------- */

async function connectSFUSocket() {
    const url =
        (location.protocol === "https:" ? "wss://" : "ws://") +
        location.host +
        `/ws/sfu?room=${encodeURIComponent(ROOM)}&id=${encodeURIComponent(myUUID)}`;
    ws = new WebSocket(url);

    ws.onopen = async () => {
        Logger.info("[SFU] WS open", { room: ROOM, id: myUUID });
        pc = new RTCPeerConnection({ iceServers: globalIceServers });

        try {
            pc.addTransceiver('audio', { direction: 'recvonly' });
            pc.addTransceiver('video', { direction: 'recvonly' });
        } catch { }

        pc.onconnectionstatechange = () => {
            Logger.info("PC state", pc.connectionState);
            if (pc.connectionState === "failed" || pc.connectionState === "closed") teardownPeer();
        };

        // Add local tracks (triggers negotiationneeded)
        if (localStream) for (const t of localStream.getTracks()) pc.addTrack(t, localStream);

        let negScheduled = false;
        pc.onnegotiationneeded = () => {
            if (negScheduled) return;
            negScheduled = true;
            queueMicrotask(async () => {
                negScheduled = false;
                await maybeMakeOffer();
            });
        };

        // Assume you know your own id used in the SFU URL (?id=...) as `selfId`
        const selfId = window.myPeerId; // set this when you connect
        const remoteTrackMap = {};      // track.id -> { element, kind, ownerId }
        const remoteByOwner = new Map(); // ownerId -> { videoEl?, audioEls:Set }

        pc.ontrack = ({ track, streams }) => {
            const stream = streams?.[0] || new MediaStream([track]);

            console.log(
                "[ontrack]",
                "kind:", track.kind,
                "id:", track.id,
                "stream.id:", stream.id
            );

            if (track.kind === "video") {
                if (stream.id === myUUID) return;

                const el = Object.assign(document.createElement("video"), {
                    autoplay: true, playsInline: true, muted: true, srcObject: stream,
                });
                el.dataset.ownerId = stream.id;
                el.addEventListener("loadedmetadata", () => el.play().catch(() => { }));

                const entry = ensureOverlay(track.id, stream.id, el);

                // if metadata arrived before video, render it now
                const pending = latestBoxes.get(track.id);
                if (pending) {
                    entry.detW = pending.w; entry.detH = pending.h;
                    drawBoxes(entry, pending.w, pending.h, pending.boxes);
                    latestBoxes.delete(track.id);
                }

                console.log("[ontrack] attached video+overlay for stream", stream.id);
            }
            else if (track.kind === "audio") {
                const el = document.createElement("audio");
                Object.assign(el, { autoplay: true, srcObject: stream });
                document.body.appendChild(el);
                console.log("[ontrack] attached audio element for stream", stream.id);
            }
        };





        pc.onicecandidate = (e) => {
            if (!pc.localDescription) return;
            if (e.candidate) {
                ws.send(JSON.stringify({ type: "candidate", candidate: e.candidate }));
            } else {
                ws.send(JSON.stringify({ type: "candidate", candidate: null }));
            }
        };

        let iceRestartTimer;
        pc.oniceconnectionstatechange = () => {
            const s = pc.iceConnectionState;
            if (s === 'disconnected') {
                clearTimeout(iceRestartTimer);
                iceRestartTimer = setTimeout(async () => {
                    if (pc.signalingState === 'stable') {
                        try {
                            await pc.setLocalDescription(await pc.createOffer({ iceRestart: true }));
                            const ld = pc.localDescription;
                            ws.send(JSON.stringify({ type: 'offer', offer: { type: ld.type, sdp: ld.sdp }, name: myName }));
                        } catch (e) { /* ignore */ }
                    }
                }, 2000);
            } else if (s === 'failed' || s === 'connected' || s === 'completed') {
                clearTimeout(iceRestartTimer);
            }
        };

    };

    ws.onmessage = async ({ data }) => {
        const msg = JSON.parse(data);

        if (msg.type === "offer") {
            const offerCollision = makingOffer || pc.signalingState !== "stable";
            const ignoreOffer = !polite && offerCollision;
            if (ignoreOffer) return;
            try {
                if (offerCollision) {
                    remoteDescSet = false;
                    await pc.setLocalDescription({ type: "rollback" });
                }
                await pc.setRemoteDescription(msg.offer);
                await pc.setLocalDescription(await pc.createAnswer());
                const ld = pc.localDescription;
                ws.send(JSON.stringify({ type: "answer", answer: { type: ld.type, sdp: ld.sdp } }));
                remoteDescSet = true;
                for (const c of candidateQueue) { try { await pc.addIceCandidate(c); } catch { } }
                candidateQueue = [];
            } catch (e) {
                Logger.error("[SFU] handle remote offer failed", e);
            }
            return;
        }

        if (msg.type === "answer") {
            try {
                await pc.setRemoteDescription(msg.answer);
                remoteDescSet = true;
                for (const c of candidateQueue) { try { await pc.addIceCandidate(c); } catch { } }
                candidateQueue = [];
            } catch (e) {
                Logger.error("[SFU] setRemoteDescription(answer) failed", e);
            }
            return;
        }

        if (msg.type === "candidate") {
            if (!msg.candidate) {
                try { await pc.addIceCandidate(null); } catch { }
                return;
            }
            if (!remoteDescSet || !pc.remoteDescription) {
                if (candidateQueue.length < MAX_CAND_QUEUE) {
                    candidateQueue.push(msg.candidate);
                } else {
                    // optional: log once or drop silently
                    Logger.warn("[SFU] candidateQueue cap reached; dropping candidate");
                }
            } else {
                try { await pc.addIceCandidate(msg.candidate); } catch (e) { Logger.error("[SFU] addIceCandidate failed", e); }
            }

            return;
        }

        if (msg.type === "peer-left" && msg.from) {
            const pubID = msg.from;
            // Remove all elements for this publisher immediately
            const set = remoteByStream.get(pubID);
            if (set) {
                for (const el of set) {
                    try { el.srcObject = null; } catch { }
                    el.remove();
                }
                remoteByStream.delete(pubID);
            }
            // Also purge any track-indexed leftovers just in case
            for (const [tid, rec] of Object.entries(remoteTrackMap)) {
                if (rec.pubID === pubID) {
                    try { rec.element.srcObject = null; } catch { }
                    rec.element.remove();
                    delete remoteTrackMap[tid];
                }
            }
            return;
        }
        if (msg.type === "cv/boxes") {
            const { from, trackId, w: detW, h: detH, boxes } = msg;
            if (from === myUUID) return;

            const fistBoxes = (boxes || []).filter(b => (b.type || "").toLowerCase() === "fist");
            const candidates = fistBoxes.length ? fistBoxes : (boxes || []);
            if (!candidates.length || !detW || !detH) return;

            let best = candidates[0], bestA = best.w * best.h;
            for (let i = 1; i < candidates.length; i++) {
                const a = candidates[i].w * candidates[i].h;
                if (a > bestA) { best = candidates[i]; bestA = a; }
            }

            // centroid (mirror x if you’re mirroring the video)
            let cx = best.x + best.w / 2;
            let cy = best.y + best.h / 2;
            cx = detW - cx; // mirror

            const nx = Math.max(0, Math.min(1, cx / detW));
            const ny = Math.max(0, Math.min(1, cy / detH));

            // --- SIZE: area-based fraction (better than width-only) ---
            const areaFrac = (best.w * best.h) / (detW * detH);              // 0..1
            const relSize = Math.sqrt(Math.max(1e-6, Math.min(1, areaFrac))); // 0..1

            updateCursor(from, nx, ny, relSize);

            // Convert to viewport px for hit-testing/dragging
            const vw = window.innerWidth, vh = window.innerHeight;
            const vx = nx * vw;
            const vy = ny * vh;


            // Use the SAME relSizeRaw you passed to updateCursor (area-based recommended)
            handleDepthClickAndDrag(from, vx, vy, relSize);


            return;
        }
    };

    ws.onerror = (e) => Logger.error("[SFU] WS error", e);
    ws.onclose = () => teardownPeer();
}

function teardownPeer() {
    try { ws?.readyState === WebSocket.OPEN && ws.send(JSON.stringify({ type: "leave" })); } catch { }
    try { ws?.close(); } catch { }
    try { pc?.getSenders().forEach(s => s.track && s.track.stop()); } catch { }
    try { pc?.close(); } catch { }
    for (const k in remoteTrackMap) { remoteTrackMap[k]?.element?.remove(); delete remoteTrackMap[k]; }
}

/* ---------------------- helpers & test controls ---------------------- */

function generateUUID() {
    if (crypto?.randomUUID) return crypto.randomUUID();
    const hex = [], rnds = new Uint8Array(16); crypto.getRandomValues(rnds);
    rnds[6] = (rnds[6] & 0x0f) | 0x40; rnds[8] = (rnds[8] & 0x3f) | 0x80;
    for (let i = 0; i < 16; i++) hex.push(rnds[i].toString(16).padStart(2, "0"));
    return [hex.slice(0, 4).join(""), hex.slice(4, 6).join(""), hex.slice(6, 8).join(""), hex.slice(8, 10).join(""), hex.slice(10, 16).join("")].join("-");
}

async function testCamera() {
    try {
        const s = await navigator.mediaDevices.getUserMedia({ video: true });
        const v = document.getElementById("preview-video"); v.style.display = "block"; v.srcObject = s;
    } catch (e) { Logger.error("preview camera failed", e); }
}

async function testMic() {
    try {
        const s = await navigator.mediaDevices.getUserMedia({ audio: true });
        s.getTracks().forEach(t => t.stop());
        document.getElementById("mic-status").textContent = "🎤 Mic OK";
    } catch { document.getElementById("mic-status").textContent = "❌ Mic not available"; }
}


function ensureOverlay(trackId, streamId, videoEl) {
    let entry = overlays.get(trackId);
    if (entry) return entry;

    // wrapper
    const wrapper = document.createElement("div");
    wrapper.className = "remote-wrapper"; // position: relative
    wrapper.dataset.ownerId = streamId;

    // canvas overlay
    const canvas = document.createElement("canvas");
    canvas.id = `ov-${trackId}`;
    canvas.className = "remote-overlay"; // position:absolute; inset:0; pointer-events:none;

    // move video into wrapper
    videoEl.classList.add("remote-video"); // width:100%; height:auto (or your layout)
    wrapper.appendChild(videoEl);
    wrapper.appendChild(canvas);

    const root = document.getElementById("videos");
    root.appendChild(wrapper);

    const ctx = canvas.getContext("2d");
    entry = { video: videoEl, canvas, ctx, detW: 0, detH: 0 };
    overlays.set(trackId, entry);

    // Keep canvas matched to rendered video size
    const syncSize = () => {
        const r = videoEl.getBoundingClientRect();
        if (!r.width || !r.height) return;
        // only resize when needed to avoid clearing context every frame
        if (canvas.width !== (r.width | 0) || canvas.height !== (r.height | 0)) {
            canvas.width = r.width | 0;
            canvas.height = r.height | 0;
        }
    };

    // On metadata (we know the intrinsic size) and while resizing
    videoEl.addEventListener("loadedmetadata", syncSize);
    // Use ResizeObserver for layout changes
    const ro = new ResizeObserver(syncSize);
    ro.observe(videoEl);
    // Store to entry if you want to disconnect later:
    entry._ro = ro;
    entry._syncSize = syncSize;

    return entry;
}

function drawBoxes(entry, detW, detH, boxes) {
    if (!entry) return;
    const { canvas, ctx } = entry;
    entry._syncSize?.();

    const vw = canvas.width, vh = canvas.height;
    if (!vw || !vh || !detW || !detH) return;

    const sx = vw / detW;
    const sy = vh / detH;

    ctx.clearRect(0, 0, vw, vh);
    ctx.lineWidth = 2;

    for (const b of boxes || []) {
        // choose color by type
        if (b.type === "palm") {
            ctx.strokeStyle = "rgba(0,200,255,0.95)";   // cyan for palm
        } else if (b.type === "fist") {
            ctx.strokeStyle = "rgba(255,80,80,0.95)";   // red for fist
        } else {
            ctx.strokeStyle = "rgba(0,255,0,0.95)";     // fallback
        }

        const x = b.x * sx, y = b.y * sy, w = b.w * sx, h = b.h * sy;
        ctx.strokeRect(x, y, w, h);
    }
}
function ema(prev, next, alpha = 0.35) {
    if (prev == null || Number.isNaN(prev)) return next;
    return prev + alpha * (next - prev);
}

function ensureCursor(pubID) {
    let c = cursors.get(pubID);
    if (c) return c;
    const el = document.createElement('div');
    el.className = 'cv-cursor';
    document.body.appendChild(el);
    c = {
        el, x: null, y: null, r: null,
        baseSize: null,
        isNear: false,          // depth state
        anchoredEl: null,       // element we’re dragging (or null)
        anchorOffsetX: 0,
        anchorOffsetY: 0,
        lastClickTs: 0
    };
    cursors.set(pubID, c);
    return c;
}

function getTopBar() {
    return document.getElementById('top-draggable');
}

function isCursorOverEl(el, x, y) {
    if (!el) return false;
    const r = el.getBoundingClientRect();
    return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
}

function updateCursor(pubID, nx, ny, relSizeRaw) {
    const c = ensureCursor(pubID);
    const vw = window.innerWidth, vh = window.innerHeight;

    // position targets
    const targetX = nx * vw;
    const targetY = ny * vh;

    // --- Auto-calibration of "neutral" fist size ---
    // Slowly adapt to the user's typical size over time.
    const BASELINE_ALPHA = 0.05;
    c.baseSize = ema(c.baseSize, relSizeRaw, (c.baseSize == null) ? 1.0 : BASELINE_ALPHA);

    // Current depth relative to baseline ( >1 when closer, <1 when farther )
    let depth = relSizeRaw / Math.max(1e-6, c.baseSize);

    // Soften extremes (optional)
    depth = Math.sqrt(depth);

    // INVERT: closer (larger depth) -> smaller circle
    let inv = 1 / Math.max(0.000001, depth);

    // Clamp the inverted factor so it stays reasonable
    const INV_MIN = 0.55;                       // don't shrink too much up close
    const INV_MAX = 1.8;                        // don't grow too huge when far
    inv = Math.max(INV_MIN, Math.min(INV_MAX, inv));

    // Radius mapping (same base as before)
    const BASE_RADIUS = Math.min(vw, vh) * 0.05;
    const CLAMP_MIN = 16;
    const CLAMP_MAX = Math.min(vw, vh) * 0.14;

    let targetR = BASE_RADIUS * inv;
    targetR = Math.max(CLAMP_MIN, Math.min(CLAMP_MAX, targetR));

    // Smooth (you can keep radius a bit “heavier” to reduce pulsing)
    c.x = ema(c.x, targetX, 0.35);
    c.y = ema(c.y, targetY, 0.35);
    c.r = ema(c.r, targetR, 0.25);

    // Apply
    c.el.style.width = `${2 * c.r}px`;
    c.el.style.height = `${2 * c.r}px`;
    c.el.style.transform = `translate(${(c.x - c.r) | 0}px, ${(c.y - c.r) | 0}px)`;
}

function handleDepthClickAndDrag(pubID, viewportX, viewportY, relSizeRaw) {
    const c = ensureCursor(pubID);
    const vw = window.innerWidth;
    const vh = window.innerHeight;

    // --- Baseline of hand size (slow EMA) ---
    const BASELINE_ALPHA = 0.05;
    c.baseSize = ema(c.baseSize, relSizeRaw, (c.baseSize == null) ? 1.0 : BASELINE_ALPHA);

    // Depth factor: >1 means closer than baseline, <1 farther
    let depth = relSizeRaw / Math.max(1e-6, c.baseSize);

    // Hysteresis thresholds to stabilize click gesture
    const NEAR_T = 1.25;  // enter near when depth rises above this
    const FAR_T = 1.10;  // leave near when depth drops below this

    let wasNear = c.isNear;
    if (!wasNear && depth >= NEAR_T) c.isNear = true;
    else if (wasNear && depth <= FAR_T) c.isNear = false;

    // Edge: far -> near = CLICK
    if (!wasNear && c.isNear) {
        const now = performance.now();
        if (now - c.lastClickTs > 220) { // debounce ~220ms
            c.lastClickTs = now;

            const bar = getTopBar();

            // If already anchored, unanchor
            if (c.anchoredEl) {
                c.anchoredEl.classList.remove('anchored');
                c.anchoredEl = null;
                return;
            }

            // Otherwise, try to anchor if cursor is over the top bar
            if (isCursorOverEl(bar, viewportX, viewportY)) {
                // record offset so movement is relative to grab point
                const r = bar.getBoundingClientRect();
                c.anchorOffsetX = viewportX - r.left;
                c.anchorOffsetY = viewportY - r.top;
                c.anchoredEl = bar;
                c.anchoredEl.classList.add('anchored');


            }
        }
    }

    // If anchored, drag the element (fixed top; we only change left)
    if (c.anchoredEl) {
        const r = c.anchoredEl.getBoundingClientRect();
        const left = Math.max(0, Math.min(vw - r.width, viewportX - c.anchorOffsetX));
        const top = Math.max(0, Math.min(vh - r.height, viewportY - c.anchorOffsetY));
        c.anchoredEl.style.left = `${left | 0}px`;
        c.anchoredEl.style.top = `${top | 0}px`;
    }
}
