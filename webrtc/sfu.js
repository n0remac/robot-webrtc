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
            const { from, trackId, w, h, boxes } = msg;

            // optional: skip our own metadata
            if (from === myUUID) return;

            // 1) try to get an existing overlay
            let entry = overlays.get(trackId);

            // 2) if missing, try to find the video and create the overlay now
            if (!entry) {
                const vid = document.querySelector(`video[data-owner-id="${from}"]`);
                if (vid) {
                    entry = ensureOverlay(trackId, from, vid);
                }
            }

            // 3) if still missing, queue once until ontrack runs
            if (!entry) {
                console.warn("[cv/boxes] overlay not ready, queuing", { trackId, from });
                latestBoxes.set(trackId, { w, h, boxes, from });
                return;
            }

            // 4) draw
            entry.detW = w; entry.detH = h;
            drawBoxes(entry, w, h, boxes);
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
    const { video, canvas, ctx } = entry;
    // ensure canvas tracks current on-screen size
    entry._syncSize?.();

    const vw = canvas.width, vh = canvas.height;
    if (!vw || !vh || !detW || !detH) return;

    const sx = vw / detW;
    const sy = vh / detH;

    // clear and draw
    ctx.clearRect(0, 0, vw, vh);
    ctx.lineWidth = 2;
    ctx.strokeStyle = "rgba(0,255,0,0.95)";
    ctx.beginPath();
    for (const b of boxes || []) {
        const x = b.x * sx, y = b.y * sy, w = b.w * sx, h = b.h * sy;
        ctx.strokeRect(x, y, w, h);
    }
}
