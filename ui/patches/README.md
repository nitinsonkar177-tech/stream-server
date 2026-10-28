# @vplayer/react 0.4.4 MP4 audio patch

The pinned package embeds its core implementation in `dist/index.js`. Its opt-in embedded-track path only accepts Matroska/WebM, although its existing bounded RemuxSession uses Mediabunny ALL_FORMATS and can remux MP4. Thus multi-audio MP4 has no Audio menu in Chromium even with embedded tracks enabled.

The Bun patch extends that opt-in gate to MP4/M4V/MOV. When the Matroska parser rejects an ISO BMFF URL, use the existing generic remux session. A single directly playable track still falls back to native playback. Unsupported or damaged files retain the existing fallback. No server-side transcoding, account preference, or public-share permission changes.

Only the React distribution is patched because this application imports @vplayer/react, which bundles its own core. Do not widen the exported @vplayer/core API based on this patch. Re-evaluate/remove the patch when upgrading this exact pinned version.

`e2e/player-embedded-tracks.spec.ts` checks real audio frequencies for AAC English/Hindi and AC3 conversion on a generated MP4, plus seek after switching. Fixture generator: `e2e/fixtures/player/generate-mp4.sh`. Existing MKV, single-track and settings/privacy regressions also apply. These are isolated UI tests, not Telegram transport or MX tests.
