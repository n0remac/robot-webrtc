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
            room: ROOM }));
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
  let videoStream = null;
  let audioStream = null;

  // try video
  try {
    videoStream = await navigator.mediaDevices.getUserMedia({ video: true });
    Logger.info('video gUM success');
  } catch (err) {
    Logger.warn('video unavailable, continuing without camera', err);
  }

  // try audio
  try {
    audioStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    Logger.info('audio gUM success');
  } catch (err) {
    Logger.warn('audio unavailable, continuing without mic', err);
  }

  // merge whatever tracks we got (could be zero!)
  const tracks = [
    ...(videoStream ? videoStream.getVideoTracks() : []),
    ...(audioStream ? audioStream.getAudioTracks() : [])
  ];
  localStream = new MediaStream(tracks);

  // (optional) notify user in UI
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
        Logger.error('WebSocket closed abnormally', { code: e.code, reason: e.reason });
        Logger.info('Trying to reconnect...');
        setTimeout(connectWebSocket, 1000);
      } else {
        Logger.info('WebSocket closed', { code: e.code, reason: e.reason });
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
    if (!video) Logger.warn('no video found for peer', { peer: uuid });
    if (video) video.remove();
    if (peers[uuid]) {
        peers[uuid].close();
        delete peers[uuid];
        Logger.info('closing peer connection', { peer: uuid });
      } else {
        Logger.warn('no peer connection found', { peer: uuid });
      }
}

function createPeerConnection(peerId) {
    const pc = new RTCPeerConnection({ iceServers: globalIceServers });
    pc.makingOffer = false;
    pc.ignoreOffer = false;
    const polite = myUUID < peerId;
    let negotiating = false;

    // create an *outgoing* data‐channel
    const dc = pc.createDataChannel('keyboard');
    dataChannels[peerId] = dc;

    dc.onopen = () =>
      Logger.info('keyboard DataChannel open', { peer: peerId });
    dc.onmessage = e => {
      const msg = JSON.parse(e.data);
      handleRemoteKey(msg.key, msg.action, peerId);
    };

    // accept an *incoming* data‐channel
    pc.ondatachannel = ({ channel }) => {
      dataChannels[peerId] = channel;
      channel.onopen    = () => Logger.info('incoming channel open', { peer: peerId });
      channel.onmessage = e => {
        const msg = JSON.parse(e.data);
        handleRemoteKey(msg.key, msg.action, peerId);
      };
    };
  
    // buffer any early ICE candidates here
    pc.queuedCandidates = [];

    pc.ontrack = ({ track, streams }) => {
      console.log("✅ ontrack fired:", event.track.kind);
      const stream = streams[0];
      if (track.kind === 'video') {
        addVideoStream(stream, peerId);
      } else if (track.kind === 'audio') {
        addAudioStream(stream, peerId);
      }
    };
  
    pc.onnegotiationneeded = async () => {
      if (negotiating) return;
      negotiating = true;
      try {
        pc.makingOffer = true;
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
  
        ws.send(JSON.stringify({
          type: 'offer',
          offer: pc.localDescription,
          from:  myUUID,
          to:    peerId,
          name:  myName,
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
        type: 'candidate',
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
            type: 'offer',
            offer: pc.localDescription,
            from:  myUUID,
            to:    peerId,
            room:  ROOM
          })));
      }
    };
  
    pc.handleSignal = async msg => {
        switch (msg.type) {
          case 'offer':
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
              type:   'answer',
              answer: pc.localDescription,
              from:   myUUID,
              to:     peerId,
              name:   myName,
              room:   ROOM
            }));
            Logger.info('answer sent', { to: peerId });
            break;
        case 'answer':
          if (!pc.makingOffer && pc.signalingState === 'have-local-offer') {
            await pc.setRemoteDescription(msg.answer);
            // flush queued candidates
            pc.queuedCandidates.forEach(c => pc.addIceCandidate(c));
            pc.queuedCandidates = [];
            Logger.info('remote SDP applied', { peer: peerId });
          }
          break;
        case 'candidate':
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
          break;
        case 'leave':
          Logger.info('handling leave signal', { from: msg.from });
          handleUserDisconnect(msg.from);
          break;
      }
  }
  // add our local tracks to kick off negotiationneeded
  localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
  return pc;
}

function generateUUID() {
    return 'xxxx-xxxx-4xxx-yxxx-xxxxxx'.replace(/[xy]/g, function(c) {
        const r = Math.random() * 16 | 0, v = c === 'x' ? r : (r & 0x3 | 0x8);
        return v.toString(16);
    });
}
