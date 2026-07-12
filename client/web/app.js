const controlKeys = new Set(['w', 'a', 's', 'd', 't', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'r', 'y']);
const pressed = new Set();
const statusEl = document.querySelector('#status');
let socket;
let reconnectTimer;

function connect() {
  clearTimeout(reconnectTimer);
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  socket = new WebSocket(`${protocol}//${location.host}/ws/control`);
  socket.addEventListener('open', () => {
    statusEl.textContent = 'Connected';
    statusEl.classList.add('connected');
  });
  socket.addEventListener('close', () => {
    pressed.clear();
    document.querySelectorAll('button.active').forEach(button => button.classList.remove('active'));
    statusEl.textContent = 'Disconnected — retrying…';
    statusEl.classList.remove('connected');
    reconnectTimer = setTimeout(connect, 1000);
  });
}

function send(message) {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
}

function setKey(key, isPressed) {
  if (!controlKeys.has(key)) return;
  if (isPressed && pressed.has(key)) return;
  if (!isPressed && !pressed.has(key)) return;
  isPressed ? pressed.add(key) : pressed.delete(key);
  document.querySelector(`[data-key="${key}"]`)?.classList.toggle('active', isPressed);
  send({key, action: isPressed ? 'pressed' : 'released'});
}

function releaseAll() {
  [...pressed].forEach(key => setKey(key, false));
}

window.addEventListener('keydown', event => {
  const key = event.key.toLowerCase();
  if (controlKeys.has(key)) {
    event.preventDefault();
    setKey(key, true);
  }
});
window.addEventListener('keyup', event => {
  const key = event.key.toLowerCase();
  if (controlKeys.has(key)) {
    event.preventDefault();
    setKey(key, false);
  }
});
window.addEventListener('blur', releaseAll);
document.addEventListener('visibilitychange', () => { if (document.hidden) releaseAll(); });

document.querySelectorAll('[data-key]').forEach(button => {
  const key = button.dataset.key;
  button.addEventListener('pointerdown', event => {
    event.preventDefault();
    button.setPointerCapture(event.pointerId);
    setKey(key, true);
  });
  for (const eventName of ['pointerup', 'pointercancel', 'lostpointercapture']) {
    button.addEventListener(eventName, () => setKey(key, false));
  }
});

setInterval(() => send({type: 'heartbeat'}), 250);
connect();
