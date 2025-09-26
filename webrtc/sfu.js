// sfu.js (with perfect negotiation)

const ROOM = new URLSearchParams(location.search).get("room") || "default";

let myUUID = generateUUID();
let myName = "";
let ws, pc, localStream;
let globalIceServers = [];
let candidateQueue = [];
let remoteDescSet = false;
const remoteTrackMap = {};

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

        pc.onconnectionstatechange = () => {
            Logger.info("PC state", pc.connectionState);
            if (pc.connectionState === "failed" || pc.connectionState === "closed") teardownPeer();
        };

        // Add local tracks (triggers negotiationneeded)
        if (localStream) for (const t of localStream.getTracks()) pc.addTrack(t, localStream);

        pc.onnegotiationneeded = () => { maybeMakeOffer(); };

        pc.ontrack = ({ track, streams }) => {
            const stream = streams?.[0] || new MediaStream([track]);
            if (!remoteTrackMap[track.id]) {
                if (track.kind === "video") {
                    const el = Object.assign(document.createElement("video"), {
                        id: `remote-video-${track.id}`, srcObject: stream, autoplay: true, playsInline: true,
                    });
                    el.classList.add("remote-video");
                    document.getElementById("videos").appendChild(el);
                    remoteTrackMap[track.id] = { element: el, kind: "video" };
                } else {
                    const el = Object.assign(document.createElement("audio"), { autoplay: true, srcObject: stream });
                    document.body.appendChild(el);
                    remoteTrackMap[track.id] = { element: el, kind: "audio" };
                }
            }
            track.onended = () => {
                const item = remoteTrackMap[track.id];
                if (item?.element) item.element.remove();
                delete remoteTrackMap[track.id];
            };
        };

        pc.onicecandidate = (e) => { if (e.candidate) ws.send(JSON.stringify({ type: "candidate", candidate: e.candidate })); };
    };

    ws.onmessage = async ({ data }) => {
        const msg = JSON.parse(data);

        if (msg.type === "offer") {
            const offerCollision = makingOffer || pc.signalingState !== "stable";
            const ignoreOffer = !polite && offerCollision;
            if (ignoreOffer) return;

            try {
                if (offerCollision) await pc.setLocalDescription({ type: "rollback" });
                await pc.setRemoteDescription(msg.offer);
                await pc.setLocalDescription(await pc.createAnswer());

                const ld = pc.localDescription;
                ws.send(JSON.stringify({
                    type: "answer",
                    answer: { type: ld.type, sdp: ld.sdp }
                }));

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
            if (!remoteDescSet || !pc.remoteDescription) {
                candidateQueue.push(msg.candidate);
            } else {
                try { await pc.addIceCandidate(msg.candidate); } catch (e) { Logger.error("[SFU] addIceCandidate failed", e); }
            }
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
