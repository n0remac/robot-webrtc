function onDragStart(event) {
  const cardId = event.target.getAttribute('data-card-id');
  const room = event.target.getAttribute('data-room');
  const playerId = event.target.getAttribute('data-player-id');
  event.dataTransfer.setData('text/plain', cardId);
  event.dataTransfer.setData('room', room);
  event.dataTransfer.setData('playerId', playerId);
}
  
const table = document.getElementById('table-area');
const discard = document.getElementById('discard-pile');

// Highlight table when dragging over
table.addEventListener('dragover', e => {
  e.preventDefault();
  table.classList.add('ring-4', 'ring-yellow-300');
});

// Remove highlight on leave
table.addEventListener('dragleave', () => {
  table.classList.remove('ring-4', 'ring-yellow-300');
});

// Handle drop: move card to table and update UI
table.addEventListener('drop', e => {
  e.preventDefault();
  table.classList.remove('ring-4', 'ring-yellow-300');

  const cardId = e.dataTransfer.getData('text/plain');
  const room = e.dataTransfer.getData('room');
  const playerId = e.dataTransfer.getData('playerId');

  sendWebSocketCommand("playCardToTrick", {
    room: room,
    card: cardId,
    playerId: playerId
  });
});

function sendWebSocketCommand(commandType, payload) {
    if (!wsWrapper) {
        console.warn("WebSocket not ready");
        return;
    }

    const message = {
        type: commandType,
        ...payload
    };

    wsWrapper.send(JSON.stringify(message));
}

