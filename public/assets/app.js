(() => {
  const root = document.documentElement;
  const savedTheme = localStorage.getItem("picg-theme");
  const videoVolumeKey = "picg-video-volume";
  const videoMutedKey = "picg-video-muted";

  let thumbnailEpoch = 0;
  const maxThumbnailTries = 60;
  const thumbnailRetryDelay = 1000;
  const thumbnailFocusInterval = 15000;
  const maxThumbnailRequests = 4;
  const recentThumbnailEvents = [];
  const thumbnailWaiting = [];
  const thumbnailWaitingSet = new Set();
  let thumbnailInFlight = 0;

  const thumbnailSrc = (image) => image.dataset.thumbnailUrl || image.getAttribute("src") || "";

  const rememberThumbnailUrl = (image, url) => {
    if (!url) return;
    image.dataset.thumbnailUrl = url.replace(/&retry=\d+$/, "");
  };

  const thumbnailParams = (image) => {
    const raw = thumbnailSrc(image);
    if (!raw) return null;
    try {
      const params = new URL(raw, location.origin).searchParams;
      return { kind: params.get("kind") || "", path: params.get("path") || "" };
    } catch {
      return null;
    }
  };

  const scheduleThumbnail = (image, options = {}) => {
    if (!image || image.dataset.thumbnailAborted === "true") return;
    if (image.complete && image.naturalWidth > 0) return;
    const url = thumbnailSrc(image);
    if (!url) return;
    rememberThumbnailUrl(image, url);
    if (options.retry) image.dataset.thumbnailRetry = "true";
    if (image.dataset.thumbnailInFlight === "true") {
      if (options.retry) image.dataset.thumbnailReady = "true";
      return;
    }
    if (thumbnailWaitingSet.has(image)) {
      if (options.prepend) {
        const index = thumbnailWaiting.indexOf(image);
        if (index > 0) {
          thumbnailWaiting.splice(index, 1);
          thumbnailWaiting.unshift(image);
        }
      }
      return;
    }
    if (options.prepend) thumbnailWaiting.unshift(image);
    else thumbnailWaiting.push(image);
    thumbnailWaitingSet.add(image);
    pumpThumbnails();
  };

  const pumpThumbnails = () => {
    while (thumbnailInFlight < maxThumbnailRequests && thumbnailWaiting.length) {
      const image = thumbnailWaiting.shift();
      thumbnailWaitingSet.delete(image);
      if (!image?.isConnected || image.dataset.thumbnailAborted === "true") continue;
      if (image.complete && image.naturalWidth > 0) continue;
      const base = thumbnailSrc(image).replace(/&retry=\d+$/, "");
      if (!base) continue;
      image.dataset.thumbnailInFlight = "true";
      thumbnailInFlight += 1;
      image.dataset.thumbnailRetry = "";
      let url = base;
      if (image.getAttribute("src")) url = base + "&retry=" + Date.now();
      image.src = url;
    }
  };

  const releaseThumbnail = (image) => {
    if (image?.dataset?.thumbnailInFlight !== "true") return;
    image.dataset.thumbnailInFlight = "";
    thumbnailInFlight = Math.max(0, thumbnailInFlight - 1);
    pumpThumbnails();
  };

  const applyThumbnailEvent = (data) => {
    if (!data?.kind) return;
    document.querySelectorAll("img[data-thumbnail]").forEach((image) => {
      if (image.dataset.thumbnailAborted === "true") return;
      const params = thumbnailParams(image);
      if (!params || params.kind !== data.kind || params.path !== data.path) return;
      if (data.status === "error") {
        if (image.matches("[data-folder-cover]") || image.dataset.mediaThumbnail === "video") image.hidden = true;
        return;
      }
      if (image.complete && image.naturalWidth > 0) return;
      scheduleThumbnail(image, { retry: true, prepend: true });
    });
  };

  const rememberThumbnailEvent = (data) => {
    recentThumbnailEvents.push(data);
    if (recentThumbnailEvents.length > 64) recentThumbnailEvents.shift();
  };

  const thumbnailTabId = (() => {
    try {
      if (crypto.randomUUID) return crypto.randomUUID();
    } catch {
    }
    return "tab-" + Math.random().toString(16).slice(2);
  })();

  const reloadIncompleteThumbnails = () => {
    document.querySelectorAll("img[data-thumbnail]").forEach((image) => {
      if (image.dataset.thumbnailAborted === "true") return;
      if (image.complete && image.naturalWidth > 0) return;
      scheduleThumbnail(image, { retry: true, prepend: true });
    });
  };

  const focusThumbnailQueue = () => {
    if (!document.querySelector("#gallery-content")) return;
    const path = document.querySelector("#current-path")?.value ?? "";
    const body = new URLSearchParams({ tab: thumbnailTabId, path });
    fetch("/thumbnail-focus", { method: "POST", body, keepalive: true }).catch(() => {});
  };

  const leaveThumbnailQueue = () => {
    const body = new URLSearchParams({ tab: thumbnailTabId, leave: "1" });
    if (navigator.sendBeacon) {
      navigator.sendBeacon("/thumbnail-focus", body);
      return;
    }
    fetch("/thumbnail-focus", { method: "POST", body, keepalive: true }).catch(() => {});
  };

  const connectThumbnailEvents = () => {
    if (!document.querySelector("#gallery-content")) return;

    const applyPayload = (data) => {
      if (!data?.kind) return;
      rememberThumbnailEvent(data);
      applyThumbnailEvent(data);
    };

    let channel = null;
    if (window.BroadcastChannel) {
      channel = new BroadcastChannel("picg-thumbnail-events");
      channel.addEventListener("message", (event) => {
        const data = event.data;
        if (data?.type === "reconnect") {
          reloadIncompleteThumbnails();
          return;
        }
        applyPayload(data);
      });
    }

    const bindSource = (source) => {
      let opened = false;
      source.addEventListener("open", () => {
        if (opened) reloadIncompleteThumbnails();
        else opened = true;
        channel?.postMessage({ type: "reconnect" });
      });
      source.addEventListener("thumbnail", (event) => {
        let data;
        try {
          data = JSON.parse(event.data);
        } catch {
          return;
        }
        applyPayload(data);
        channel?.postMessage(data);
      });
      return source;
    };

    if (window.EventSource && navigator.locks?.request) {
      navigator.locks.request("picg-thumbnail-events", () => new Promise(() => {
        bindSource(new EventSource("/thumbnail-events"));
      }));
      return;
    }
    if (!window.EventSource) return;

    let source = null;
    const syncSource = () => {
      if (document.hidden) {
        source?.close();
        source = null;
        return;
      }
      if (!source) source = bindSource(new EventSource("/thumbnail-events"));
    };
    document.addEventListener("visibilitychange", syncSource);
    syncSource();
  };

  const prepareThumbnails = (scope = document) => {
    scope.querySelectorAll?.("img[data-thumbnail]").forEach((image) => {
      if (image.complete && image.naturalWidth > 0) image.classList.add("is-loaded");
      else scheduleThumbnail(image);
    });
    recentThumbnailEvents.forEach(applyThumbnailEvent);
  };

  const splitMediaGrids = (scope = document) => {
    scope.querySelectorAll?.("[data-photo-grid]:not([data-media-split])").forEach((grid) => {
      const section = grid.closest(".photo-section");
      const heading = section?.querySelector(":scope > .section-heading");
      const images = [...grid.querySelectorAll('[data-media-kind="image"]')];
      const videos = [...grid.querySelectorAll('[data-media-kind="video"]')];
      if (!section || !heading) return;

      grid.dataset.mediaSplit = "true";
      const setHeading = (target, label, count) => {
        target.querySelector("h2").textContent = label;
        target.querySelector("span").textContent = String(count);
      };

      if (images.length === 0) {
        setHeading(heading, "影片預覽", videos.length);
        return;
      }
      setHeading(heading, "圖片預覽", images.length);
      if (videos.length === 0) return;

      const videoSection = document.createElement("section");
      videoSection.className = "section-block photo-section";
      const videoHeading = heading.cloneNode(true);
      setHeading(videoHeading, "影片預覽", videos.length);
      const videoGrid = grid.cloneNode(false);
      videoGrid.dataset.mediaSplit = "true";
      videos.forEach((video) => videoGrid.append(video));
      videoSection.append(videoHeading, videoGrid);
      section.after(videoSection);
    });
  };

  const cancelThumbnailWork = () => {
    thumbnailEpoch += 1;
    thumbnailWaiting.length = 0;
    thumbnailWaitingSet.clear();
    thumbnailInFlight = 0;
    document.querySelectorAll("img[data-thumbnail]").forEach((image) => {
      if (image.complete && image.naturalWidth > 0) return;
      image.dataset.thumbnailAborted = "true";
      image.dataset.thumbnailInFlight = "";
      image.dataset.thumbnailReady = "";
      image.removeAttribute("src");
    });
  };

  const isGalleryNavigation = (node) => node?.closest?.("a.folder-card, nav.breadcrumbs a[hx-get]");

  if (savedTheme) root.dataset.theme = savedTheme;
  splitMediaGrids();
  prepareThumbnails();
  connectThumbnailEvents();
  focusThumbnailQueue();
  window.setInterval(focusThumbnailQueue, thumbnailFocusInterval);
  window.addEventListener("pagehide", leaveThumbnailQueue);

  const persistSort = (value) => {
    document.cookie = `picg-sort=${encodeURIComponent(value)}; path=/; max-age=31536000; SameSite=Lax`;
  };

  const lightboxDialog = () => document.querySelector("[data-lightbox]");
  const mediaItems = () => [...document.querySelectorAll("[data-photo-src]")];
  const lightboxPreloads = new Map();
  const lightboxVideoStreams = new WeakMap();
  const streamMimeType = 'video/mp4; codecs="avc1.42C02A, mp4a.40.2"';
  const lightboxZoomMin = 1;
  const lightboxZoomMax = 5;
  let lightboxRequest = 0;
  let lightboxZoom = 1;
  let lightboxPanX = 0;
  let lightboxPanY = 0;
  let lightboxPanning = false;
  let lightboxPanStart = null;

  const stopVideoElement = (video) => {
    if (!video) return;
    const stream = lightboxVideoStreams.get(video);
    if (stream) stream.stopped = true;
    stream?.abort?.abort();
    lightboxVideoStreams.delete(video);
    video.pause();
    video.removeAttribute("src");
    video.load();
    if (stream?.objectURL) URL.revokeObjectURL(stream.objectURL);
  };

  const stopLightboxVideo = (dialog) => stopVideoElement(dialog?.querySelector("[data-lightbox-video]"));

  const updateVideoBuffer = (sourceBuffer, update) => new Promise((resolve, reject) => {
    const cleanup = () => {
      sourceBuffer.removeEventListener("updateend", complete);
      sourceBuffer.removeEventListener("error", fail);
      sourceBuffer.removeEventListener("abort", fail);
    };
    const complete = () => {
      cleanup();
      resolve();
    };
    const fail = (event) => {
      cleanup();
      if (event?.type === "abort") {
        reject(new DOMException("video segment aborted", "AbortError"));
        return;
      }
      reject(new Error("unable to append video segment"));
    };
    sourceBuffer.addEventListener("updateend", complete, { once: true });
    sourceBuffer.addEventListener("error", fail, { once: true });
    sourceBuffer.addEventListener("abort", fail, { once: true });
    try {
      update();
    } catch (error) {
      cleanup();
      reject(error);
    }
  });

  const appendVideoChunk = (sourceBuffer, chunk) => updateVideoBuffer(sourceBuffer, () => sourceBuffer.appendBuffer(chunk));

  const trimVideoBuffer = async (sourceBuffer, currentTime) => {
    if (sourceBuffer.buffered.length === 0) return;
    const start = sourceBuffer.buffered.start(0);
    const end = currentTime - 30;
    if (end - start < 30) return;
    await updateVideoBuffer(sourceBuffer, () => sourceBuffer.remove(start, end));
  };

  const videoStreamURL = (src, start) => {
    const streamURL = new URL(src, window.location.href);
    if (start > 0) streamURL.searchParams.set("start", String(start));
    return streamURL;
  };

  const pumpVideoStream = async (video, stream, start) => {
    const generation = ++stream.generation;
    const abort = new AbortController();
    stream.abort = abort;
    const response = await fetch(videoStreamURL(stream.src, start), { cache: "no-store", signal: abort.signal });
    if (!response.ok || !response.body) throw new Error(`video stream returned ${response.status}`);
    if (lightboxVideoStreams.get(video) !== stream || generation !== stream.generation) return;
    const duration = Number(response.headers.get("X-Video-Duration"));
    if (Number.isFinite(duration) && duration > 0) {
      stream.duration = duration;
      stream.mediaSource.duration = duration;
    }
    stream.sourceBuffer.timestampOffset = start;
    const reader = response.body.getReader();
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if (lightboxVideoStreams.get(video) !== stream || generation !== stream.generation) return;
      await appendVideoChunk(stream.sourceBuffer, value);
      await trimVideoBuffer(stream.sourceBuffer, video.currentTime);
    }
    if (lightboxVideoStreams.get(video) === stream && generation === stream.generation && stream.mediaSource.readyState === "open" && !stream.sourceBuffer.updating) {
      stream.mediaSource.endOfStream();
    }
  };

  const streamVideo = async (video, src, autoplay = true) => {
    stopVideoElement(video);
    if (!window.MediaSource || !MediaSource.isTypeSupported(streamMimeType)) {
      video.src = videoStreamURL(src, 0);
      if (autoplay) await video.play();
      return;
    }

    const mediaSource = new MediaSource();
    const objectURL = URL.createObjectURL(mediaSource);
    const stream = { abort: null, generation: 0, mediaSource, objectURL, sourceBuffer: null, src, stopped: false };
    lightboxVideoStreams.set(video, stream);
    video.src = objectURL;
    if (autoplay) void video.play().catch(() => {});
    const sourceBuffer = await new Promise((resolve, reject) => {
      const fail = () => reject(new DOMException("video stream closed", "AbortError"));
      const open = () => {
        mediaSource.removeEventListener("sourceclose", fail);
        try {
          resolve(mediaSource.addSourceBuffer(streamMimeType));
        } catch (error) {
          reject(error);
        }
      };
      mediaSource.addEventListener("sourceclose", fail, { once: true });
      mediaSource.addEventListener("sourceopen", open, { once: true });
    });
    if (stream.stopped || lightboxVideoStreams.get(video) !== stream) return;
    stream.sourceBuffer = sourceBuffer;
    await pumpVideoStream(video, stream, 0);
  };

  const seekVideoStream = async (video, stream, start, autoplay) => {
    stream.abort?.abort();
    stream.generation += 1;
    if (stream.mediaSource.readyState === "ended" && stream.duration) stream.mediaSource.duration = stream.duration;
    if (stream.mediaSource.readyState === "open") stream.sourceBuffer.abort();
    if (autoplay) void video.play().catch(() => {});
    await pumpVideoStream(video, stream, start);
  };

  const isVideoTimeBuffered = (video, time) => {
    for (let index = 0; index < video.buffered.length; index += 1) {
      if (time >= video.buffered.start(index) && time <= video.buffered.end(index)) return true;
    }
    return false;
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

  const lightboxParts = (dialog) => ({
    image: dialog.querySelector("[data-lightbox-image]"),
    video: dialog.querySelector("[data-lightbox-video]"),
    stage: dialog.querySelector("[data-lightbox-stage]"),
    slider: dialog.querySelector("[data-lightbox-zoom]"),
    label: dialog.querySelector("[data-lightbox-zoom-label]"),
    bar: dialog.querySelector("[data-lightbox-zoom-bar]"),
  });

  const applyLightboxView = () => {
    const dialog = lightboxDialog();
    if (!dialog) return;
    const { image, slider, label, stage } = lightboxParts(dialog);
    if (image) image.style.transform = `translate(${lightboxPanX}px, ${lightboxPanY}px) scale(${lightboxZoom})`;
    const percent = Math.round(lightboxZoom * 100);
    if (slider && Number(slider.value) !== percent) slider.value = String(percent);
    if (label) label.textContent = `${percent}%`;
    stage?.classList.toggle("is-zoomed", lightboxZoom > 1.001);
    if (lightboxZoom <= 1) stage?.classList.remove("is-panning");
  };

  const resetLightboxView = () => {
    lightboxZoom = 1;
    lightboxPanX = 0;
    lightboxPanY = 0;
    lightboxPanning = false;
    lightboxPanStart = null;
    applyLightboxView();
  };

  const setLightboxZoom = (value, clientX, clientY) => {
    const dialog = lightboxDialog();
    if (!dialog?.open) return;
    const { image, stage } = lightboxParts(dialog);
    if (!image || image.hidden) return;
    const next = Math.min(lightboxZoomMax, Math.max(lightboxZoomMin, value));
    if (clientX != null && clientY != null && stage && lightboxZoom > 0) {
      const rect = stage.getBoundingClientRect();
      const cx = clientX - (rect.left + rect.width / 2);
      const cy = clientY - (rect.top + rect.height / 2);
      const ratio = next / lightboxZoom;
      lightboxPanX = cx - (cx - lightboxPanX) * ratio;
      lightboxPanY = cy - (cy - lightboxPanY) * ratio;
    }
    lightboxZoom = next;
    if (lightboxZoom <= 1.001) {
      lightboxZoom = 1;
      lightboxPanX = 0;
      lightboxPanY = 0;
    }
    applyLightboxView();
  };

  const applySavedVideoVolume = (video) => {
    const savedVolume = Number(localStorage.getItem(videoVolumeKey));
    if (Number.isFinite(savedVolume)) video.volume = Math.min(1, Math.max(0, savedVolume));
    video.muted = localStorage.getItem(videoMutedKey) === "1";
  };

  const persistVideoVolume = (video) => {
    localStorage.setItem(videoVolumeKey, String(video.volume));
    localStorage.setItem(videoMutedKey, video.muted ? "1" : "0");
  };

  const paintLightbox = (dialog, photo, image, video) => {
    const isVideo = photo.dataset.mediaKind === "video";
    const chrome = dialog.querySelector("[data-lightbox-chrome]");
    const bar = dialog.querySelector("[data-lightbox-zoom-bar]");
    dialog.querySelector("[data-lightbox-title]").textContent = photo.dataset.photoTitle;
    dialog.querySelector("[data-lightbox-meta]").textContent = photo.dataset.photoMeta;
    if (chrome) chrome.hidden = isVideo;
    if (bar) bar.hidden = isVideo;
    resetLightboxView();
    if (isVideo) {
      clearLightboxImage(image);
      video.hidden = false;
      applySavedVideoVolume(video);
      streamVideo(video, photo.dataset.photoSrc).catch((error) => {
        if (error?.name !== "AbortError") console.error("video stream failed", error);
      });
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
    resetLightboxView();
    dialog?.close();
    prepareThumbnails();
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
      document.querySelectorAll("[data-photo-grid]").forEach((grid) => grid.classList.toggle("compact"));
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

    if (event.target.closest("[data-lightbox-chrome], [data-lightbox-zoom-bar]")) return;

    if (event.target.closest("[data-lightbox-close]") || event.target === dialog) {
      closeLightbox();
    }
  });

  document.addEventListener("keydown", (event) => {
    if ((event.key === "Enter" || event.key === " ") && isGalleryNavigation(event.target)) {
      cancelThumbnailWork();
    }
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
    const image = event.target;
    if (!image?.matches?.("img[data-thumbnail]")) return;
    if (image.dataset.thumbnailAborted === "true") return;
    const url = image.getAttribute("src");
    if (!url) return;
    rememberThumbnailUrl(image, url);
    const ready = image.dataset.thumbnailReady === "true";
    image.dataset.thumbnailReady = "";
    releaseThumbnail(image);
    if (ready) {
      scheduleThumbnail(image, { retry: true, prepend: true });
      return;
    }
    if (window.EventSource) return;
    const tries = Number(image.dataset.thumbnailTries || "0");
    if (tries >= maxThumbnailTries) {
      if (image.matches("[data-folder-cover]") || image.dataset.mediaThumbnail === "video") image.hidden = true;
      return;
    }
    image.dataset.thumbnailTries = String(tries + 1);
    const epoch = thumbnailEpoch;
    window.setTimeout(() => {
      if (epoch !== thumbnailEpoch || !image.isConnected || image.dataset.thumbnailAborted === "true") return;
      scheduleThumbnail(image, { retry: true });
    }, thumbnailRetryDelay);
  }, true);

  document.addEventListener("load", (event) => {
    const image = event.target;
    if (!image?.matches?.("img[data-thumbnail]")) return;
    image.dataset.thumbnailReady = "";
    image.classList.add("is-loaded");
    releaseThumbnail(image);
  }, true);

  document.addEventListener("volumechange", (event) => {
    if (!event.target.matches?.("[data-lightbox-video]")) return;
    persistVideoVolume(event.target);
  }, true);

  document.addEventListener("seeking", (event) => {
    const video = event.target;
    if (!video.matches?.("[data-lightbox-video]")) return;
    const stream = lightboxVideoStreams.get(video);
    if (!stream || isVideoTimeBuffered(video, video.currentTime)) return;
    const upperBound = stream.duration ? Math.max(0, stream.duration - 0.25) : video.currentTime;
    const target = Math.min(video.currentTime, upperBound);
    seekVideoStream(video, stream, target, !video.paused).catch((error) => {
      if (error?.name !== "AbortError") console.error("video seek failed", error);
    });
  }, true);

  document.addEventListener("change", (event) => {
    if (!event.target.matches?.("#gallery-sort")) return;
    persistSort(event.target.value);
  }, true);

  document.addEventListener("input", (event) => {
    if (!event.target.matches?.("[data-lightbox-zoom]")) return;
    setLightboxZoom(Number(event.target.value) / 100);
  });

  document.addEventListener("wheel", (event) => {
    const dialog = lightboxDialog();
    if (!dialog?.open) return;
    event.preventDefault();
    const image = dialog.querySelector("[data-lightbox-image]");
    if (!image || image.hidden) return;
    const factor = event.deltaY < 0 ? 1.12 : 1 / 1.12;
    setLightboxZoom(lightboxZoom * factor, event.clientX, event.clientY);
  }, { passive: false, capture: true });

  document.addEventListener("pointerdown", (event) => {
    if (isGalleryNavigation(event.target)) cancelThumbnailWork();
    const dialog = lightboxDialog();
    if (!dialog?.open || lightboxZoom <= 1) return;
    if (!event.target.closest("[data-lightbox-image]")) return;
    event.preventDefault();
    lightboxPanning = true;
    lightboxPanStart = { x: event.clientX, y: event.clientY, panX: lightboxPanX, panY: lightboxPanY };
    dialog.querySelector("[data-lightbox-stage]")?.classList.add("is-panning");
    event.target.setPointerCapture?.(event.pointerId);
  }, true);

  document.addEventListener("pointermove", (event) => {
    if (!lightboxPanning || !lightboxPanStart) return;
    lightboxPanX = lightboxPanStart.panX + (event.clientX - lightboxPanStart.x);
    lightboxPanY = lightboxPanStart.panY + (event.clientY - lightboxPanStart.y);
    applyLightboxView();
  });

  document.addEventListener("pointerup", () => {
    if (!lightboxPanning) return;
    lightboxPanning = false;
    lightboxPanStart = null;
    lightboxDialog()?.querySelector("[data-lightbox-stage]")?.classList.remove("is-panning");
  });

  document.addEventListener("htmx:beforeRequest", cancelThumbnailWork);

  document.addEventListener("htmx:afterSwap", (event) => {
    splitMediaGrids(event.target);
    prepareThumbnails(event.target);
    focusThumbnailQueue();
    window.scrollTo({ top: 0, behavior: "smooth" });
  });

  document.addEventListener("visibilitychange", () => {
    if (document.hidden) return;
    focusThumbnailQueue();
    prepareThumbnails();
  });
})();
