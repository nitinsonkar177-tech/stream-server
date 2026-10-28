#!/usr/bin/env bash
# Synthetic MP4 regression: H264 and AAC440Hz/AAC880Hz/AC3660Hz.
set -euo pipefail
cd "$(dirname "$0")"
ffmpeg -hide_banner -loglevel error -y \
 -f lavfi -i 'testsrc2=size=320x180:rate=24:duration=8' \
 -f lavfi -i 'sine=frequency=440:duration=8:sample_rate=48000' \
 -f lavfi -i 'sine=frequency=880:duration=8:sample_rate=48000' \
 -f lavfi -i 'sine=frequency=660:duration=8:sample_rate=48000' \
 -map 0:v -map 1:a -map 2:a -map 3:a -c:v libx264 -preset ultrafast -crf 32 -g 24 \
 -c:a:0 aac -c:a:1 aac -c:a:2 ac3 -b:a 64k \
 -metadata:s:a:0 language=eng -metadata:s:a:1 language=hin \
 -disposition:a:0 default -disposition:a:1 0 -disposition:a:2 0 \
 -movflags +faststart multi-track.mp4
