from pathlib import Path

path = Path("cmd/server/httpapi.go")
text = path.read_text()

replacements = [
    (
        '\tvar callID, peerStr string\n\tvar err error\n\tif group != "" {\n\t\tcallID, err = sess.startOutgoingGroup(r.Context(), group, body.Video)\n\t\tpeerStr = group\n\t} else {\n\t\tpeer := types.NewJID(normalizePhone(body.Phone), types.DefaultUserServer)\n\t\tcallID, err = sess.startOutgoing(r.Context(), peer, body.Video, nil)\n\t\tpeerStr = peer.String()\n\t}\n',
        '\tvar callID, peerStr, peerPhone string\n\tvar err error\n\tif group != "" {\n\t\tcallID, err = sess.startOutgoingGroup(r.Context(), group, body.Video)\n\t\tpeerStr = group\n\t} else {\n\t\tpeer := types.NewJID(normalizePhone(body.Phone), types.DefaultUserServer)\n\t\tcallID, err = sess.startOutgoing(r.Context(), peer, body.Video, nil)\n\t\tpeerStr = peer.String()\n\t\tpeerPhone = normalizePhone(peer.User)\n\t}\n',
        "start-call block",
    ),
    (
        '\ts.broker.upsertCall(CallRecord{\n\t\tSessionID: sess.id, CallID: callID, Owner: &owner, Direction: "outbound", Peer: peerStr,\n\t\tMedia: callMedia, StartedAt: time.Now().UnixMilli(), Status: StatusRinging,\n\t})\n',
        '\ts.broker.upsertCall(CallRecord{\n\t\tSessionID: sess.id, CallID: callID, Owner: &owner, Direction: "outbound", Peer: peerStr, PeerPhone: peerPhone,\n\t\tMedia: callMedia, StartedAt: time.Now().UnixMilli(), Status: StatusRinging,\n\t})\n',
        "outbound CallRecord",
    ),
    (
        '\ts.broker.upsertCall(CallRecord{\n\t\tSessionID: targetSess.id, CallID: transferID, Owner: &owner, Direction: "outbound", Peer: rec.Peer,\n\t\tMedia: "audio", StartedAt: time.Now().UnixMilli(), Status: StatusRinging,\n\t})\n',
        '\ts.broker.upsertCall(CallRecord{\n\t\tSessionID: targetSess.id, CallID: transferID, Owner: &owner, Direction: "outbound", Peer: rec.Peer, PeerPhone: rec.PeerPhone,\n\t\tMedia: "audio", StartedAt: time.Now().UnixMilli(), Status: StatusRinging,\n\t})\n',
        "transfer CallRecord",
    ),
]

for old, new, label in replacements:
    if old not in text:
        raise SystemExit(f"{label} changed; refusing unsafe patch")
    text = text.replace(old, new, 1)

path.write_text(text)
print("peerPhone patch applied")
