# Chatspot-specific WaCalls patches

This fork keeps the upstream WaCalls call/media architecture and adds only the compatibility and delivery changes needed by Chatspot Calls.

## Current changes

- Expose `peerPhone` alongside the original WhatsApp `peer` identifier.
- Resolve inbound LID identifiers to the real phone number when WhatsApp provides `caller_pn` or the local LID map already knows the mapping.
- Preserve `peerPhone` in call status, incoming, claimed, ended, active call list, and history payloads.
- Propagate a terminate for an outbound call that ends before media connects so other linked devices stop ringing.
- Optional server-side call recording, disabled by default:
  - starts only after the call reaches the active/connected state;
  - records the browser/agent PCM and WhatsApp/customer PCM as separate 16 kHz mono tracks;
  - aligns missing intervals with silence and writes incrementally under the persistent data directory;
  - finalizes a stereo WAV with left=agent and right=customer;
  - uploads through the Chatspot Calls recording endpoint using HMAC, never a Chatspot account token;
  - sends mono WAV chunks to Chatspot Calls for speaker-aware transcription, then asks Chatspot Calls to finish the summary/private-note workflow;
  - persists queue state on disk, resumes after restart, retries transient failures, and removes local audio after successful delivery.

## Recording configuration

The recorder is intentionally off unless `RECORDING_ENABLED=1` is set.

- `RECORDING_ENABLED=1` enables server-side recording.
- `CHATSPOT_CALLS_URL=https://calls.chatspot.com.br` selects the delivery service. This has the same value by default.
- `WACALLS_PASSWORD` is also used as the HMAC secret for the recording endpoint, matching the existing relay contract. It must be available in the WaCalls server environment when recording is enabled.
- `RECORDING_DIR` optionally overrides the queue directory. By default it is `recordings` beside the SQLite database, so production `/data/wacalls.db` automatically uses `/data/recordings` on the existing persistent volume.

The final format is WAV rather than adding an ffmpeg runtime dependency. The production deployment currently downloads a self-contained WaCalls server binary, so WAV keeps the release portable and does not require rebuilding the container image. The raw PCM and final WAV are temporary delivery artifacts, not permanent storage.

The existing SQLite database and WhatsApp pairing/session data are unchanged.
