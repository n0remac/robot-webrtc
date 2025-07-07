const domainName = window.location.hostname === "localhost" ? "localhost:8080" : "noremac.dev";
const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";
const ROOM = new URLSearchParams(location.search).get("room") || "default";

let myUUID = generateUUID();
let ws;
let pc;
let dataChannel;

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

async function start() {
    // Fetch TURN
    const turnData = await fetchTurnCredentials();
    const iceServers = [
        { urls: 'stun:stun.l.google.com:19302' }
    ];
    if (turnData?.username && turnData?.password) {
        iceServers.push(
            { urls: 'turn:turn.noremac.dev:3478?transport=udp', username: turnData.username, credential: turnData.password },
            { urls: 'turns:turn.noremac.dev:443?transport=tcp', username: turnData.username, credential: turnData.password }
        );
    }
    // 1. Set up PeerConnection
    pc = new RTCPeerConnection({ iceServers });
    // 2. Set up control DataChannel
    dataChannel = pc.createDataChannel('keyboard');
    dataChannel.onopen = () => console.log('DataChannel open');
    dataChannel.onclose = () => console.log('DataChannel closed');
    // Optionally: receive robot status here
    dataChannel.onmessage = e => {
        console.log('Robot status:', e.data);
    };

    // 3. Handle robot's video
    pc.ontrack = ({ track, streams }) => {
        if (track.kind === 'video') {
            const video = document.getElementById('robot-video');
            video.srcObject = streams[0];
        }
    };

    // 4. ICE candidate handling
    pc.onicecandidate = e => {
        if (e.candidate) {
            ws.send(JSON.stringify({
                type: 'candidate',
                candidate: e.candidate,
                from: myUUID,
                room: ROOM
            }));
        }
    };

    // 5. Set up signaling WS
    ws = new WebSocket(`${wsProtocol}://${domainName}/ws/hub?room=${ROOM}`);
    ws.onopen = () => {
        ws.send(JSON.stringify({
            type: 'join',
            from: myUUID,
            room: ROOM
        }));
    };
    ws.onmessage = async ({ data }) => {
        const msg = JSON.parse(data);
        if (msg.to && msg.to !== myUUID) return;
        if (msg.from === myUUID) return;
        switch (msg.type) {
            case 'offer':
                await pc.setRemoteDescription(msg.offer);
                const answer = await pc.createAnswer();
                await pc.setLocalDescription(answer);
                ws.send(JSON.stringify({
                    type: 'answer',
                    answer: pc.localDescription,
                    from: myUUID,
                    to: msg.from,
                    room: ROOM
                }));
                break;
            case 'answer':
                await pc.setRemoteDescription(msg.answer);
                break;
            case 'candidate':
                if (msg.candidate) await pc.addIceCandidate(msg.candidate);
                break;
            case 'leave':
                pc.close();
                ws.close();
                break;
        }
    };

    // 6. Key handlers → send over DataChannel
    ['keydown', 'keyup'].forEach(evt => {
        window.addEventListener(evt, (e) => {
            // Ignore repeated keydown events
            if (evt === 'keydown' && e.repeat) return;
            // Only allow certain keys
            const allowed = ['w', 'a', 's', 'd', '1', '2', '3', '4'];
            const k = e.key.toLowerCase();
            if (!allowed.includes(k)) return;
            e.preventDefault();
            if (dataChannel && dataChannel.readyState === 'open') {
                dataChannel.send(JSON.stringify({
                    key: k,
                    action: evt === 'keydown' ? 'pressed' : 'released'
                }));
            }
        }, true);
    });
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

document.addEventListener("DOMContentLoaded", start);
