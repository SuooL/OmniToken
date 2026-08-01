"use strict";
// Methodology notes, folded behind an `i` (ADR-0024).
//
// A card that explains how its number is computed is explaining something you
// read once. Left as body text it costs its width on every render forever, and
// the longest of them ran six lines — pushing the data it described below the
// fold. The note is not deleted, it is one click away.
//
// Click, not hover: hover has no touch equivalent, and a tip that cannot be
// opened on a phone is a tip that is gone. The button is a real <button>, so
// it is in the tab order and answers Enter and Space without any extra code.

// Every open tip is registered here so opening one closes the others and a
// document-level Escape can close whatever is showing.
const InfoTips = {
  open: null,

  // infoTip returns the markup for one note. `html` is inserted as-is, since
  // these notes carry <b> and <code>; callers pass literals, never user data.
  markup(html, label = "口径说明") {
    const id = `infotip-${++this._seq}`;
    return `<span class="infotip-wrap">` +
      `<button type="button" class="infotip-btn" aria-label="${label}" ` +
      `aria-expanded="false" aria-controls="${id}">i</button>` +
      `<span class="infotip-pop" id="${id}" role="tooltip" hidden>${html}</span>` +
      `</span>`;
  },
  _seq: 0,

  close() {
    if (!this.open) return;
    this.open.button.setAttribute("aria-expanded", "false");
    this.open.pop.hidden = true;
    this.open = null;
  },

  toggle(button) {
    const pop = document.getElementById(button.getAttribute("aria-controls"));
    if (!pop) return;
    const wasOpen = this.open && this.open.pop === pop;
    this.close();
    if (wasOpen) return;
    button.setAttribute("aria-expanded", "true");
    pop.hidden = false;
    this.open = { button, pop };
    // A tip near the right edge would otherwise be clipped by the card.
    pop.classList.toggle("flip",
      pop.getBoundingClientRect().right > document.documentElement.clientWidth - 8);
  },

  // One listener for the whole document: the views rebuild their innerHTML on
  // every poll, so anything bound to an individual button would be dropped.
  install() {
    document.addEventListener("click", (event) => {
      const button = event.target.closest(".infotip-btn");
      if (button) {
        event.preventDefault();
        this.toggle(button);
        return;
      }
      if (!event.target.closest(".infotip-pop")) this.close();
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") this.close();
    });
  },
};

function infoTip(html, label) {
  return InfoTips.markup(html, label);
}

InfoTips.install();
