document.addEventListener('DOMContentLoaded', function() {
    loadInitialContent();

    const backButton = document.getElementById("back-button");
    const forwardButton = document.getElementById("forward-button");

    if (backButton) {
        backButton.addEventListener("click", goBack);
    }
    if (forwardButton) {
        forwardButton.addEventListener("click", goForward);
    }
    updateNavigationButtons();

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
    try {
        let data = JSON.parse(evt.detail.message);
        if (data.type === "newContent") {
            evt.preventDefault();
            // Save the new content to history
            appendNewContentToHistory(data.html);
            // Insert the new content with a fade effect
            fadeOutAndReplaceContent(data.html);
        }
    } catch (err) {
        // Not JSON or no valid type; do nothing
    }
});

window.addEventListener("load", function() {
    localStorage.removeItem("contentHistory");
    localStorage.removeItem("currentContentIndex");
    loadInitialContent()
    updateNavigationButtons();
  });

function loadInitialContent() {
    let history = loadHistory();
    if (history.length === 0) {
        let initialContent = document.getElementById("content");
        if (initialContent) {
            // Save the current content's outer HTML or inner HTML (depending on your use case)
            history.push(initialContent.outerHTML);
            saveHistory(history);
            setCurrentIndex(0);
        }
    }
}

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
    let tempDiv = document.createElement("div");
    tempDiv.innerHTML = newHTML;
    let newContent = tempDiv.firstElementChild;
    if (!newContent) return;
  
    // Use initial fade-in classes (we use our two-class solution)
    newContent.classList.add("fading-in");
    parent.appendChild(newContent);
  
    // Process for htmx bindings (in case the new content has interactive elements)
    htmx.process(newContent);
  
    // Use a double requestAnimationFrame to trigger the transition
    requestAnimationFrame(() => {
        requestAnimationFrame(() => {
            newContent.classList.add("fading-in-active");
        });
    });
  
    // Clean up classes once transition is done
    newContent.addEventListener("transitionend", () => {
        newContent.classList.remove("fading-in", "fading-in-active");
    });
}
  
  
function loadHistory() {
    let history = localStorage.getItem("contentHistory");
    return history ? JSON.parse(history) : [];
}

function saveHistory(history) {
    localStorage.setItem("contentHistory", JSON.stringify(history));
}

function getCurrentIndex() {
    let idx = localStorage.getItem("currentContentIndex");
    return idx !== null ? parseInt(idx, 10) : -1;
}

function setCurrentIndex(index) {
    localStorage.setItem("currentContentIndex", index);
}

function appendNewContentToHistory(newHTML) {
    let history = loadHistory();
    history.push(newHTML);
    saveHistory(history);
    setCurrentIndex(history.length - 1);
    updateNavigationButtons();
}

function updateNavigationButtons() {
    let history = loadHistory();
    let currentIndex = getCurrentIndex();
    let backButton = document.getElementById("back-button");
    let forwardButton = document.getElementById("forward-button");

    if (backButton) {
        if (currentIndex <= 0) {
            backButton.disabled = true;
            backButton.classList.add("hidden");  // Hide button
        } else {
            backButton.disabled = false;
            backButton.classList.remove("hidden");
        }
    }
    if (forwardButton) {
        if (currentIndex >= history.length - 1) {
            forwardButton.disabled = true;
            forwardButton.classList.add("hidden");  // Hide button
        } else {
            forwardButton.disabled = false;
            forwardButton.classList.remove("hidden");
        }
    }
}

// Function to replace current content with newHTML using fade effects
function replaceContent(newHTML) {
    let oldContent = document.getElementById("content");
    if (!oldContent) {
        // If there's no current content, simply insert the new HTML
        insertNewContent(newHTML, document.getElementById("card-container"));
        return;
    }
    let parent = oldContent.parentNode;
    oldContent.classList.add("fading-out");
    oldContent.addEventListener("transitionend", function onEnd(e) {
        if (e.target !== oldContent) return;
        oldContent.removeEventListener("transitionend", onEnd);
        // Remove the old content and insert the new one
        parent.removeChild(oldContent);
        insertNewContent(newHTML, parent);
    });
}

// Navigation functions

function goBack() {
    let history = loadHistory();
    let currentIndex = getCurrentIndex();
    if (currentIndex > 0) {
        currentIndex--;
        setCurrentIndex(currentIndex);
        replaceContent(history[currentIndex]);
        updateNavigationButtons();
    }
}

function goForward() {
    let history = loadHistory();
    let currentIndex = getCurrentIndex();
    if (currentIndex < history.length - 1) {
        currentIndex++;
        setCurrentIndex(currentIndex);
        replaceContent(history[currentIndex]);
        updateNavigationButtons();
    }
}