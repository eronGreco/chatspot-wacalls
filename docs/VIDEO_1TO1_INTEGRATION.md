# Vídeo 1:1 no Chatspot Calls

## Estado e escopo

Integração inicial implementada no Lovable. Em 30/09/2026, o usuário confirmou uma chamada de vídeo real com imagem nos dois sentidos usando a `v2.0.0-alpha.2`. Isso não valida todos os cenários: o vídeo do celular recebido no site congelou frequentemente, inclusive por cerca de 20 segundos, a webcam apareceu quadrada/achatada no celular e a gravação apresentou silêncio inicial apesar de conversa audível. A etapa atual cobre videochamadas diretas recebidas e feitas; grupos e upgrade de voz durante a chamada serão validados em etapas posteriores.

Voz e gravação de áudio foram validadas pelo usuário em entrada e saída. Reconexão via QR foi relatada como funcional em 30/09/2026. Multioperador real e recuperação da fila após restart continuam pendentes.

## Fontes conferidas

- [PR #56 do WaCalls](https://github.com/JotaDev66/WaCalls/pull/56), aberto, sem comentários na consulta de 30/09/2026.
- [Implementação do PR](https://github.com/fabriciosprj/WaCalls-Video/tree/b07ce6e6fdd70b6eb64d4aefc5861b81aa4a7474), autoria fabriciosprj; descrição relata teste real de recepção/transmissão de vídeo.
- [Meowcaller usado pela v2](https://github.com/purpshell/meowcaller/tree/c48c3e2a243c), biblioteca que executa o protocolo WhatsApp e a mídia.
- [Nossa base adaptada](https://github.com/eronGreco/chatspot-wacalls/tree/3369de3d348c8f826ccc07b27c35e7ca7d509bc5): `client/src/lib/video-pipe.ts`, `video-frame.ts`, `webrtc.ts`, `client/src/constants/video.ts`, `cmd/server/bridge.go`, `session.go`, `httpapi.go`.

A licença MIT e os avisos de copyright do WaCalls devem acompanhar cópias substanciais. O site deve creditar a base e manter os avisos, além dos créditos a Fabricio e Meowcaller.

## Contrato existente na alpha 2

Todas as operações usam o mesmo contrato autenticado do site e o mesmo `X-Client-Id` da chamada.

| Operação | Requisição | Resposta |
| --- | --- | --- |
| Iniciar vídeo | `POST /api/sessions/{sid}/calls`, JSON `{"phone":"...","video":true}` | `{"call":{"callId":"..."}}` |
| Negociar navegador | `POST /api/sessions/{sid}/calls/{callId}/webrtc`, JSON `{"sdp_offer":"..."}` | `{"sdp_answer":"..."}` |
| Aceitar recebida | `POST /api/sessions/{sid}/calls/{callId}/accept` | ID da chamada |
| Rejeitar recebida | `POST /api/sessions/{sid}/calls/{callId}/reject` | Status HTTP |
| Encerrar | `DELETE /api/sessions/{sid}/calls/{callId}` | Status HTTP |
| Parar envio local de vídeo | `POST /api/sessions/{sid}/calls/{callId}/video/stop` | Status HTTP |

`incoming`, `call-status` e registros de `call-list` incluem `media`: `audio` ou `video`. Ausência do campo mantém compatibilidade com voz. Preservar esse campo no proxy, nos schemas e nos comandos da extensão.

O endpoint `video/stop` sinaliza o estado ao WhatsApp; não deve fechar PCM nem o decoder remoto. Antes de expor a operação, conferir a semântica de `Call.StopVideo` do pin do Meowcaller e testar recepção contínua.

## Transporte no navegador

O vídeo usa a mesma PeerConnection que o áudio. Criar ambos os data channels antes da oferta inicial:

- `pcm`: áudio Int16 LE, 16 kHz.
- `vp8`: nome legado; o conteúdo é **H.264 Annex-B**, não VP8.
- Vídeo binário, canal ordenado, `binaryType = "arraybuffer"`.

Não trocar esse formato por RTP, MediaRecorder ou trilhas WebRTC convencionais sem alterar o backend correspondente.

Cada mensagem de quadro tem:

| Bytes | Conteúdo |
| --- | --- |
| 0 | Flags: bit 0 keyframe; bits 1–2 rotação, códigos 0/1/2/3 |
| 1–4 | Timestamp em milissegundos, uint32 big-endian |
| 5 em diante | Access unit H.264 Annex-B |

Rotação: códigos 0/1/2/3 correspondem a 0/90/180/270 graus. Reaproveitar a correção CVO do decoder existente; validar celular em pé e deitado.

Mensagem de controle de exatamente um byte `0x01` pede ao navegador um keyframe. Não confundir com um quadro.

Base de captura: `avc1.42E01F`, Annex-B, realtime, maior aresta 640, 20 fps, 600 kbps e keyframe a cada 2 segundos. Verificar `VideoEncoder.isConfigSupported` e `VideoDecoder.isConfigSupported`; presença de WebCodecs por si só não comprova suporte a H.264.

## Adaptações exigidas no site

1. Reaproveitar o pipe/frame existente, sem substituir o módulo de áudio validado.
2. Incorporar vídeo ao preparo local anterior ao aceite, preservando ownership e mic/SDP únicos.
3. Pedir câmera apenas após ação explícita. Separar recepção remota da disponibilidade de câmera local.
4. Preview local mudo e vídeo remoto no modal global, incluindo estado aguardando primeiro quadro.
5. Preservar histórico montado, aba, busca, expansão e posições de scroll ao fechar.
6. Encerrar câmeras, codecs, timers e frames em todos os caminhos de sucesso, erro, rejeição e conflito.
7. Controlar fila do encoder e `bufferedAmount` sem bloquear PCM. Após descarte que comprometa sequência, recuperar em keyframe.
8. Preservar tamanho máximo de mensagem SCTP; não fragmentar Annex-B arbitrariamente.
9. Manter gravação de **áudio** estéreo agent/customer, fila, upload HMAC, transcrição, resumo e nota. Esta etapa não armazena vídeo.
10. Manter grupos e upgrade de voz fora da aprovação desta etapa.

O código de exemplo tem comentários antigos citando VP8. O conteúdo efetivo do encoder e do protocolo é H.264; corrigir esses comentários ao adaptar.

## Validação

### Automatizada

- Envelope de quadro, timestamp, rotações e comando keyframe.
- Propagação `media` em API, SSE, snapshot e comandos.
- `video:true` na chamada de saída.
- Permissão negada, codec indisponível, câmera ausente, cleanup parcial.
- Encerramento remoto e ownership conflitante.
- Preservação do modal, histórico e áudio.
- Typecheck/build do site.

### WhatsApp real, a executar pelo usuário

1. Site → celular: vídeo e áudio nos dois sentidos.
2. Celular → site: modal de vídeo, aceite e mídia nos dois sentidos.
3. Câmera local interrompida sem perder áudio nem vídeo remoto.
4. Rotação do celular e adaptação ao painel estreito.
5. Encerramento pelo operador e pelo celular libera a câmera.
6. Gravação de áudio, transcrição e nota chegam ao Chatspot.
7. Chamada apenas de voz continua funcionando.

Não marcar como validado antes desses testes. Teste unitário de envelope/bridge não prova decodificação H.264 nem interoperabilidade com o WhatsApp.

## Deploy

A investigação inicial não identificou necessidade de novo binário: usar a `alpha.2` existente para os primeiros testes de integração. Se a implementação exigir mudança no backend, documentar a diferença e publicar outra alpha antes de instruir qualquer alteração no Portainer. O site só deve ser publicado depois da revisão do código e dos checks.

## Investigação do primeiro teste real

- No site `3af63e01`, o decoder descarta deltas e espera IDR quando `decodeQueueSize > 3`. Não há pedido de keyframe remoto nesse caminho. A espera pode durar bastante; é uma falha identificada no código, mas falta diagnóstico da chamada para atribuir todos os congelamentos a ela.
- `0x01` é controle servidor → navegador. A `alpha.2` ignora esse controle enviado no sentido oposto. Não anunciar recuperação por PLI originada no navegador sem implementar o caminho completo. O Meowcaller do pin possui recuperação por PLI para perda de RTP; isso não detecta descartes locais do decoder no site.
- A captura pede 640×480 e usa `track.getSettings` para redimensionar. A correção deve usar metadados reais do vídeo e preservar proporção; preencher um quadro vertical com câmera horizontal exige recorte central, não esticar pixels. O layout final do telefone é controlado pelo WhatsApp, não pelo CSS do site.
- No backend, a gravação inicia em `CallPhaseActive`, que o pin do motor pode emitir no primeiro RTP decodificado, antes do aceite remoto. PCM anterior à criação do recorder é ignorado, e o tap customer só existe após conectar o bridge do navegador. Esses caminhos merecem inspeção, mas não provam a causa do silêncio grande relatado.
- No site, a confirmação de gravação no servidor falhando aciona gravação local alternativa. Identificar qual arquivo foi entregue antes de mudar o relógio ou os taps.
- Para fechar o diagnóstico de gravação, correlacionar call ID, offer/accept, primeiro RTP, recording started, conexão do browser, origem server/local e duração real do silêncio no arquivo. Não cortar silêncio nem alterar timestamps da transcrição como substituto para recuperar áudio perdido.

## Receive recovery in alpha.4

The first receiver changes were insufficient in the next live test: outbound camera video stayed fluent on the phone, while inbound phone video remained frozen. The frontend had no remote keyframe request path. Inspection found that the pinned RTP assembler triggers PLI only on the first packet gap; an IDR wait could therefore persist indefinitely without another request.

Alpha.4 routes browser control byte `0x01` through `Bridge.OnRemoteKeyframeRequest` to `Call.RequestVideoKeyframe`. A participant-scoped recovery loop uses the existing authenticated SRTCP sender, retries PLI with a one-second minimum interval, and stops after IDR recovery or media termination. Authenticated receive SSRCs are tracked; no guessed SSRC or plaintext RTCP is used. The H.264 assembler retains SPS/PPS sent separately during recovery and includes them with a subsequent IDR. The browser also retains parameters across decoder restart and requests recovery when stalled.

The Meowcaller runtime subset is copied under its MIT license at the same pin into `third_party/meowcaller`, referenced through a local Go module replace. Its README records provenance and targeted changes. CI explicitly runs nested-module regression tests, since root `go test ./...` does not traverse nested modules.

The matching Lovable changes keep the video view within the embedded viewport, preserve history scroll, add a working microphone mute, and use compact camera controls. Transcription is processed by energy windows with original offsets; an investigation found an incorrectly timed phrase present later in the server WAV. No historical audio is trimmed or replaced.

Validated: main-module CI (including real Pion data-channel control delivery), nested H.264/SRTCP and recovery race tests, and Lovable tests/typecheck/build. Remaining: a fresh 1:1 WhatsApp video test against alpha.4 and the published frontend. Automated recovery tests do not prove every real-network freeze is resolved. Video recording remains unimplemented and must be server-side when added.


## alpha.5: RTP ordering and bounded production logging

Authorized test `2026-10-01 03:41 UTC`: the last 1000 log lines contain
993 H264 RTP packets covering only approximately six seconds. All sequence
numbers 9509 through 10501 are present, but three arrival inversions exist:
9984,9986,9987,9988,9985; 9995,9997,9996; and 10083,10085,10084.
The access-unit assembler requires ordered input, but the receive loop previously
passed arrival order directly. That converts reordering into packet loss and
unnecessary IDR waits. This explains interruptions in this captured slice;
it does not establish the cause of every interruption during the full call.

RFC6184 section 7.1 specifies RTP sequence ordering before H264 depacketization:
https://www.rfc-editor.org/rfc/rfc6184.html#section-7.1

Authenticated video payloads now enter one `rtp.VideoReorderBuffer` per receiver
before depacketization. Contiguous packets emit immediately. When a sequence gap
appears, the queue waits until the missing packet arrives, its oldest queued
packet reaches 80ms on a subsequent Push, or the queue reaches 128 payloads.
Expired gaps are passed through to the existing IDR recovery logic. Duplicates
and stale packets are ignored; 16-bit sequence wrap is supported. Buffered
payloads are copied because the receive storage can be reused. Retention is
bounded to 128 payloads of at most 1500 bytes per SSRC (187.5KiB of payload plus
map/metadata overhead), with no extra goroutines or timers. If video arrivals
stop entirely, the alpha.4 recovery watchdog remains responsible for recovery.

Production RTP packet summaries now stop after the first 20 RTP packets per call,
regardless of payload type. Video summaries appear at most once per ten seconds
per active receiver plus a final summary, with call_id, SSRC, cumulative packet,
ordering, missing, late/duplicate, rejected, frame and recovery counters.
Optional private wire diagnostics remain opt-in. Normal video paths avoid
constructing diagnostic frame maps when diagnostics are disabled.

The regression fixture contains only sequence/timestamp/marker metadata, without
call IDs, participant IDs or media. Its second-resolution source log cannot
reconstruct true arrival delays; the automated ordering replay explicitly uses
simulated 1ms intervals. Separate tests validate timeout behavior, real loss,
fragmented interframe reconstruction, duplicate and queue bounds. Queue-only
benchmarks with 500 independent states test bookkeeping, not full server
concurrency. End-to-end load capacity requires CPU/RAM/network measurement with
real audio/video calls on the target host; no capacity guarantee is inferred.
