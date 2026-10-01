# Server-only recording

Call recordings must be produced and retained by the WaCalls server. The browser captures and transmits live call media and displays status; it must not record, store, upload, or retry recording files. No browser fallback is allowed when configuration or an RPC fails. Future video recording must follow the same server-only model.

## Delivery and retention

Set `RECORDING_ENABLED=1`, configure the Chatspot Calls endpoint and shared secret, and persist `/data` on a volume. The server records agent/customer PCM, builds a stereo WAV, obtains an upload URL from Chatspot Calls, uploads and confirms the file, and completes transcription processing. It removes its recording directory only after `Confirmed=true` and `State=done`.

Transient delivery failures remain scheduled indefinitely with capped retry intervals. Permanent failures are persisted as `failed`, retain all files, and are excluded from normal queue selection so other calls can proceed. After correcting the cause, a failed job requires deliberate recovery to a queued state. Unreadable/orphaned recording directories are retained for recovery rather than deleted by age.

A browser disconnect can stop live media delivery or end the call; server recording preserves media already received and the server delivery queue remains independent of that browser. This does not imply continued capture of microphone audio after the browser disconnects. Retained failures consume storage and should be monitored.

## Deployment

The server retention fix ships in `v2.0.0-alpha.3`; `alpha.2` still has the old failure cleanup policy. Publish the matching Lovable frontend that removes browser recording and upload paths. Historical browser WebM files are preserved. If a call already references a browser WebM, the server upload endpoint reports a conflict instead of treating that unrelated file as confirmation of the server WAV.

## Validation

Regression tests cover confirmed-only deletion, unconfirmed completed jobs, permanent failure retention, retry after more than 24 hours, and old unreadable job retention. A real call must separately validate voice at the start of the delivered WAV and server queue completion after the window closes.
