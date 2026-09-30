# Chatspot Calls v2 — status da migração

Branch: `v2/meowcaller`

PR: `#3`

## Arquitetura escolhida

A aplicação continua responsável por HTTP/SSE, sessões, QR, ownership, WebRTC do browser e integração Chatspot.

O motor do protocolo de chamadas passa a ser:

```text
Meowcaller
    ↓
HyperMeow
    ↓
WhatsApp
```

O motor VoIP manual herdado do WaCalls foi removido da branch v2:

- `internal/voip/call` removido;
- `internal/voip/core` removido;
- `internal/voip/signaling` removido;
- `internal/voip/transport` removido;
- `internal/voip/wanode` removido;
- `internal/wa` removido;
- MLow/RTP/SRTP/RTCP/STUN próprios removidos.

O diretório `internal/voip/media` contém somente 4 helpers genéricos ainda usados pela ponte Browser ↔ Backend:

- `pcm.go`
- `pcm_test.go`
- `videoframe.go`
- `videoframe_test.go`

Eles não implementam o protocolo de chamadas do WhatsApp e podem ser renomeados/movidos depois sem mudança arquitetural.

## Portado da v1.1.0-chatspot

- serviço de gravação server-side;
- duas trilhas PCM: agent/customer;
- sincronização e preenchimento com silêncio;
- WAV estéreo L=agent / R=customer;
- chunks mono para transcrição;
- fila persistente em disco;
- retry progressivo;
- recovery após restart;
- HMAC para Chatspot Calls;
- upload-url → upload → confirm → transcribe → done;
- limpeza local somente depois da entrega completa;
- `peerPhone` separado do LID técnico.

## Integração da gravação com Meowcaller

Atendente:

```text
browser PCM
  → liveAudioSource.push
  → recording.writeAgent
  → Meowcaller AudioSource
```

Cliente:

```text
Meowcaller Call.Receive
  → Bridge.WritePCM
  → recording.writeCustomer
  → browser
```

A gravação começa quando a chamada entra em `CallPhaseActive` e é finalizada no teardown da chamada.

## Contrato Lovable

A etapa de paridade preserva a API/SSE já consumida pelo Chatspot Calls.

Ver:

- `docs/CHATSPOT_CALLS_V2_CONTRACT.md`
- `docs/LOVABLE_V2_MIGRATION_PROMPT.md`

## Validação automática

O workflow de CI foi configurado para rodar também em push para `v2/meowcaller` e possui `workflow_dispatch`.

No momento da criação deste documento, o GitHub ainda não criou nenhum workflow run para a branch/PR. Como este repositório é um fork, confirmar na aba **Actions** se os workflows do fork estão habilitados. Não considerar a branch compilada/testada até aparecerem os jobs `server` e `client` verdes.

## Gates de paridade obrigatórios

- CI server: módulos, vet, gofmt, build, testes/race;
- CI client: typecheck e build;
- restauração de sessão pareada;
- criação de sessão e QR;
- chamada 1:1 saída;
- chamada 1:1 entrada;
- accept/reject/end;
- áudio bidirecional;
- peerPhone correto em LID;
- ownership;
- gravação server-side;
- canais L/R corretos;
- upload final no Chatspot;
- transcrição/resumo/nota;
- retry/recovery da gravação após restart.

## Depois da paridade

Só então validar e integrar no Chatspot Calls:

1. vídeo 1:1 de saída;
2. vídeo 1:1 de entrada;
3. upgrade áudio ↔ vídeo;
4. adicionar terceiro participante à chamada atual;
5. chamada de grupo;
6. vídeo multiparte.

Chamadas em grupo são marcadas como experimentais no Meowcaller e exigem teste real antes de serem tratadas como estáveis.
