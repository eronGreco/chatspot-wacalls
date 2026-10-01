# Mixed calls and server video recording (alpha.9)

## Operational capability

`GET /api/config` advertises `mediaHistoryVersion: 1`, `mixedCalls: true`,
`recordingClock: "server_connected"` and `videoRecordingSupported: true` only
when recording is enabled, VIDEO_RECORDING_ENABLED is set, and FFmpeg/ffprobe
with libx264/AAC have been validated at startup. Missing required encoders
fail configuration instead of advertising a recording that cannot be made.

All recording, persistence, composition and uploads run in WaCalls. The browser
supplies live camera/microphone for the conversation, never MediaRecorder,
recording files, local recording storage, uploads or an upload fallback.
Inbound capture does not depend on the browser video sink being attached.

## Media history and camera transitions

`call-list`, `call-status`, `call-ended` and call GET responses carry optional
`mediaHistory`: `{version:1, connectedAt, endedAt?, profile, segments, truncated}`.
Connection/end times are Unix milliseconds. Segment
`{startMs,endMs?,media,localVideo,remoteVideo}` offsets share the PCM recording
origin. Media is video when either negotiated camera is active; requests and
rejected upgrades are excluded. The whole-call profile is audio/video/mixed,
independent of current media. Turning one camera off leaves the other active.

Identical states deduplicate and zero-duration transitions replace boundaries.
History is bounded at 512 segments; `truncated` does not change the whole-call
classification. Persisted snapshots are account/call scoped and monotonic.
Legacy records without history remain unknown. A video badge never proves a
recording exists: file/status/partial flags are separate.

Upgrade/accept/reject/stop endpoints retain owner checks. Receive-only accept
uses `{sendVideo:false}`. Video remains on the same PCM PeerConnection. Camera
stop cannot be undone by late accept signaling. Real WhatsApp upgrades and
orientation changes must still be validated with a new end-to-end test.

## Persistent encoded capture

A server-only tap receives authenticated inbound H.264 access units with RTP
clock/SSRC and orientation. Outbound live H.264 comes from the owned WebRTC
bridge with encoder timestamps. Each stream anchors its source clock to the
shared connection clock, retaining variable presentation timing rather than
concatenating raw Annex-B at an assumed frame rate. Wraparound, resets,
SSRC changes, >1-second arrival gaps, orientation changes, SPS/PPS changes and
camera epochs require a new independently decodable segment starting at IDR.
A keyframe is requested when the connected recording starts.

The capture goroutine writes streaming Matroska Clusters without decoding or
encoding live pixels. Segments contain AVC decoder configuration and actual
SPS dimensions. The small journal records filenames, direction, start/end,
orientation, capture limits and errors. File/journal sync occurs approximately
every 2 seconds and on completion. After an interrupted capture ffprobe streams
persisted packet timestamps to recover the last frames; restart downtime is
never added to the recovered movie or the recovered audio/media end time. An interrupted recording is marked partial.
A sudden power failure can lose data not yet flushed during that interval.

Capture queues contain at most 32 frames per call and 32 MiB of owned frame
payload globally, with a 2 MiB frame limit. Enqueue never waits for disk or
FFmpeg. Queue overflow discards dependent frames until another IDR and marks
the result partial; capture failures leave the live call and audio pipeline
available. There are no per-frame recording logs. Audio-only calls do not
reserve video capture slots. Video activation reserves a slot lazily, so
mixed calls upgrading later work through the same recorder.

## Compact composition and resource limits

After hangup a separate video worker processes ONE call at a time. FFmpeg
uses one decoder/encoder/filter thread per stage, with bounded stderr and a
processing deadline. Segments are normalized sequentially, including rotation,
proportion-preserving scale/pad and timing; there is no unbounded filter graph
or simultaneous decoder per camera toggle. Temporary clips are rebuilt on
retry rather than accumulating. Audio-only/camera-off/gap intervals are black.
Sub-second missing input inside a segment holds the last decoded frame until
the next presentation timestamp. At camera boundaries frame precision is
100 ms, matching the archival 10 fps profile.

Final MP4: 640x240, agent left/customer right, 320x240 per pane, 10 fps,
H.264 baseline CRF 32/ultrafast with 192 kbps maximum video rate and a 384 kb
VBV buffer. AAC mono mixes both authoritative 16 kHz PCM tracks at 48 kbps.
WAV stereo is preserved independently for transcription, speakers and summary.
MP4 passes ffprobe checks for streams, dimensions and full intended duration.
Reduction affects the stored recording, not the live camera delivery.

Default environment:

| Variable | Default | Meaning |
| --- | ---: | --- |
| VIDEO_RECORDING_ENABLED | off | Enable server video recording |
| VIDEO_RECORDING_MAX_CALLS | 8 | Simultaneous active video captures per server |
| VIDEO_RECORDING_MAX_MB | 128 | Combined original encoded capture per call (MiB) |
| VIDEO_RECORDING_MAX_SECONDS | 3600 | Recorded video timeline limit per call |
| VIDEO_RECORDING_DISK_MB | 4096 | Sampled recording queue disk budget (MiB) |

A disk monitor checks the entire recording directory and free space every
5 seconds, requiring 512 MiB free for capture. The queue budget is a sampled
protection threshold, not a filesystem quota; ongoing PCM and temporary mux
files also consume space. Operators must allow for intermediate copies and
retained failures. The limits stop video capture, mark partial/unavailable,
and never truncate the ongoing audio call. Final video beyond the configured
timeline limit is intentionally omitted; WAV remains full length. These are
protective defaults, not measured capacity for a specific Portainer host.

## Independent delivery and confirmed cleanup

`job.json` owns audio/WAV/transcription. `video-job.json` owns capture,
composition/upload/retries. Neither worker waits for the other to perform its
network/transcription work; the video worker only requires that PCM capture
has finished. Failed video does not block audio analysis or its Chatspot note.

Both upload actions use signed raw JSON (HMAC-SHA256, WACALLS_PASSWORD).
`upload-url`/`confirm` include `artifact:"audio"|"video"` (omitted means legacy
audio). Video MIME is video/mp4, with a filename ending .mp4. The Chatspot
proxy obtains GET /core/v2/file Type=VIDEO/Name/MimeType, WaCalls PUTs raw bytes,
and the proxy POSTs `{tempFileId}`. Successful confirmation must return a
nonempty permanent fileId and persist it. Upload retries use new temporary
URLs; already-confirmed audio/video are independently idempotent.

Video confirm adds `durationMs`,
`layout:"side-by-side:agent-left,customer-right"`, `partial` and optional
`error`. `video-status` accepts queued/processing/uploading/failed/unavailable.
`ready` is only written on confirmation and cannot regress. The done action
includes mediaHistory and the current video capability but does not wait for
MP4. Permanent failures retain originals and journals; transient failures use
persisted exponential backoff. A restart resumes queued/processing/uploading
jobs. When video is disabled, pending captures remain retained for later.

Directory cleanup requires completed audio analysis, confirmed WAV with a
permanent file ID and completed confirmed video with a permanent file ID
whenever video segments exist. Unreadable journals, missing IDs or any failure
prevent deletion. No expiry silently removes an undelivered file. Restoring a
failed job requires fixing the reported cause, then explicitly requeueing its
retained journal; destructive cleanup is never the default.

## Lovable records and Chatspot note

The alpha.8 contract uses separate video columns, monotonic ready/file IDs and
account-scoped renewable Chatspot links. The player shows only confirmed video;
pending link/processing/error/partial indicators reflect actual state. WAV
alone feeds AI. A ready video joins the existing note when available; if the
WAV/summary note already went out, an attachment-only note follows without a
second summary. Note retries claim atomically and reconcile the same file ID
against existing Chatspot notes after ambiguous responses.

## Validation and remaining real-world check

Tests execute FFmpeg with real H.264 fixtures, jittered timestamps, staggered
camera starts, portrait/landscape padding, rotation/source-clock resets,
black camera-off intervals and audio impulses on the shared timeline. They
cover restart recovery using persisted packet times, bounded capture slots,
queue drain, no-browser inbound tap, missing-confirmation IDs, retries and
cleanup only after both artifacts. CI installs FFmpeg and makes its presence
mandatory; root and nested modules run vet and the race detector.

Local WebRTC negotiation tests require netlink access unavailable in this
workspace; CI runs them normally. No claim of real WhatsApp/API/Portainer
end-to-end validation follows from the synthetic fixtures. After applying
the release and publishing Lovable, test audio → video → audio, rotation,
mic mute and final hangup; verify the MP4/WAV/summary and server cleanup.

Primary specifications: [Matroska codec mapping](https://www.matroska.org/technical/codec_specs.html),
[Matroska elements](https://www.matroska.org/technical/elements.html),
[FFmpeg filters](https://ffmpeg.org/ffmpeg-filters.html).
