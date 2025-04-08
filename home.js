document.addEventListener('DOMContentLoaded', function() {
    document.addEventListener('htmx:wsConfigSend', function(evt) {
        const clickedElt = evt.detail.elt;
        const word = clickedElt.getAttribute('data-word');
        if (word) {
            evt.detail.parameters.selectedWord = word;
        }
        const currentContentElt = document.getElementById('content');
        if (currentContentElt) {
            const contentId = currentContentElt.getAttribute('data-content-id');
            evt.detail.parameters.currentContentId = contentId;
        }
    });

    const hoverTimers = new WeakMap();

    document.addEventListener('mouseover', function(e) {
        if (e.target.classList.contains('selectable-word')) {
            e.target.classList.add('hovering');
            const timer = setTimeout(() => {
                if (e.target.classList.contains('hovering')) {
                    e.target.classList.remove('hovering');
                    e.target.classList.add('selected');
                    e.target.dispatchEvent(new Event('click'));
                }
            }, 2000);

            hoverTimers.set(e.target, timer);
        }
    });

    document.addEventListener('mouseout', function(e) {
        if (e.target.classList.contains('selectable-word')) {
            e.target.classList.remove('hovering');
            const timer = hoverTimers.get(e.target);
            if (timer) {
                clearTimeout(timer);
            }
            hoverTimers.delete(e.target);
        }
    });

    document.addEventListener('click', function(e) {
        if (e.target.classList.contains('selectable-word')) {
            e.target.classList.remove('hovering');
            e.target.classList.add('selected');
        }
    });
});

document.addEventListener('htmx:wsBeforeMessage', function(evt) {
    // The raw message is in evt.detail.message
    try {
        let data = JSON.parse(evt.detail.message);

        if (data.type === "newContent") {
        // prevent htmx from doing normal auto-swap
        evt.preventDefault();

        fadeOutAndReplaceContent(data.html);
        }
    } catch (err) {
        // probably not JSON, or has no type
    }
});

function fadeOutAndReplaceContent(newHTML) {
    let oldContent = document.getElementById('content');
    if (!oldContent) {
        insertNewContent(newHTML);
        return;
    }

    // The parent that holds #content
    let parent = oldContent.parentNode;

    oldContent.classList.add('fading-out');

    let fadeCancelled = false;
    function handleClickToCancel() {
        fadeCancelled = true;
        oldContent.classList.remove('fading-out');
        oldContent.removeEventListener('click', handleClickToCancel);
    }
    oldContent.addEventListener('click', handleClickToCancel);

    oldContent.addEventListener('transitionend', function onTransitionEnd(e) {
        if (e.target !== oldContent) return; // ignore child transitions
        oldContent.removeEventListener('transitionend', onTransitionEnd);
        oldContent.removeEventListener('click', handleClickToCancel);

        if (!fadeCancelled) {
            // Remove old content from DOM
            parent.removeChild(oldContent);
            // Insert new content into the same parent
            insertNewContent(newHTML, parent);
        }
    });
}

function insertNewContent(newHTML, parent) {
    let tempDiv = document.createElement('div');
    tempDiv.innerHTML = newHTML;
  
    let newContent = tempDiv.firstElementChild;
    if (!newContent) return;
  
    // 1. Add initial class: fully transparent, no transition
    newContent.classList.add('fading-in');
  
    // 2. Insert into DOM
    parent.appendChild(newContent);
  
    // 3. Let the browser render once or twice
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        // 4. Add the active class that transitions from 0 -> 1
        newContent.classList.add('fading-in-active');
      });
    });
  
    // (Optional) clean up classes once the transition finishes
    newContent.addEventListener('transitionend', () => {
      newContent.classList.remove('fading-in', 'fading-in-active');
    });
  
    // Reprocess for htmx
    htmx.process(newContent);
  }
  
  
