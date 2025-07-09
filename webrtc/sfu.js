// sfu.js

const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";
const ROOM = new URLSearchParams(location.search).get("room") || "default";

let myUUID = generateUUID();
let myName;
let ws;
let pc;
let localStream;
let globalIceServers = [];
let candidateQueue = [];
let remoteDescSet = false;

// Map: track.id => { element, kind }
const remoteTrackMap = {};

window.addEventListener('beforeunload', () => {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({
            type: 'leave',
            from: myUUID,
            room: ROOM
        }));
        ws.close();
    }
});

document.addEventListener("DOMContentLoaded", () => {
    document.getElementById('test-camera').addEventListener('click', testCamera);
    document.getElementById('test-mic').addEventListener('click', testMic);
    document.getElementById('join-btn').addEventListener('click', joinSFUSession);
});

async function joinSFUSession() {
    myName = document.getElementById('name').value;
    if (!myName) return alert('Please enter your name');
    myUUID = generateUUID();
    Logger.info('SFU join click', { uuid: myUUID, name: myName });

    document.getElementById('join-screen').style.display = 'none';
    document.getElementById('participant-view').style.display = 'block';

    const turnData = await fetchTurnCredentials();
    setupIceServers(turnData);
    await setupLocalMedia();
    showLocalVideo();
    await connectSFUWebSocket();
}

async function fetchTurnCredentials() {
    try {
        const res = await fetch('/turn-credentials');
        return res.ok ? res.json() : null;
    } catch (e) {
        Logger.error('TURN fetch failed', e);
        return null;
    }
}

function setupIceServers(turnData) {
    globalIceServers = [
        { urls: 'stun:stun.l.google.com:19302' },
    ];
    if (turnData?.username && turnData?.password) {
        globalIceServers.push({ urls: 'turn:turn.noremac.dev:3478?transport=udp', username: turnData.username, credential: turnData.password });
        globalIceServers.push({ urls: 'turns:turn.noremac.dev:443?transport=tcp', username: turnData.username, credential: turnData.password });
    }
    Logger.info('SFU ICE list built', { count: globalIceServers.length });
}

async function setupLocalMedia() {
    let videoStream = null;
    let audioStream = null;

    try {
        videoStream = await navigator.mediaDevices.getUserMedia({ video: true });
        Logger.info('video gUM success');
    } catch (err) {
        Logger.warn('video unavailable, continuing without camera', err);
    }

    try {
        audioStream = await navigator.mediaDevices.getUserMedia({ audio: true });
        Logger.info('audio gUM success');
    } catch (err) {
        Logger.warn('audio unavailable, continuing without mic', err);
    }

    const tracks = [
        ...(videoStream ? videoStream.getVideoTracks() : []),
        ...(audioStream ? audioStream.getAudioTracks() : [])
    ];
    localStream = new MediaStream(tracks);

    if (tracks.length === 0) {
        const warn = document.getElementById('no-media-warning');
        if (warn) warn.textContent = '⚠️ No camera or mic available; joining with media disabled.';
    }
}

function showLocalVideo() {
    const video = Object.assign(document.createElement('video'), {
        id: 'local-video',
        srcObject: localStream,
        autoplay: true,
        playsInline: true,
        muted: true,
    });
    video.classList.add('local-video');
    document.getElementById('videos').appendChild(video);
    Logger.info('local video added');
}

async function connectSFUWebSocket() {
    ws = new WebSocket(
        (location.protocol === 'https:' ? 'wss://' : 'ws://') +
        location.host +
        '/ws/sfu?id=' + encodeURIComponent(myUUID)
    );

    ws.onopen = async () => {
        Logger.info('[SFU] WebSocket open');

        // Create PeerConnection and wire up handlers
        pc = new RTCPeerConnection({ iceServers: globalIceServers });

        pc.addTransceiver("video", { direction: "sendrecv" });
        pc.addTransceiver("audio", { direction: "sendrecv" });

        // Add our local tracks to the PC
        if (localStream) {
            localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
        }

        pc.onsignalingstatechange = () => console.log("SignalingState:", pc.signalingState);
        pc.oniceconnectionstatechange = () => console.log("ICEConnectionState:", pc.iceConnectionState);
        
        // --- Remote track handling and cleanup ---
        pc.ontrack = ({ track, streams }) => {
            let stream = streams[0] || new MediaStream([track]);
            console.log('ontrack fired:', track, streams, stream);
            // Add to UI if not already present
            if (!remoteTrackMap[track.id]) {
                if (track.kind === "video") {
                    const video = Object.assign(document.createElement('video'), {
                        id: `remote-video-${track.id}`,
                        srcObject: stream,
                        autoplay: true,
                        playsInline: true,
                        muted: false
                    });
                    video.classList.add('remote-video');
                    document.getElementById('videos').appendChild(video);
                    Logger.info('[SFU] remote video added', { track: track.id });
                    remoteTrackMap[track.id] = { element: video, kind: "video" };
                } else if (track.kind === "audio") {
                    const audio = Object.assign(document.createElement('audio'), {
                        id: `remote-audio-${track.id}`,
                        srcObject: stream,
                        autoplay: true,
                    });
                    document.body.appendChild(audio);
                    Logger.info('[SFU] remote audio added', { track: track.id });
                    remoteTrackMap[track.id] = { element: audio, kind: "audio" };
                }
            }

            // Remove UI when track ends
            track.onended = () => {
                const item = remoteTrackMap[track.id];
                if (item && item.element) {
                    item.element.remove();
                    Logger.info(`[SFU] removed ${item.kind} for ended track`, { track: track.id });
                }
                delete remoteTrackMap[track.id];
            };
        };

        // ICE candidate gathering (local → SFU)
        pc.onicecandidate = (e) => {
            if (e.candidate) {
                ws.send(JSON.stringify({
                    type: 'candidate',
                    candidate: e.candidate
                }));
            }
        };

        // --- Begin signaling: create offer and send to server ---
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);

        ws.send(JSON.stringify({
            type: 'offer',
            offer: pc.localDescription,
            name: myName,
            id: myUUID
        }));
    };

    ws.onmessage = async ({ data }) => {
        const msg = JSON.parse(data);
        console.log('WS MESSAGE', msg.type, msg);
        switch (msg.type) {
            case "answer":
                console.log('Setting remote description (answer)', msg.answer);
                await pc.setRemoteDescription(msg.answer);
                remoteDescSet = true;
                for (const c of candidateQueue) {
                    try { await pc.addIceCandidate(c); } catch (e) { Logger.error('[SFU] candidate flush failed', e); }
                }
                candidateQueue = [];
                Logger.info('[SFU] Set remote description (answer)');
                break;
            case "candidate":
                console.log('Received candidate', msg.candidate);
                if (!remoteDescSet || !pc.remoteDescription) {
                    candidateQueue.push(msg.candidate);
                } else {
                    try {
                        await pc.addIceCandidate(msg.candidate);
                        Logger.info('[SFU] Added ICE candidate');
                    } catch (e) {
                        Logger.error('[SFU] Failed to add ICE candidate', e);
                    }
                }
                break;
            case "offer":
                console.log('Received offer', msg.offer);
                try {
                    if (pc.signalingState !== "stable") {
                        await pc.setLocalDescription({ type: "rollback" });
                    }
                    await pc.setRemoteDescription(msg.offer);
                    for (const c of candidateQueue) {
                        try { await pc.addIceCandidate(c); } catch (e) { Logger.error('[SFU] candidate flush failed', e); }
                    }
                    candidateQueue = [];
                    const answer = await pc.createAnswer();
                    await pc.setLocalDescription(answer);
                    ws.send(JSON.stringify({
                        type: 'answer',
                        answer: pc.localDescription,
                    }));
                    Logger.info('[SFU] Renegotiated and sent answer');
                } catch (e) {
                    Logger.error('[SFU] Failed to handle offer', e);
                }
                break;
        }
    };


    ws.onerror = e => Logger.error('[SFU] WebSocket error', e);
    ws.onclose = (e) => {
        Logger.warn('[SFU] WebSocket closed', { code: e.code, reason: e.reason });
        // Optionally: add reconnect logic
    };
}

function generateUUID() {
    // If available, use the browser's native randomUUID
    if (window.crypto && window.crypto.randomUUID) {
        return window.crypto.randomUUID();
    }
    // Otherwise, polyfill
    const hex = [];
    const rnds = new Uint8Array(16);
    window.crypto.getRandomValues(rnds);
    rnds[6] = (rnds[6] & 0x0f) | 0x40; // version 4
    rnds[8] = (rnds[8] & 0x3f) | 0x80; // variant 10xx

    for (let i = 0; i < 16; i++) {
        hex.push(rnds[i].toString(16).padStart(2, '0'));
    }
    return [
        hex.slice(0, 4).join(''),
        hex.slice(4, 6).join(''),
        hex.slice(6, 8).join(''),
        hex.slice(8, 10).join(''),
        hex.slice(10, 16).join('')
    ].join('-');
}
