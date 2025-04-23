const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";

const wsUrl =
  (location.protocol === "https:" ? "wss://" : "ws://")
  + location.host
  + "/ws/hub";

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

const ROOM = new URLSearchParams(location.search).get("room") || "default";

window.addEventListener('beforeunload', () => {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ leave: myUUID, room: ROOM }));
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
        ws.send(JSON.stringify({ join: myUUID, room: ROOM }));
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
    const { type, uuid, offer, answer, candidate } = data;
    if (uuid === myUUID) return;
  
    // **1) Lazily create** a peerConnection for every new uuid
    if (!peers[uuid]) {
      peers[uuid] = createPeerConnection(uuid);
    }
    const pc = peers[uuid];
  
    switch (type) {
      case 'join':
        // nothing else to do here—the PC is created, and negotiationneeded will fire
        break;
  
      case 'offer': {
        // polite‐negotiation logic
        const polite = myUUID < uuid;
        const collision = pc.makingOffer || pc.signalingState !== 'stable';
        if (!polite && collision) return;    // impolite side just skips
        if (collision) {
          await pc.setLocalDescription({ type: 'rollback' });
        }
        await pc.setRemoteDescription(offer);
        const ans = await pc.createAnswer();
        await pc.setLocalDescription(ans);
        ws.send(JSON.stringify({ type: 'answer', uuid: myUUID, to: uuid, answer: ans }));
        break;
      }
  
      case 'answer':
        if (!pc.makingOffer && pc.signalingState === 'have-local-offer') {
          await pc.setRemoteDescription(answer);
        }
        break;
  
      case 'candidate':
        try {
          await pc.addIceCandidate(candidate);
        } catch (e) {
          console.warn('ICE candidate failed', e);
        }
        break;
  
      case 'leave':
        cleanupPeer(uuid);
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
  const pc = peers[uuid];
  if (!pc) return Logger.warn('No peer for answer', { uuid });

  // only accept an answer if we’re in have-local-offer
  if (pc.signalingState !== 'have-local-offer') {
    Logger.warn('Skipping remote answer in state', { uuid, state: pc.signalingState });
    return;
  }

  try {
    await pc.setRemoteDescription(new RTCSessionDescription(answer));
    Logger.info('remote SDP set', { uuid });
  } catch (err) {
    Logger.error('remote SDP failed', err);
  }

  // flush any queued candidates…
  if (pc.queuedCandidates) {
    for (const c of pc.queuedCandidates) {
      await pc.addIceCandidate(new RTCIceCandidate(c));
    }
    pc.queuedCandidates = [];
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
        pc = createPeerConnection(uuid);
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
    ws.send(JSON.stringify({
        offer: offer,
        uuid:  uuid,           // which peer you're targeting
        room:  ROOM
      }));
    Logger.info('offer sent', { to: uuid });
}

async function createAnswer(uuid, offer) {
    Logger.info('createAnswer', { from: uuid });
    let pc = peers[uuid];
    if (!pc) {
        pc = createPeerConnection(uuid);
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
    ws.send(JSON.stringify({ answer: answer, uuid: uuid, room: ROOM }));
    Logger.info('answer sent', { to: uuid });
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


  

  function createPeerConnection(uuid) {
    const pc = new RTCPeerConnection({ iceServers: globalIceServers });
    pc.makingOffer = false;
    pc.onicecandidate = e => {
      if (!e.candidate) return;
      ws.send(JSON.stringify({
        type:      'candidate',
        uuid:      myUUID,
        to:        uuid,
        candidate: e.candidate
      }));
    };
    pc.onnegotiationneeded = async () => {
      try {
        pc.makingOffer = true;
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        ws.send(JSON.stringify({
          type:  'offer',
          uuid:  myUUID,
          to:    uuid,
          offer: offer
        }));
      } finally {
        pc.makingOffer = false;
      }
    };
    pc.ontrack = e => addRemoteStream(e.streams[0], uuid);
    // **add your local tracks**
    localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
    return pc;
  }
  