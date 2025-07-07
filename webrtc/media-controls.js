function toggleNoise() {
    localStream.getAudioTracks().forEach(track => track.enabled = !track.enabled);
    track.applyConstraints({
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true
    });
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

let previewStream;
async function testCamera() {
  try {
    previewStream = await navigator.mediaDevices.getUserMedia({ video: true });
    const preview = document.getElementById('preview-video');
    preview.srcObject = previewStream;
    preview.style.display = 'block';
    Logger.info('Camera test passed');
  } catch (err) {
    alert('Camera access failed: ' + err.message);
    Logger.error('Camera test failed', err);
  }
}

let micStream;
async function testMic() {
  try {
    micStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    document.getElementById('mic-status').textContent = '🎤 Microphone is working';
    // optional: hook into Web Audio API to display levels…
    Logger.info('Mic test passed');
  } catch (err) {
    document.getElementById('mic-status').textContent = '❌ Mic access denied';
    Logger.error('Mic test failed', err);
  }
}