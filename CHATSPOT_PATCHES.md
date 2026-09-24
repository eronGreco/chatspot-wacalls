# Chatspot-specific WaCalls patches

This fork keeps the upstream WaCalls architecture and adds only the compatibility changes needed by Chatspot Calls.

## Current changes

- Expose `peerPhone` alongside the original WhatsApp `peer` identifier.
- Resolve inbound LID identifiers to the real phone number when WhatsApp provides `caller_pn` or the local LID map already knows the mapping.
- Preserve `peerPhone` in call status, incoming, claimed, ended, active call list, and history payloads.
- Propagate a terminate for an outbound call that ends before media connects so other linked devices stop ringing.

The existing SQLite database and WhatsApp pairing/session data are unchanged.
