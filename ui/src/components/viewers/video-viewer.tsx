import { DEFAULT_CAPTION_SETTINGS, VideoPlayer } from "@vplayer/react";
import "@vplayer/react/player.css";
import { useEffect, useRef, useState } from "react";
import type { FileEntry } from "@/api/types";
import { previewMedia } from "@/features/files/preview-support";
import { useAppLocale } from "@/lib/locale";
import { useEmbeddedTracks } from "@/lib/player-settings";
import { usePreferences } from "@/lib/preferences";

export function VideoViewer({
  file,
  url,
  embeddedTracks: shareAllows,
}: {
  file: FileEntry;
  url: string;
  /** Set on a public share: the owner's choice, which then replaces the browser setting. */
  embeddedTracks?: boolean;
}) {
  const rootRef = useRef<HTMLDivElement>(null);
  const { locale, t } = useAppLocale();
  const captions = usePreferences()?.captions ?? DEFAULT_CAPTION_SETTINGS;
  // Off unless it is turned on in Settings → Player (on the account, or by the owner for their
  // public shares); while it is off the player does not look inside the file at all.
  const [browserSetting] = useEmbeddedTracks();
  const embeddedTracks = shareAllows ?? browserSetting;


  // Some browsers cannot decode a file's video track at all (for example HEVC without
  // hardware support): the element still plays audio and advances its clock while decoding
  // zero video frames, leaving a silent black picture. Detect that and say so honestly.
  const [undecodable, setUndecodable] = useState(false);
  const [noticeDismissed, setNoticeDismissed] = useState(false);
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    let interval: number | undefined;
    const attach = (video: HTMLVideoElement) => {
      let firstTime: number | null = null;
      interval = window.setInterval(() => {
        if (video.paused || video.ended || video.error) {
          firstTime = null;
          return;
        }
        if (firstTime === null) {
          firstTime = video.currentTime;
          return;
        }
        const frames = video.getVideoPlaybackQuality?.().totalVideoFrames;
        const noFrames = frames === undefined ? video.videoWidth === 0 : frames === 0;
        if (!noFrames) {
          firstTime = null;
          setUndecodable(false);
          return;
        }
        if (video.currentTime - firstTime > 2.5) setUndecodable(true);
      }, 1000);
    };
    const existing = root.querySelector("video");
    if (existing) {
      attach(existing);
      return () => window.clearInterval(interval);
    }
    const observer = new MutationObserver(() => {
      const video = root.querySelector("video");
      if (video) {
        observer.disconnect();
        attach(video);
      }
    });
    observer.observe(root, { childList: true, subtree: true });
    return () => {
      observer.disconnect();
      window.clearInterval(interval);
    };
  }, [url]);

  useEffect(() => {
    const root = rootRef.current;
    return () => {
      const video = root?.querySelector("video");
      if (!video) return;
      video.pause();
      video.removeAttribute("src");
      video.load();
    };
  }, []);

  return (
    <div ref={rootRef} className="relative flex h-full w-full items-center justify-center bg-black">
      {undecodable && !noticeDismissed && (
        <div
          role="status"
          data-codec-notice
          className="absolute inset-x-3 top-3 z-10 mx-auto flex max-w-lg items-start gap-3 rounded-lg border border-zinc-600/60 bg-zinc-900/95 px-4 py-3 text-sm text-zinc-100 shadow-lg"
        >
          <span className="flex-1">{t("player.videoUndecodable")}</span>
          <button
            type="button"
            aria-label={t("player.dismissNotice")}
            className="shrink-0 rounded px-1 text-zinc-400 hover:text-zinc-100"
            onClick={() => setNoticeDismissed(true)}
          >
            ✕
          </button>
        </div>
      )}
      {/* The player wraps itself in one div whose height is automatic; let it fill instead. */}
      <div className="h-full w-full max-w-384 [&>div]:h-full!">
        <VideoPlayer
          src={url}
          type={previewMedia(file)?.type || file.mimeType}
          title={file.name}
          autoPlay
          defaultHotkeys
          persistPreferences
          lang={locale}
          captionSettings={captions}
          captionSettingsMenu={false}
          embeddedTracks={embeddedTracks}
          // Fill the window and letterbox the picture inside, instead of the player's own
          // fixed 16:9 box, which left a small strip on a phone and overran a short window.
          className="aspect-auto! h-full max-h-none! w-full"
        />
      </div>
    </div>
  );
}
