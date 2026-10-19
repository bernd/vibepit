// Mounts an asciinema player on every <div class="vp-cast" data-cast="...">.
// The recordings are generated with `make docs-casts`.
function mountCasts() {
  // Leave the empty boxes rather than break other page scripts.
  if (typeof AsciinemaPlayer === "undefined") {
    return;
  }
  document.querySelectorAll(".vp-cast[data-cast]").forEach((el) => {
    if (el.dataset.mounted) {
      return;
    }
    el.dataset.mounted = "true";
    AsciinemaPlayer.create(el.dataset.cast, el, {
      theme: "vibepit",
      fit: "width",
      preload: true,
      idleTimeLimit: 2,
      autoPlay: el.dataset.autoplay === "true",
      loop: el.dataset.loop === "true",
      poster: el.dataset.poster,
    });
  });
}

// Material's document$ also fires after instant navigation, if enabled.
if (typeof document$ !== "undefined") {
  document$.subscribe(mountCasts);
} else {
  document.addEventListener("DOMContentLoaded", mountCasts);
}
