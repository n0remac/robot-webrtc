const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";
const ROOM = "robot";

let myUUID = generateUUID();
let ws;
let pc;
let dc;
let globalIceServers = [];
const ROBOT_ID = "robot";

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

async function joinSession() {
    // Fetch TURN
    const turnData = await fetchTurnCredentials();
    globalIceServers = [
        { urls: 'stun:stun.l.google.com:19302' }
    ];
    if (turnData?.username && turnData?.password) {
        globalIceServers.push(
            { urls: 'turn:turn.noremac.dev:3478?transport=udp', username: turnData.username, credential: turnData.password },
            { urls: 'turns:turn.noremac.dev:443?transport=tcp', username: turnData.username, credential: turnData.password }
        );
    }
    await connectWebSocket();
}

async function connectWebSocket() {
    ws = new WebSocket(
      (location.protocol === 'https:' ? 'wss://' : 'ws://')
      + location.host
      + '/ws/hub?room=' + encodeURIComponent(ROOM)
      + '&playerId=' + encodeURIComponent(myUUID)
    );

    ws.onopen = () => {
      Logger.info('WebSocket open');
      ws.send(JSON.stringify({
        type: 'join',
        join: myUUID,
        from: myUUID,
        room: ROOM,
        to: ROBOT_ID
      }));
    };
  
    ws.onmessage = ({ data }) => {
      const msg = JSON.parse(data);
      console.log("Received message:", msg);

      // Only handle messages *from* the robot
      if (msg.from !== ROBOT_ID) return;

      if (!pc) {
          pc = createPeerConnection(ROBOT_ID);
      }
      pc.handleSignal(msg);
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

function createPeerConnection(peerId) {
    const pc = new RTCPeerConnection({ iceServers: globalIceServers });
    pc.makingOffer = false;
    pc.ignoreOffer = false;
    const polite = myUUID < peerId;
    let negotiating = false;

    // create an *outgoing* data‐channel
    dc = pc.createDataChannel('keyboard');

    dc.onopen = () =>
      Logger.info('keyboard DataChannel open', { peer: peerId });
    dc.onmessage = e => {
      const msg = JSON.parse(e.data);
      handleRemoteKey(msg.key, msg.action, peerId);
    };

    // accept an *incoming* data‐channel
    pc.ondatachannel = ({ channel }) => {
      dc = channel;
      channel.onopen    = () => Logger.info('incoming channel open', { peer: peerId });
      channel.onmessage = e => {
        const msg = JSON.parse(e.data);
        handleRemoteKey(msg.key, msg.action, peerId);
      };
    };
  
    // buffer any early ICE candidates here
    pc.queuedCandidates = [];

    pc.ontrack = ({ track, streams }) => {
      const video = document.getElementById('robot-video');
      if (track.kind === 'video' && video) {
          video.srcObject = streams[0];
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
        console.log("Handling signal:", msg);
        switch (msg.type) {
          case 'offer':
            console.log("Received offer from robot, processing…");
            const collision = pc.makingOffer || pc.signalingState !== 'stable';
            pc.ignoreOffer = !polite && collision;
            if (pc.ignoreOffer) return;
            if (collision) await pc.setLocalDescription({ type: 'rollback' });
    
            try {
              await pc.setRemoteDescription(msg.offer);
              Logger.info('Set remote description OK');
            } catch (e) {
              Logger.error('Failed to set remote description', e);
            }
            // flush any queued ICE candidates
            pc.queuedCandidates.forEach(c => pc.addIceCandidate(c));
            pc.queuedCandidates = [];
    
            const answer = await pc.createAnswer();
            await pc.setLocalDescription(answer);

            console.log("Received offer from robot, sending answer…");
            ws.send(JSON.stringify({
              type:   'answer',
              answer: pc.localDescription,
              from:   myUUID,
              to:     peerId,
              room:   ROOM,
              name:   'robot'
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
          console.log("Received ICE candidate from robot, processing…");
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
  
  return pc;
}

function handleUserDisconnect(uuid) {
    Logger.info('disconnecting peer', { peer: uuid });
    const video = document.getElementById(`robot-video`);
    if (!video) Logger.warn('no video found for peer', { peer: uuid });
    if (video) video.remove();
    if (pc) {
        pc.close();
        Logger.info('closing peer connection', { peer: uuid });
      } else {
        Logger.warn('no peer connection found', { peer: uuid });
      }
}

async function fetchTurnCredentials() {
    try {
        const res = await fetch('/turn-credentials');
        return res.ok ? res.json() : null;
    } catch (e) {
        return null;
    }
}

function generateUUID() {
    return 'xxxx-xxxx-4xxx-yxxx-xxxxxx'.replace(/[xy]/g, function(c) {
        const r = Math.random() * 16 | 0, v = c === 'x' ? r : (r & 0x3 | 0x8);
        return v.toString(16);
    });
}

function bindkeys() {
    // bind keys
    ;[
      'w','a','s','d', 
      '1', '2', '3', '4',
      't', 'f', 'g', 'h',
      'i', 'j', 'k', 'l',
      'r', 'y',
    ].forEach(k =>
      createKeyPressEventListener(k)
    );
}

function createKeyPressEventListener(key) {
  const normalized = key.toLowerCase();

  function handler(e) {
    if (e.key && e.key.toLowerCase() === normalized) {
      e.preventDefault();
      e.stopImmediatePropagation();

      const action = e.type === 'keydown' ? 'pressed' : 'released';
      console.log(`Key ${normalized} ${action}`);

      // broadcast to each peer
      if (dc && dc.readyState === 'open') {
        console.log(`Sending ${action} event to ${dc.label}`);
        dc.send(JSON.stringify({ key: normalized, action }));
      }
    }
  }

  window.addEventListener('keydown', handler, true);
  window.addEventListener('keyup',   handler, true);
}

function start() {
    bindkeys();
    joinSession();
}

document.addEventListener("DOMContentLoaded", start);
