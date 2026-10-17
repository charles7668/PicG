(() => {
  const root = document.documentElement;
  const savedTheme = localStorage.getItem("picg-theme");

  const requestGeneratedThumbnail = (image) => {
    const generatedSource = image.dataset.thumbnailSrc;
    if (!generatedSource || image.dataset.thumbnailLoading === "true") return;
    image.dataset.thumbnailLoading = "true";
    const generated = new Image();
    generated.decoding = "async";
    generated.onload = () => {
      if (!image.isConnected) return;
      delete image.dataset.thumbnailSrc;
      delete image.dataset.thumbnailLoading;
      image.hidden = false;
      image.src = generatedSource;
      image.classList.add("is-loaded");
    };
    generated.onerror = () => {
      delete image.dataset.thumbnailLoading;
    };
    generated.src = generatedSource;
  };

  const thumbnailObserver = "IntersectionObserver" in window
    ? new IntersectionObserver((entries) => {
        entries.forEach((entry) => {
          if (!entry.isIntersecting) return;
          thumbnailObserver.unobserve(entry.target);
          requestGeneratedThumbnail(entry.target);
        });
      }, { rootMargin: "300px" })
    : null;

  const prepareThumbnails = (scope = document) => {
    scope.querySelectorAll?.("img[data-thumbnail]").forEach((image) => {
      if (image.complete && image.naturalWidth > 0) image.classList.add("is-loaded");
      if (!image.dataset.thumbnailSrc) return;
      if (thumbnailObserver) {
        thumbnailObserver.observe(image);
      } else {
        requestGeneratedThumbnail(image);
      }
    });
  };

  if (savedTheme) root.dataset.theme = savedTheme;
  prepareThumbnails();

  document.addEventListener("click", (event) => {
    if (event.target.closest("[data-theme-toggle]")) {
      const next = root.dataset.theme === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      localStorage.setItem("picg-theme", next);
      return;
    }

    if (event.target.closest("[data-view-toggle]")) {
      document.querySelector("[data-photo-grid]")?.classList.toggle("compact");
      return;
    }

    const lightbox = document.querySelector("[data-lightbox]");
    const photo = event.target.closest("[data-photo-src]");
    if (photo && lightbox) {
      const image = lightbox.querySelector("[data-lightbox-image]");
      const video = lightbox.querySelector("[data-lightbox-video]");
      const isVideo = photo.dataset.mediaKind === "video";
      image.hidden = isVideo;
      video.hidden = !isVideo;
      if (isVideo) {
        video.src = photo.dataset.photoSrc;
      } else {
        video.pause();
        video.removeAttribute("src");
        image.src = photo.dataset.photoSrc;
        image.alt = photo.dataset.photoTitle;
      }
      lightbox.querySelector("[data-lightbox-title]").textContent = photo.dataset.photoTitle;
      lightbox.querySelector("[data-lightbox-meta]").textContent = photo.dataset.photoMeta;
      if (lightbox.isConnected && !lightbox.open) lightbox.showModal();
      if (isVideo) video.play().catch(() => {});
      return;
    }

    if (event.target.closest("[data-lightbox-close]") || event.target === lightbox) {
      const video = lightbox?.querySelector("[data-lightbox-video]");
      video?.pause();
      if (video) video.removeAttribute("src");
      lightbox?.close();
    }
  });

  document.addEventListener("keydown", (event) => {
    const lightbox = document.querySelector("[data-lightbox]");
    if (event.key === "Escape" && lightbox?.open) {
      const video = lightbox.querySelector("[data-lightbox-video]");
      video.pause();
      video.removeAttribute("src");
      lightbox.close();
    }
  });

  document.addEventListener("error", (event) => {
    if (event.target.dataset?.thumbnailSrc) return;
    if (event.target.matches?.("[data-folder-cover]")) event.target.hidden = true;
    if (event.target.dataset?.mediaThumbnail === "video") event.target.hidden = true;
  }, true);

  document.addEventListener("load", (event) => {
    if (event.target.matches?.("img[data-thumbnail]")) event.target.classList.add("is-loaded");
  }, true);

  document.addEventListener("htmx:afterSwap", (event) => {
    prepareThumbnails(event.target);
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
})();
