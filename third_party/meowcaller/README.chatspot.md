# Chatspot's pinned Meowcaller source

Production source copied from `purpshell/meowcaller` commit `c48c3e2a243c672c942cbf0c941d14161720d540`, module identity unchanged, under the upstream MIT license in LICENSE. The main module uses a local replace so builds do not silently follow upstream updates. The embedded MLow tables are unchanged.

Chatspot changes:
- authenticated remote keyframe requests exposed as `Call.RequestVideoKeyframe`;
- bounded, participant-scoped retry of SRTCP PLI while waiting for IDR or after video stalls;
- preserve SPS/PPS parameter sets received separately while the H.264 assembler awaits an IDR;
- browser requests now reach this remote recovery path through the bridge;
- recovery ticker stops both on call context cancellation and media-loop exit.

The upstream H.264 assembler and video/SRTCP tests are retained, together with recovery regression tests. Other upstream tests/fixtures, examples, and analysis documents are not included in this runtime subset. Run `go -C third_party/meowcaller test -race ./...` separately from the main module tests. Root `go test ./...` does not traverse nested modules.

No MLow/audio codec or WhatsApp signaling behavior is changed. Browser/server call tests and fresh real WhatsApp video validation remain required.
