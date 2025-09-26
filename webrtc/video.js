const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";
const ROOM = new URLSearchParams(location.search).get("room") || "default";

Logger.attachSocket(`${wsProtocol}://${domainName}/ws/logs`);
document.addEventListener("DOMContentLoaded", () => {
  document.getElementById('test-camera').addEventListener('click', testCamera);
  document.getElementById('test-mic').addEventListener('click', testMic);
  document.getElementById('join-btn').addEventListener('click', joinSession);
  document.getElementById('mute-btn').addEventListener('click', toggleMute);
  document.getElementById('video-btn').addEventListener('click', toggleVideo);
  document.getElementById('noise-btn').addEventListener('click', toggleNoise);
});

let myUUID = null;
let myName;
let localStream;
let isMuted = false;
let isVideoStopped = false;
let ws;
let globalIceServers = [];
const peerNames = {};
const dataChannels = {};
const peers = {};

window.addEventListener('beforeunload', () => {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({
      type: 'leave',
      leave: myUUID,
      from: myUUID,
      room: ROOM
    }));
    Logger.info('sent leave on unload', { uuid: myUUID });
    ws.close();
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
    if (!res.ok) {
      console.error('Failed to fetch turn credentials:', res.status, res.statusText);
      return null;
    }
    const text = await res.text();
    try {
      return JSON.parse(text);
    } catch (err) {
      console.error('Turn credentials are not JSON:', text);
      return null;
    }
  } catch (e) {
    console.error('Error fetching turn credentials:', e);
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
    const both = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
    localStream = both;
    Logger.info('audio+video gUM success');
  } catch (err) {
    Logger.warn('audio+video failed, trying partials', err);
    let tracks = [];
    try {
      const v = await navigator.mediaDevices.getUserMedia({ video: true });
      tracks = tracks.concat(v.getVideoTracks());
    } catch { }
    try {
      const a = await navigator.mediaDevices.getUserMedia({ audio: true });
      tracks = tracks.concat(a.getAudioTracks());
    } catch { }
    localStream = new MediaStream(tracks);
    if (!tracks.length) {
      const warn = document.getElementById('no-media-warning');
      if (warn) warn.textContent = '⚠️ No camera or mic available; joining with media disabled.';
    }
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
  if (Object.keys(peers).length === 0) {
    video.classList.add('remote-video');
  } else {
    video.classList.add('local-video');
  }
  document.getElementById('videos').appendChild(video);
  Logger.info('local video added');
}

async function connectWebSocket() {
  ws = new WebSocket(
    (location.protocol === 'https:' ? 'wss://' : 'ws://') +
    location.host +
    '/ws/hub?room=' + encodeURIComponent(ROOM) +
    '&playerId=' + encodeURIComponent(myUUID)
  );

  ws.onopen = () => {
    Logger.info('WebSocket open');
    ws.send(JSON.stringify({
      type: 'join',
      join: myUUID,
      from: myUUID,
      room: ROOM
    }));
  };

  ws.onmessage = ({ data }) => {
    const msg = JSON.parse(data);
    if (msg.name) {
      peerNames[msg.from] = msg.name;
    }
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
  ws.onclose = (e) => {
    if (e.code === 1006) {
      Object.values(peers).forEach(pc => pc.close());
      for (const k in peers) delete peers[k];
      for (const peerId in dataChannels) {
        closeAndDeleteDataChannel(peerId);
      }
      setTimeout(connectWebSocket, 1000);
    }
  };
}

function addAudioStream(stream, uuid) {
  console.log("✅ addAudioStream fired:", stream);
  const id = `audio-${uuid}`;
  if (document.getElementById(id)) return;
  const audio = Object.assign(
    document.createElement('audio'),
    { id, srcObject: stream, autoplay: true }
  );
  document.body.appendChild(audio);
}

function addVideoStream(stream, uuid) {
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
    muted: false,
  });
  video.classList.add('remote-video');

  // add name overlay
  const nameOverlay = document.createElement('div');
  nameOverlay.className = 'name-overlay';
  nameOverlay.textContent = peerNames[uuid] || uuid;
  nameOverlay.style.position = 'absolute';
  nameOverlay.style.top = '0';
  nameOverlay.style.left = '0';
  nameOverlay.style.color = 'white';
  nameOverlay.style.backgroundColor = 'rgba(0, 0, 0, 0.5)';

  const videoContainer = document.createElement('div');
  videoContainer.style.position = 'relative';
  videoContainer.style.overflow = 'hidden';
  videoContainer.style.borderRadius = '10px';
  videoContainer.style.boxShadow = '0 0 10px rgba(0, 0, 0, 0.5)';
  videoContainer.style.margin = '10px';
  videoContainer.style.backgroundColor = 'black';
  videoContainer.appendChild(video);
  videoContainer.appendChild(nameOverlay);

  document.getElementById('videos').appendChild(videoContainer);
  Logger.info('remote video added', { peer: uuid });
}

function handleUserDisconnect(uuid) {
  Logger.info('disconnecting peer', { peer: uuid });
  const video = document.getElementById(`video-${uuid}`);
  const audio = document.getElementById(`audio-${uuid}`);
  if (!audio) Logger.warn('no audio found for peer', { peer: uuid });
  if (audio) audio.remove();
  if (!video) Logger.warn('no video found for peer', { peer: uuid });
  if (video) video.remove();

  closeAndDeleteDataChannel(uuid);

  if (peers[uuid]) {
    peers[uuid].close();
    delete peers[uuid];
    Logger.info('closing peer connection', { peer: uuid });
  } else {
    Logger.warn('no peer connection found', { peer: uuid });
  }
}

function closeAndDeleteDataChannel(peerId) {
  const ch = dataChannels[peerId];
  if (!ch) return;

  try {
    // Only attempt close if it’s not already closed
    if (ch.readyState === 'open' || ch.readyState === 'closing') {
      ch.close();
    }
  } catch (e) {
    Logger.warn('error closing datachannel', { peer: peerId, err: String(e) });
  } finally {
    delete dataChannels[peerId];
  }
}

function createPeerConnection(peerId) {
  const pc = new RTCPeerConnection({ iceServers: globalIceServers });
  pc.makingOffer = false;
  pc.ignoreOffer = false;
  const polite = myUUID < peerId;
  let negotiating = false;

  const safeSend = (obj) => {
    try {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(obj));
      } else {
        Logger.warn('WS not open; dropping signal', { to: peerId, type: obj?.type });
      }
    } catch (e) {
      Logger.error('WS send failed', { err: String(e), to: peerId, type: obj?.type });
    }
  };

  // create an *outgoing* data‐channel
  const dc = pc.createDataChannel('keyboard');
  dataChannels[peerId] = dc;

  dc.onopen = () =>
    Logger.info('keyboard DataChannel open', { peer: peerId });
  dc.onmessage = e => {
    const msg = JSON.parse(e.data);
    handleRemoteKey(msg.key, msg.action, peerId);
  };
  dc.onclose = () => {
    // Only delete if the stored reference points to THIS channel instance.
    if (dataChannels[peerId] === dc) {
      delete dataChannels[peerId];
    }
    Logger.info('DataChannel closed', { peer: peerId });
  };

  // accept an *incoming* data‐channel
  pc.ondatachannel = ({ channel }) => {
    dataChannels[peerId] = channel;
    channel.onopen = () => Logger.info('incoming channel open', { peer: peerId });
    channel.onmessage = e => {
      const msg = JSON.parse(e.data);
      handleRemoteKey(msg.key, msg.action, peerId);
    };
  };

  // buffer any early ICE candidates here
  pc.queuedCandidates = [];

  pc.ontrack = ({ track, streams }) => {
    const stream = streams[0];
    if (track.kind === 'video') {
      addVideoStream(stream, peerId);
    } else if (track.kind === 'audio') {
      addAudioStream(stream, peerId);
    }
  };

  pc.onnegotiationneeded = async () => {
    // prevent re-entrancy
    if (negotiating) return;
    // bail if closed
    if (pc.signalingState === 'closed') return;

    negotiating = true;
    try {
      pc.makingOffer = true;
      const offer = await pc.createOffer().catch((e) => {
        Logger.error('createOffer failed', { peer: peerId, err: String(e) });
        throw e;
      });

      await pc.setLocalDescription(offer).catch((e) => {
        Logger.error('setLocalDescription(offer) failed', { peer: peerId, err: String(e) });
        throw e;
      });

      safeSend({
        type: 'offer',
        offer: pc.localDescription,
        from: myUUID,
        to: peerId,
        name: myName,
        room: ROOM
      });
      Logger.info('offer sent', { to: peerId });
    } catch (e) {
      // If anything went wrong, try to roll back to stable to avoid “stuck” states
      try {
        if (pc.signalingState !== 'stable') {
          await pc.setLocalDescription({ type: 'rollback' });
          Logger.warn('rolled back after offer failure', { peer: peerId });
        }
      } catch (rbErr) {
        Logger.error('rollback failed after offer error', { peer: peerId, err: String(rbErr) });
      }
    } finally {
      pc.makingOffer = false;
      negotiating = false;
    }
  };

  pc.onicecandidate = e => {
    if (!e.candidate) return;
    safeSend({
      type: 'candidate',
      candidate: e.candidate,
      from: myUUID,
      to: peerId,
      room: ROOM
    });
  };

  pc.oniceconnectionstatechange = () => {
    if (pc.iceConnectionState === 'failed') {
      // ICE restart pathway: wrap in try/catch and log
      (async () => {
        try {
          const offer = await pc.createOffer({ iceRestart: true });
          await pc.setLocalDescription(offer);
          safeSend({
            type: 'offer',
            offer: pc.localDescription,
            from: myUUID,
            to: peerId,
            room: ROOM
          });
          Logger.info('ICE restart offer sent', { to: peerId });
        } catch (e) {
          Logger.error('ICE restart failed', { peer: peerId, err: String(e) });
          // Try to rollback if we got stuck mid-restart
          try {
            if (pc.signalingState !== 'stable') {
              await pc.setLocalDescription({ type: 'rollback' });
              Logger.warn('rolled back after ICE restart failure', { peer: peerId });
            }
          } catch (rbErr) {
            Logger.error('rollback failed after ICE restart error', { peer: peerId, err: String(rbErr) });
          }
        }
      })();
    }
  };

  pc.handleSignal = async msg => {
    switch (msg.type) {
      case 'offer': {
        // collision detection per Perfect Negotiation
        const collision = pc.makingOffer || pc.signalingState !== 'stable';
        pc.ignoreOffer = !polite && collision;
        if (pc.ignoreOffer) return;

        try {
          if (collision) {
            await pc.setLocalDescription({ type: 'rollback' }).catch(e => {
              Logger.error('rollback failed before applying remote offer', { peer: peerId, err: String(e) });
              throw e;
            });
          }

          await pc.setRemoteDescription(msg.offer).catch(e => {
            Logger.error('setRemoteDescription(offer) failed', { peer: peerId, err: String(e) });
            throw e;
          });

          // apply any queued ICE now that we have an RD
          for (const c of pc.queuedCandidates) {
            try { await pc.addIceCandidate(c); }
            catch (e) { Logger.warn('queued ICE add failed after offer', { peer: peerId, err: String(e) }); }
          }
          pc.queuedCandidates = [];

          const answer = await pc.createAnswer().catch(e => {
            Logger.error('createAnswer failed', { peer: peerId, err: String(e) });
            throw e;
          });

          await pc.setLocalDescription(answer).catch(e => {
            Logger.error('setLocalDescription(answer) failed', { peer: peerId, err: String(e) });
            throw e;
          });

          safeSend({
            type: 'answer',
            answer: pc.localDescription,
            from: myUUID,
            to: peerId,
            name: myName,
            room: ROOM
          });
          Logger.info('answer sent', { to: peerId });
        } catch (e) {
          // Try to get back to a sane state if anything broke mid-way
          try {
            if (pc.signalingState !== 'stable') {
              await pc.setLocalDescription({ type: 'rollback' });
              Logger.warn('rolled back after inbound offer error', { peer: peerId });
            }
          } catch (rbErr) {
            Logger.error('rollback failed after inbound offer error', { peer: peerId, err: String(rbErr) });
          }
        }
        break;
      }

      case 'answer': {
        // Only valid when we have a local offer outstanding
        if (!pc.makingOffer && pc.signalingState === 'have-local-offer') {
          try {
            await pc.setRemoteDescription(msg.answer);
            for (const c of pc.queuedCandidates) {
              try { await pc.addIceCandidate(c); }
              catch (e) { Logger.warn('queued ICE add failed after answer', { peer: peerId, err: String(e) }); }
            }
            pc.queuedCandidates = [];
            Logger.info('remote SDP applied', { peer: peerId });
          } catch (e) {
            Logger.error('setRemoteDescription(answer) failed', { peer: peerId, err: String(e) });
            // If we crashed mid-apply, attempt rollback to avoid deadlock
            try {
              if (pc.signalingState !== 'stable') {
                await pc.setLocalDescription({ type: 'rollback' });
                Logger.warn('rolled back after answer error', { peer: peerId });
              }
            } catch (rbErr) {
              Logger.error('rollback failed after answer error', { peer: peerId, err: String(rbErr) });
            }
          }
        } else {
          Logger.warn('unexpected answer (no local offer)', { peer: peerId, state: pc.signalingState });
        }
        break;
      }

      case 'candidate': {
        // If RD not yet set, queue the candidate
        if (!pc.remoteDescription) {
          pc.queuedCandidates.push(msg.candidate);
        } else {
          try {
            await pc.addIceCandidate(msg.candidate);
          } catch (e) {
            Logger.warn('addIceCandidate failed', { peer: peerId, err: String(e) });
          }
        }
        break;
      }

      case 'leave': {
        Logger.info('handling leave signal', { from: msg.from });
        handleUserDisconnect(msg.from);
        break;
      }
    }
  };
  // add our local tracks to kick off negotiationneeded
  localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
  return pc;
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
