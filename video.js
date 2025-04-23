const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";
const ROOM = new URLSearchParams(location.search).get("room") || "default";

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

window.addEventListener('beforeunload', () => {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ 
            leave: myUUID, 
            from: myUUID,
            room: ROOM }));
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
    ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws/hub');
  
    ws.onopen = () => {
      Logger.info('WebSocket open');
      ws.send(JSON.stringify({
        join: myUUID,   // ← server’s “join” command
        from: myUUID,
        room: ROOM
      }));
    };
  
    ws.onmessage = ({ data }) => {
      const msg = JSON.parse(data);
      // filter messages not addressed to us or echoes
      if (msg.to && msg.to !== myUUID) return;
      if (msg.from === myUUID) return;
  
      // lazy PC creation
      if (!peers[msg.from]) {
        peers[msg.from] = createPeerConnection(msg.from);
      }
      peers[msg.from].handleSignal(msg);
    };
  
    ws.onerror = e => Logger.error('WebSocket error', e);
    ws.onclose = e => Logger.warn('WebSocket closed', e);
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

function createPeerConnection(peerId) {
    const pc = new RTCPeerConnection({ iceServers: globalIceServers });
    pc.makingOffer = false;
    pc.ignoreOffer = false;
    const polite = myUUID < peerId;
    let negotiating = false;
  
    // buffer any early ICE candidates here
    pc.queuedCandidates = [];
  
    pc.onnegotiationneeded = async () => {
      if (negotiating) return;
      negotiating = true;
      try {
        pc.makingOffer = true;
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
  
        ws.send(JSON.stringify({
          offer: pc.localDescription,
          from:  myUUID,
          to:    peerId,
          room:  ROOM
        }));
        Logger.info('offer sent', { to: peerId });
      } finally {
        pc.makingOffer = false;
        negotiating = false;
      }
    };
  
    pc.onicecandidate = e => {
      if (!e.candidate) return;
      ws.send(JSON.stringify({
        candidate: e.candidate,
        from:      myUUID,
        to:        peerId,
        room:      ROOM
      }));
    };
  
    pc.oniceconnectionstatechange = () => {
      if (pc.iceConnectionState === 'failed') {
        pc.createOffer({ iceRestart: true })
          .then(o => pc.setLocalDescription(o))
          .then(() => ws.send(JSON.stringify({
            offer: pc.localDescription,
            from:  myUUID,
            to:    peerId,
            room:  ROOM
          })));
      }
    };
  
    pc.ontrack = e => addRemoteStream(e.streams[0], peerId);
  
    pc.handleSignal = async msg => {
        if (msg.offer) {
          const collision = pc.makingOffer || pc.signalingState !== 'stable';
          pc.ignoreOffer = !polite && collision;
          if (pc.ignoreOffer) return;
          if (collision) await pc.setLocalDescription({ type: 'rollback' });
  
          await pc.setRemoteDescription(msg.offer);
          // flush any queued ICE candidates
          pc.queuedCandidates.forEach(c => pc.addIceCandidate(c));
          pc.queuedCandidates = [];
  
          const answer = await pc.createAnswer();
          await pc.setLocalDescription(answer);
          ws.send(JSON.stringify({
            answer: pc.localDescription,
            from:   myUUID,
            to:     peerId,
            room:   ROOM
          }));
          Logger.info('answer sent', { to: peerId });
        } else if (msg.answer) {
          if (!pc.makingOffer && pc.signalingState === 'have-local-offer') {
            await pc.setRemoteDescription(msg.answer);
            // flush queued candidates
            pc.queuedCandidates.forEach(c => pc.addIceCandidate(c));
            pc.queuedCandidates = [];
            Logger.info('remote SDP applied', { peer: peerId });
          }
        } else if (msg.candidate) {
          // if remoteDescription isn’t set yet, queue it
          if (!pc.remoteDescription) {
            pc.queuedCandidates.push(msg.candidate);
          } else {
            try {
              await pc.addIceCandidate(msg.candidate);
            } catch (e) {
              console.warn('failed to add ICE candidate', e);
            }
          }
        } else if (msg.leave) {
          handleUserDisconnect(peerId);
        }
      }
  
    // add our local tracks to kick off negotiationneeded
    localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
    return pc;
  }
  
  