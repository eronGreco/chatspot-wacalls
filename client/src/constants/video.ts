// Video-call constants. The browser encodes/decodes H.264 Annex-B using
// WebCodecs. Meowcaller handles WhatsApp RTP/SRTP; the browser bridge carries
// encoded access units in the five-byte envelope documented in
// docs/VIDEO_1TO1_INTEGRATION.md. Settings still require real-call validation.

// VIDEO_CHANNEL_LABEL must match videoChannelLabel in cmd/server/bridge.go.
// (The label is still "vp8" for wire compatibility; the payload is now H264.)
export const VIDEO_CHANNEL_LABEL = "vp8";

// WhatsApp video calling is H264. The browser encodes/decodes Annex-B H264 via
// WebCodecs; the Go side packetizes it into RTP. Constrained Baseline / level
// 3.1 is the most broadly supported real-time profile.
export const VIDEO_H264_CODEC = "avc1.42E01F";

// Capture / encoder settings. 640x480@20fps keeps the data channel and the relay
// comfortable; raise once the media plane is proven.
export const VIDEO_WIDTH = 640;
export const VIDEO_HEIGHT = 480;
export const VIDEO_FRAMERATE = 20;
export const VIDEO_BITRATE = 600_000;

// The camera frame is downscaled so its longest edge is at most this, keeping
// its aspect ratio. This bounds keyframe size so a single frame never exceeds
// the "vp8" data channel's SCTP maxMessageSize (which silently drops the send).
export const VIDEO_MAX_EDGE = 640;

// Keyframe on the first frame and then on this fixed interval. Além disso, o
// servidor pode pedir um keyframe fora de hora (ver VIDEO_CTL_KEYFRAME_REQUEST)
// quando o WhatsApp manda um RTCP PLI/FIR.
export const VIDEO_KEYFRAME_INTERVAL_MS = 2000;

// Mensagem de controle de 1 byte no canal "vp8" (server → browser): "manda um
// keyframe agora". Um quadro real sempre tem >= 5 bytes, então nunca há
// ambiguidade. Espelha videoCtlKeyframeRequest em
// internal/voip/media/videoframe.go.
export const VIDEO_CTL_KEYFRAME_REQUEST = 0x01;
