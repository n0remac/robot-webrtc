const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";

const wsUrl =
  (location.protocol === "https:" ? "wss://" : "ws://")
  + location.host
  + "/ws/video";

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

Logger.attachSocket(`${wsProtocol}://${domainName}/ws/logs`);
document.addEventListener("DOMContentLoaded", () => {
    document.getElementById('join-btn').addEventListener('click', joinSession);
    document.getElementById('mute-btn').addEventListener('click', toggleMute);
    document.getElementById('video-btn').addEventListener('click', toggleVideo);
});
let myUUID = null;
let myName;
let localStream;
let isMuted = false;
let isVideoStopped = false;
let ws;
let globalIceServers = [];

const peers = {};
const pendingCandidates = {};
const pendingOffers = {};
const messageQueue = [];
let wsReady = false;
const ICE_TIMEOUT_MS = 10000;
const MAX_RETRIES = 5;
const retryCounts = {}; 

window.addEventListener('beforeunload', () => {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'leave', uuid: myUUID }));
        Logger.info('sent leave on unload', { uuid: myUUID });
    }
});

async function joinSession() {
    myName = document.getElementById('name').value;
    if (!myName) return alert('Please enter your name');
    myUUID = generateUUID();
    Logger.info('join click', { uuid: myUUID, name: myName });

    document.getElementById('join-screen').style.display = 'none';
    document.getElementById('participant-view').style.display = 'block';

    const turnData = await fetchTurnCredentials();
    setupIceServers(turnData);
    await setupLocalMedia();
    showLocalVideo();
    await connectWebSocket();
    flushBufferedMessages();
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
    Logger.info('ICE list built', { count: globalIceServers.length });
}

async function setupLocalMedia() {
    try {
        localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
        Logger.info('gUM success');
    } catch (err) {
        Logger.error('getUserMedia failed', err);
        throw err;
    }
}

function showLocalVideo() {
    const video = Object.assign(document.createElement('video'), {
        id: 'local-video',
        srcObject: localStream,
        autoplay: true,
        playsInline: true,
        muted: false,
    });
    if (Object.keys(peers).length === 0) {
        video.classList.add('remote-video');
    } else {
        video.classList.add('local-video');
    }
    document.getElementById('videos').appendChild(video);
    Logger.info('local video added');
}

async function connectWebSocket() {
    ws = new WebSocket(wsUrl);

    ws.onmessage = async ({ data }) => {
        const msg = JSON.parse(data);
        if (!wsReady) return messageQueue.push(msg);
        await handleSignalingMessage(msg);
    };

    ws.onopen = () => {
        wsReady = true;
        Logger.info('WebSocket open');
        ws.send(JSON.stringify({ type: 'join', uuid: myUUID }));
    };
    ws.onerror = e => Logger.error('WebSocket error', e);
    ws.onclose = e => Logger.warn('WebSocket closed', e);
}

function flushBufferedMessages() {
    while (messageQueue.length > 0) {
        handleSignalingMessage(messageQueue.shift());
    }
}

async function handleSignalingMessage(data) {
    Logger.info('WS msg', data);
    switch (data.type) {
        case 'join':
            if (data.uuid !== myUUID) await createOffer(data.uuid);
            break;
        case 'offer':
            if (!peers[data.uuid]) pendingOffers[data.uuid] = data.offer;
            await createAnswer(data.uuid, data.offer);
            break;
        case 'answer':
            await setRemoteDescriptionSafely(data.uuid, data.answer);
            break;
        case 'candidate':
            bufferOrApplyCandidate(data.uuid, data.candidate);
            break;
        case 'leave':
            handleUserDisconnect(data.uuid);
            break;
    }
}

function bufferOrApplyCandidate(uuid, candidate) {
    if (!peers[uuid]) {
        (pendingCandidates[uuid] ||= []).push(candidate);
    } else if (peers[uuid].remoteDescription) {
        peers[uuid].addIceCandidate(new RTCIceCandidate(candidate));
    } else {
        (peers[uuid].queuedCandidates ||= []).push(candidate);
    }
}

async function setRemoteDescriptionSafely(uuid, answer) {
    if (!peers[uuid]) return Logger.warn('Answer for unknown peer', { uuid });
    await peers[uuid].setRemoteDescription(new RTCSessionDescription(answer));
    Logger.info('remote SDP set', { uuid });
    if (peers[uuid].queuedCandidates) {
        for (const c of peers[uuid].queuedCandidates) {
            await peers[uuid].addIceCandidate(new RTCIceCandidate(c));
        }
        peers[uuid].queuedCandidates = [];
    }
}

async function createOffer(uuid) {
    if ((retryCounts[uuid] || 0) >= MAX_RETRIES) {
        Logger.warn('Max retries reached, will not reconnect', { peer: uuid });
        return;
    }
    retryCounts[uuid] = (retryCounts[uuid] || 0) + 1;
    Logger.info('createOffer', { to: uuid, attempt: retryCounts[uuid] });

    let pc = peers[uuid];
    if (!pc) {
        pc = new RTCPeerConnection({ iceServers: globalIceServers });
        wireUpPeer(pc, uuid);
        peers[uuid] = pc;
    }

    if (pendingCandidates[uuid]) {
        for (const c of pendingCandidates[uuid]) {
            await pc.addIceCandidate(new RTCIceCandidate(c));
        }
        delete pendingCandidates[uuid];
    }

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    ws.send(JSON.stringify({ type: 'offer', uuid, offer }));
    Logger.info('offer sent', { to: uuid });
}

async function createAnswer(uuid, offer) {
    Logger.info('createAnswer', { from: uuid });
    let pc = peers[uuid];
    if (!pc) {
        pc = new RTCPeerConnection({ iceServers: globalIceServers });
        wireUpPeer(pc, uuid);
        peers[uuid] = pc;
    }

    await pc.setRemoteDescription(new RTCSessionDescription(offer));

    if (pendingCandidates[uuid]) {
        for (const c of pendingCandidates[uuid]) {
            await pc.addIceCandidate(new RTCIceCandidate(c));
        }
        delete pendingCandidates[uuid];
    }

    if (pc.queuedCandidates?.length) {
        for (const c of pc.queuedCandidates) {
            await pc.addIceCandidate(new RTCIceCandidate(c));
        }
        pc.queuedCandidates = [];
    }

    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    ws.send(JSON.stringify({ type: 'answer', uuid, answer }));
    Logger.info('answer sent', { to: uuid });
}

function wireUpPeer(pc, uuid) {
    if (pc._tracksAdded) return;

    pc.ontrack = (e) => {
        Logger.info('ontrack', { peer: uuid });
        addRemoteStream(e.streams[0], uuid);
    };

    pc.onicecandidate = (e) => {
        if (e.candidate) {
            ws.send(JSON.stringify({ type: 'candidate', uuid, candidate: e.candidate }));
            Logger.info('sent candidate', { to: uuid });
        }
    };

    const timeout = setTimeout(() => {
        if (pc.iceConnectionState !== 'connected' && pc.iceConnectionState !== 'completed') {
            Logger.warn('ICE timeout, attempting reconnect', { peer: uuid });
            handleUserDisconnect(uuid);
            createOffer(uuid);
        }
    }, ICE_TIMEOUT_MS);

    pc.oniceconnectionstatechange = () => {
        Logger.info('ICE state', { peer: uuid, state: pc.iceConnectionState });
        if (['failed', 'disconnected', 'closed'].includes(pc.iceConnectionState)) {
            clearTimeout(timeout);
            handleUserDisconnect(uuid);
        }
        if (['connected', 'completed'].includes(pc.iceConnectionState)) {
            clearTimeout(timeout);
            retryCounts[uuid] = 0; // reset retry count on success
        }
    };

    localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
    pc._tracksAdded = true;
}

function addRemoteStream(stream, uuid) {
    // if local is still styled as "remote-video", downgrade it to PiP
    const localVid = document.getElementById('local-video');
    if (localVid && localVid.classList.contains('remote-video')) {
        localVid.classList.replace('remote-video', 'local-video');
    }
    
    const id = `video-${uuid}`;
    if (document.getElementById(id)) return;

    const video = Object.assign(document.createElement('video'), {
        id,
        srcObject: stream,
        autoplay: true,
        playsInline: true,
        muted: true
    });
    video.classList.add('remote-video');
    document.getElementById('videos').appendChild(video);
    Logger.info('remote video added', { peer: uuid });
}

function handleUserDisconnect(uuid) {
    Logger.info('disconnecting peer', { peer: uuid });
    const video = document.getElementById(`video-${uuid}`);
    if (video) video.remove();
    if (peers[uuid]) {
        peers[uuid].close();
        delete peers[uuid];
    }
}
function toggleMute() {
    localStream.getAudioTracks().forEach(track => track.enabled = !track.enabled);
    isMuted = !isMuted;
    document.getElementById('mute-btn').textContent = isMuted ? 'Unmute' : 'Mute';
    Logger.info('ui:mute‑toggle', {muted: isMuted});
}

function toggleVideo() {
    localStream.getVideoTracks().forEach(track => track.enabled = !track.enabled);
    isVideoStopped = !isVideoStopped;
    document.getElementById('video-btn').textContent = isVideoStopped ? 'Start Video' : 'Stop Video';
    Logger.info('ui:video‑toggle', {stopped: isVideoStopped});
}

function generateUUID() {
    return 'xxxx-xxxx-4xxx-yxxx-xxxxxx'.replace(/[xy]/g, function(c) {
        const r = Math.random() * 16 | 0, v = c === 'x' ? r : (r & 0x3 | 0x8);
        return v.toString(16);
    });
}

