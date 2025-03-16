document.getElementById('join-btn').addEventListener('click', joinSession);
document.getElementById('mute-btn').addEventListener('click', toggleMute);
document.getElementById('video-btn').addEventListener('click', toggleVideo);

let localStream;
let peers = {};
let ws;

let peerConnection;
let isMuted = false;
let isVideoStopped = false;

async function joinSession() {
    const name = document.getElementById('name').value;
    if (!name) {
        alert('Please enter your name');
        return;
    }

    document.getElementById('join-screen').style.display = 'none';
    document.getElementById('participant-view').style.display = 'block';

    ws = new WebSocket(`ws://${window.location.host}/ws`);

    ws.onopen = () => {
        console.log('Connected to signaling server');
        ws.send(JSON.stringify({ type: 'join', name: name }));
    };

    localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });

    const localVideo = document.createElement('video');
    localVideo.srcObject = localStream;
    localVideo.autoplay = true;
    localVideo.muted = true;
    document.getElementById('videos').appendChild(localVideo);

    ws.onmessage = async (message) => {
        const data = JSON.parse(message.data);
        switch (data.type) {
            case 'join':
                createOffer(data.name);
                break;
            case 'offer':
                createAnswer(data.name, data.offer);
                break;
            case 'answer':
                await peers[data.name].setRemoteDescription(new RTCSessionDescription(data.answer));
                break;
            case 'candidate':
                await peers[data.name].addIceCandidate(new RTCIceCandidate(data.candidate));
                break;
        }
    };
}

async function createOffer(name) {
    const peerConnection = new RTCPeerConnection({
        iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
    });

    localStream.getTracks().forEach(track => peerConnection.addTrack(track, localStream));

    peerConnection.onicecandidate = (event) => {
        if (event.candidate) {
            ws.send(JSON.stringify({ type: 'candidate', name, candidate: event.candidate }));
        }
    };

    peerConnection.ontrack = (event) => {
        addRemoteStream(event.streams[0], name);
    };

    const offer = await peerConnection.createOffer();
    await peerConnection.setLocalDescription(offer);

    peers[name] = peerConnection;

    ws.send(JSON.stringify({ type: 'offer', name, offer }));
}

async function createAnswer(name, offer) {
    const peerConnection = new RTCPeerConnection({
        iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
    });

    localStream.getTracks().forEach(track => peerConnection.addTrack(track, localStream));

    peerConnection.onicecandidate = (event) => {
        if (event.candidate) {
            ws.send(JSON.stringify({ type: 'candidate', name, candidate: event.candidate }));
        }
    };

    peerConnection.ontrack = (event) => {
        addRemoteStream(event.streams[0], name);
    };

    await peerConnection.setRemoteDescription(new RTCSessionDescription(offer));
    const answer = await peerConnection.createAnswer();
    await peerConnection.setLocalDescription(answer);

    peers[name] = peerConnection;

    ws.send(JSON.stringify({ type: 'answer', name, answer }));
}

function addRemoteStream(stream, name) {
    let remoteVideo = document.createElement('video');
    remoteVideo.srcObject = stream;
    remoteVideo.autoplay = true;
    remoteVideo.id = `video-${name}`;
    document.getElementById('videos').appendChild(remoteVideo);
}


function toggleMute() {
    localStream.getAudioTracks().forEach(track => track.enabled = !track.enabled);
    isMuted = !isMuted;
    document.getElementById('mute-btn').textContent = isMuted ? 'Unmute' : 'Mute';
}

function toggleVideo() {
    localStream.getVideoTracks().forEach(track => track.enabled = !track.enabled);
    isVideoStopped = !isVideoStopped;
    document.getElementById('video-btn').textContent = isVideoStopped ? 'Start Video' : 'Stop Video';
}

function addRemoteStream(stream) {
    const remoteVideo = document.createElement('video');
    remoteVideo.srcObject = stream;
    remoteVideo.autoplay = true;
    document.getElementById('videos').appendChild(remoteVideo);
}
