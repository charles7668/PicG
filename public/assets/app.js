(() => {
  const root = document.documentElement;
  const savedTheme = localStorage.getItem("picg-theme");

  let thumbnailAbort = new AbortController();
  let thumbnailActive = 0;
  const thumbnailWait = [];
  const thumbnailLimit = 3;

  const pumpThumbnails = () => {
    while (thumbnailActive < thumbnailLimit && thumbnailWait.length > 0) {
      thumbnailWait.shift()();
    }
  };

  const cancelThumbnailWork = () => {
    thumbnailWait.length = 0;
    thumbnailAbort.abort();
    thumbnailAbort = new AbortController();
    document.querySelectorAll("img").forEach((image) => {
      if (!image.complete) image.removeAttribute("src");
    });
  };

  const requestGeneratedThumbnail = (image) => {
    const generatedSource = image.dataset.thumbnailSrc;
    if (!generatedSource || image.dataset.thumbnailLoading === "true") return;
    image.dataset.thumbnailLoading = "true";
    thumbnailWait.push(() => {
      if (!image.isConnected || image.dataset.thumbnailSrc !== generatedSource) {
        delete image.dataset.thumbnailLoading;
        pumpThumbnails();
        return;
      }
      thumbnailActive += 1;
      fetch(generatedSource, { signal: thumbnailAbort.signal, priority: "low" })
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
        })
        .finally(() => {
          thumbnailActive -= 1;
          pumpThumbnails();
        });
    });
    pumpThumbnails();
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
  const lightboxPreloads = new Map();
  let lightboxRequest = 0;

  const stopLightboxVideo = (dialog) => {
    const video = dialog?.querySelector("[data-lightbox-video]");
    video?.pause();
    if (video) video.removeAttribute("src");
  };

  const clearLightboxImage = (image) => {
    if (!image) return;
    image.hidden = true;
    image.removeAttribute("src");
    image.alt = "";
  };

  const preloadLightboxSrc = (src) => {
    if (!src) return null;
    const existing = lightboxPreloads.get(src);
    if (existing) {
      lightboxPreloads.delete(src);
      lightboxPreloads.set(src, existing);
      return existing;
    }
    const loader = new Image();
    loader.decoding = "async";
    loader.src = src;
    lightboxPreloads.set(src, loader);
    while (lightboxPreloads.size > 8) {
      lightboxPreloads.delete(lightboxPreloads.keys().next().value);
    }
    return loader;
  };

  const preloadLightboxNeighbors = (index, items) => {
    if (items.length < 2) return;
    for (const offset of [-1, 1]) {
      const photo = items[(index + offset + items.length) % items.length];
      if (photo.dataset.mediaKind !== "video") preloadLightboxSrc(photo.dataset.photoSrc);
    }
  };

  const whenImageReady = (src, done) => {
    const loader = preloadLightboxSrc(src);
    const finish = () => done();
    if (!loader) {
      finish();
      return;
    }
    const afterLoad = () => {
      if (typeof loader.decode === "function") {
        loader.decode().then(finish, finish);
        return;
      }
      finish();
    };
    loader.addEventListener("load", afterLoad, { once: true });
    loader.addEventListener("error", finish, { once: true });
    if (loader.complete) afterLoad();
  };

  const paintLightbox = (dialog, photo, image, video) => {
    const isVideo = photo.dataset.mediaKind === "video";
    dialog.querySelector("[data-lightbox-title]").textContent = photo.dataset.photoTitle;
    dialog.querySelector("[data-lightbox-meta]").textContent = photo.dataset.photoMeta;
    if (isVideo) {
      clearLightboxImage(image);
      video.hidden = false;
      video.src = photo.dataset.photoSrc;
      video.play().catch(() => {});
      return;
    }
    stopLightboxVideo(dialog);
    video.hidden = true;
    image.alt = photo.dataset.photoTitle;
    image.src = photo.dataset.photoSrc;
    image.hidden = false;
  };

  const closeLightbox = () => {
    lightboxRequest += 1;
    lightboxPreloads.clear();
    const dialog = lightboxDialog();
    stopLightboxVideo(dialog);
    clearLightboxImage(dialog?.querySelector("[data-lightbox-image]"));
    if (dialog) {
      dialog.querySelector("[data-lightbox-title]").textContent = "";
      dialog.querySelector("[data-lightbox-meta]").textContent = "";
    }
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
    const request = ++lightboxRequest;
    cancelThumbnailWork();
    dialog.dataset.lightboxIndex = String(next);
    const showNav = items.length > 1;
    dialog.querySelector("[data-lightbox-prev]").hidden = !showNav;
    dialog.querySelector("[data-lightbox-next]").hidden = !showNav;
    if (dialog.isConnected && !dialog.open) dialog.showModal();
    if (photo.dataset.mediaKind !== "video") preloadLightboxSrc(photo.dataset.photoSrc);
    preloadLightboxNeighbors(next, items);
    const apply = () => {
      if (request !== lightboxRequest) return;
      paintLightbox(dialog, photo, image, video);
    };
    if (photo.dataset.mediaKind === "video") {
      apply();
      return;
    }
    whenImageReady(photo.dataset.photoSrc, apply);
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

  document.addEventListener("pointerdown", (event) => {
    if (event.target.closest("a.folder-card, .breadcrumbs a[hx-get]")) cancelThumbnailWork();
  }, true);

  document.addEventListener("htmx:beforeRequest", cancelThumbnailWork);

  document.addEventListener("htmx:afterSwap", (event) => {
    prepareThumbnails(event.target);
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
})();
