// cards.js
// Drag-and-drop logic for the MVP card UI

/**
 * Called on dragstart of a hand card
 */
function onDragStart(event) {
    const cardId = event.target.getAttribute('data-card-id');
    event.dataTransfer.setData('text/plain', cardId);
  }
  
  /**
   * Initialize drag-and-drop handlers on DOM ready
   */
  document.addEventListener('DOMContentLoaded', () => {
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
  
    // Handle drop: move card to discard and update UI
    table.addEventListener('drop', e => {
      e.preventDefault();
      table.classList.remove('ring-4', 'ring-yellow-300');
  
      const cardId = e.dataTransfer.getData('text/plain');
      const cardEl = document.querySelector(`[data-card-id="${cardId}"]`);
      if (!cardEl) return;
  
      // Clone card into discard pile
      const clone = cardEl.cloneNode(true);
      clone.removeAttribute('draggable');
      clone.removeAttribute('ondragstart');
  
      // Clear previous discard and append
      discard.innerHTML = '';
      discard.appendChild(clone);
  
      // Remove original from hand
      cardEl.remove();
  
      // Optional: notify server of the play
      // fetch('/game/mvp/action', {
      //   method: 'POST',
      //   headers: { 'Content-Type': 'application/json' },
      //   body: JSON.stringify({ move: 'play', card: cardId })
      // });
  
      // Trigger HTMX refresh on the hand fragment
      if (window.htmx) {
        htmx.trigger(htmx.find('#player-hand'), 'htmx:refresh');
      }
    });
  });
  