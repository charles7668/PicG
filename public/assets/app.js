(() => {
  const root = document.documentElement;
  const savedTheme = localStorage.getItem("picg-theme");

  let thumbnailAbort = new AbortController();

  const requestGeneratedThumbnail = (image) => {
    const generatedSource = image.dataset.thumbnailSrc;
    if (!generatedSource || image.dataset.thumbnailLoading === "true") return;
    image.dataset.thumbnailLoading = "true";
    fetch(generatedSource, { signal: thumbnailAbort.signal })
      .then((response) => {
        if (!response.ok) throw new Error("thumbnail");
        if (!image.isConnected) return;
        delete image.dataset.thumbnailSrc;
        delete image.dataset.thumbnailLoading;
        image.hidden = false;
        image.src = generatedSource;
        image.classList.add("is-loaded");
      })
      .catch((error) => {
        if (error?.name === "AbortError") return;
        delete image.dataset.thumbnailLoading;
      });
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

  const lightboxDialog = () => document.querySelector("[data-lightbox]");
  const mediaItems = () => [...document.querySelectorAll("[data-photo-src]")];

  const stopLightboxVideo = (dialog) => {
    const video = dialog?.querySelector("[data-lightbox-video]");
    video?.pause();
    if (video) video.removeAttribute("src");
  };

  const closeLightbox = () => {
    const dialog = lightboxDialog();
    stopLightboxVideo(dialog);
    dialog?.close();
  };

  const showMediaAt = (index) => {
    const dialog = lightboxDialog();
    const items = mediaItems();
    if (!dialog || items.length === 0) return;
    const next = ((index % items.length) + items.length) % items.length;
    const photo = items[next];
    const image = dialog.querySelector("[data-lightbox-image]");
    const video = dialog.querySelector("[data-lightbox-video]");
    const isVideo = photo.dataset.mediaKind === "video";
    video.pause();
    if (isVideo) {
      image.removeAttribute("src");
      image.hidden = true;
      video.hidden = false;
      video.src = photo.dataset.photoSrc;
    } else {
      video.removeAttribute("src");
      video.hidden = true;
      image.hidden = false;
      image.src = photo.dataset.photoSrc;
      image.alt = photo.dataset.photoTitle;
    }
    dialog.querySelector("[data-lightbox-title]").textContent = photo.dataset.photoTitle;
    dialog.querySelector("[data-lightbox-meta]").textContent = photo.dataset.photoMeta;
    dialog.dataset.lightboxIndex = String(next);
    const showNav = items.length > 1;
    dialog.querySelector("[data-lightbox-prev]").hidden = !showNav;
    dialog.querySelector("[data-lightbox-next]").hidden = !showNav;
    if (dialog.isConnected && !dialog.open) dialog.showModal();
    if (isVideo) video.play().catch(() => {});
  };

  const stepLightbox = (delta) => {
    const dialog = lightboxDialog();
    if (!dialog?.open) return;
    const items = mediaItems();
    if (items.length < 2) return;
    showMediaAt(Number(dialog.dataset.lightboxIndex || 0) + delta);
  };

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

    const dialog = lightboxDialog();
    const photo = event.target.closest("[data-photo-src]");
    if (photo && dialog) {
      const index = mediaItems().indexOf(photo);
      if (index >= 0) showMediaAt(index);
      return;
    }

    if (event.target.closest("[data-lightbox-prev]")) {
      stepLightbox(-1);
      return;
    }
    if (event.target.closest("[data-lightbox-next]")) {
      stepLightbox(1);
      return;
    }

    if (event.target.closest("[data-lightbox-close]") || event.target === dialog) {
      closeLightbox();
    }
  });

  document.addEventListener("keydown", (event) => {
    const dialog = lightboxDialog();
    if (!dialog?.open) return;
    if (event.key === "Escape") {
      closeLightbox();
      return;
    }
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      stepLightbox(-1);
      return;
    }
    if (event.key === "ArrowRight") {
      event.preventDefault();
      stepLightbox(1);
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
    thumbnailAbort.abort();
    thumbnailAbort = new AbortController();
    prepareThumbnails(event.target);
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
})();
