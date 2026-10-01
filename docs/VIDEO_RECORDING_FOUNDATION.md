# Mixed calls and server video recording foundation

## Shipped capability

`GET /api/config` advertises `mediaHistoryVersion: 1`, `mixedCalls: true`,
`recordingClock: "server_connected"`, and **`videoRecordingSupported: false`**.
This change tracks call modality and prepares artifact metadata. It does not
capture, mux, upload, or delete any video recording. Audio WAV capture,
transcription, summary, durable delivery queue and confirmed-upload deletion
remain the existing server pipeline. No browser recording or upload fallback.

## Authoritative media history

`call-list`, `call-status`, `call-ended` and call GET responses carry optional
`mediaHistory`: `{version:1, connectedAt, endedAt?, profile, segments, truncated}`.
Times `connectedAt` and `endedAt` are Unix milliseconds. Segment
`{startMs,endMs?,media,localVideo,remoteVideo}` offsets use the same connection
origin as server audio recording. Segments start at zero and are continuous.
`media` is video when either negotiated camera is active, audio otherwise.
Pending upgrades are excluded. `profile` is audio, video or mixed over the
whole connected call, independent of current `media`.

Turning off one camera preserves video when the other is active. Both cameras
off returns to audio. Requests and rejected upgrades alone do not mark a call
mixed. Adjacent identical direction states are deduplicated; zero-duration
transitions replace the boundary state. History is bounded at 512 segments.
On overflow `truncated` becomes true, visible boundaries stop advancing, but
whole-call profile continues updating. Do not extend truncated coverage to the
end or reject authoritative mixed merely because visible coverage has one mode.
Missed calls and old records without metadata remain unknown.

Persist complete snapshots scoped to account and call under a row lock.
Reject different connection origins, stale boundaries and reopening an ended
history. Recording completion can consolidate a closed snapshot without
regressing a newer history. Repeated snapshots are idempotent. Preserve legacy
records and accept `mediaHistory: null` or absent on old recording jobs.

## Upgrade protocol and ownership

Authenticated session endpoints retain call-owner checks:
- `POST calls/{id}/webrtc/renegotiate`: add video transport on the same WebRTC
  connection. Preserve microphone, PCM channel and audio clock.
- `POST calls/{id}/video/start`: request upgrade, outbound camera gated until
  peer acceptance.
- `POST calls/{id}/video/accept`: accept peer request, default starts own camera
  before accepting. Optional `{ "sendVideo": false }` receives video without
  requesting local camera.
- `POST calls/{id}/video/reject`: decline pending peer request, preserving
  active cameras and audio. Failed signaling preserves request for retry.
- `POST calls/{id}/video/stop`: stop own camera, preserving peer camera/audio.

`call-peer-video` includes authoritative `localVideo`, `remoteVideo` and `raw`.
Raw 4 is upgrade acceptance; 5 rejection; 8 cancellation. These are not camera
stopped states. Raw 0/6 disable/stop remote camera; raw 1 enables it. Ask before
activating camera for remote upgrades, handle permission denial, serialize
renegotiation and cancel stale work when hanging up.

## Durable recording manifest

Active audio recording directories contain atomically replaced `media.json`
with version 1, clock `server_connected`, audio sample rate 16000,
`videoRecordingSupported:false`, `videoRecordingStatus:"unavailable"` and
`mediaHistory`. Changes are persisted only on modality transitions, not frames.
Queue jobs optionally include the history; restart recovery reloads it.
Delivery `action=done` includes history and explicit unsupported-video capability.
A modality badge never implies a video file exists. Keep WAV and future video
artifact identifiers/statuses independent. AI analysis continues using audio.

## Next implementation: actual video capture

1. Tap authenticated inbound and outbound encoded media on the server,
   independent of browser rendering and file upload. Store chunks with source,
   codec, dimensions, orientation and capture/stream timestamps. A raw Annex-B
   concatenation alone is insufficient for reliable synchronization.
2. Preserve variable frame timing, RTP clock mapping, gaps, upgrades and camera
   rotation on the shared connection timeline. Keep complete audio from call
   start including intervals without video. Never infer video duration from a
   fixed frame rate or first observed image.
3. Persist chunk manifests and finalization state so crash recovery can finish
   partial calls. Set explicit disk quotas, bounded buffers, queue limits and
   limited mux workers. Media processing must not block live RTP or audio.
4. Mux a playable server artifact after the call. Preserve aspect ratios and
   represent inactive directions and audio-only intervals explicitly. Verify
   audiovisual timing and seek behavior against mixed-call fixtures.
5. Confirm the Chatspot video API and artifact contract before enabling video
   upload. Persist upload progress and retries; delete local video only after
   confirmed delivery. Audio upload/analysis must remain independent of video
   failures. Expose queued/processing/ready/failed honestly in history.
6. Only enable `videoRecordingSupported` after capture, mux, recovery, upload
   confirmation and UI playback pass. Validate real WhatsApp upgrades in both
   directions, permission denial, network loss, closing the extension and
   repeated camera toggles; automated signaling tests are not end-to-end proof.
