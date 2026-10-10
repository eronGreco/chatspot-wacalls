# Chatspot Calls v2 — contrato backend ↔ Lovable

## Objetivo

A v2 troca o motor de chamadas manual herdado do WaCalls por **Meowcaller + HyperMeow**.

Essa troca é interna ao backend. Para tudo que já funciona hoje no Chatspot Calls, o contrato com o Lovable deve permanecer compatível. O Lovable não deve conhecer Meowcaller, HyperMeow, RTP, SRTP ou detalhes do WhatsApp.

```text
Chatspot Calls / Lovable
        ↓ HTTP + SSE
Chatspot Calls backend
        ↓
Meowcaller
        ↓
HyperMeow
        ↓
WhatsApp
```

O antigo motor VoIP manual (`internal/voip/call`, signaling, transport, MLow, SRTP etc.) foi removido da branch v2. Permanecem apenas pequenos helpers genéricos de serialização PCM/video usados pela ponte Browser ↔ Backend.

---

## Regra principal da migração

**Primeira etapa = paridade.**

Antes de ativar vídeo, grupos ou novos controles, a v2 precisa preservar:

- criação/restauração de sessões;
- QR code e pareamento;
- chamadas 1:1 de áudio, entrada e saída;
- aceitar, rejeitar e encerrar;
- ownership/claim por operador;
- `peerPhone` correto, separado do JID técnico/LID;
- bridge de microfone/alto-falante do navegador;
- gravação server-side;
- entrega final da gravação ao Chatspot;
- transcrição, resumo e nota privada pelo Chatspot Calls;
- recuperação da fila de gravação após restart.

Nenhuma função nova deve ser considerada pronta para produção/teste principal antes dessa paridade passar.

---

## Configuração backend já usada

O backend continua usando as variáveis já existentes:

```text
RECORDING_ENABLED=1
CHATSPOT_CALLS_URL=https://calls.chatspot.com.br
WACALLS_PASSWORD=<segredo HMAC compartilhado>
```

`WACALLS_PASSWORD` é segredo de runtime. Nunca deve ser salvo no repositório nem enviado ao browser.

O diretório de gravação continua persistente ao lado do banco. No deploy atual:

```text
/data/wacalls.db
/data/recordings
```

A v2 deve continuar usando o volume persistente `/data`.

---

# Contrato HTTP que o Lovable já usa

## Sessões

### `GET /api/sessions`

Resposta:

```json
{
  "sessions": [
    {
      "id": "...",
      "name": "...",
      "jid": "...",
      "state": "open|qr|connecting|logged_out",
      "paired": true
    }
  ]
}
```

### `POST /api/sessions`

Body:

```json
{ "name": "Nome da conexão" }
```

Resposta:

```json
{ "id": "SESSION_ID" }
```

Depois da criação, o QR chega por SSE. Não é necessário fazer polling agressivo.

### `POST /api/sessions/{sid}/pair`

Reinicia o processo de pareamento de uma sessão não pareada. O QR chega por SSE.

### `POST /api/sessions/{sid}/logout`

Desconecta a sessão sem apagar o registro local.

### `DELETE /api/sessions/{sid}`

Remove a sessão.

---

## Chamada 1:1

### `POST /api/sessions/{sid}/calls`

Contrato de paridade atual:

```json
{
  "phone": "5537...",
  "duration_ms": 300000,
  "record": false
}
```

`record` do request legado não controla a gravação oficial do Chatspot Calls. Quando `RECORDING_ENABLED=1`, a gravação oficial é server-side.

Resposta:

```json
{
  "call": {
    "callId": "CALL_ID"
  }
}
```

### `POST /api/sessions/{sid}/calls/{callId}/webrtc`

Body:

```json
{ "sdp_offer": "..." }
```

Resposta:

```json
{ "sdp_answer": "..." }
```

Essa rota continua sendo a ponte Browser ↔ Backend. A mudança para Meowcaller não elimina o WebRTC local usado pelo discador do Chatspot Calls.

### `POST /api/sessions/{sid}/calls/{callId}/accept`

Atende chamada recebida e associa o operador (`X-Client-Id`) à chamada.

### `POST /api/sessions/{sid}/calls/{callId}/reject`

Rejeita chamada recebida.

### `DELETE /api/sessions/{sid}/calls/{callId}`

Encerra a chamada.

---

# SSE

Endpoint:

```text
GET /api/events?clientId=<CLIENT_ID>
```

O backend continua enviando objetos JSON em `data:`.

## Eventos obrigatórios para paridade

### `session-list`

```json
{
  "type": "session-list",
  "sessions": []
}
```

### `session-qr`

```json
{
  "type": "session-qr",
  "sessionId": "...",
  "qr": "..."
}
```

O Lovable deve renderizar esse valor como QR code. O valor é payload do QR, não imagem base64.

### `auth-state`

```json
{
  "type": "auth-state",
  "sessionId": "...",
  "paired": true,
  "state": "open",
  "qr": ""
}
```

### `incoming`

```json
{
  "type": "incoming",
  "sessionId": "...",
  "id": "CALL_ID",
  "peer": "JID_TECNICO",
  "peerPhone": "5537...",
  "media": "audio",
  "offeredAt": 0
}
```

### `incoming-claimed`

```json
{
  "type": "incoming-claimed",
  "sessionId": "...",
  "id": "CALL_ID",
  "owner": "CLIENT_ID",
  "peer": "JID_TECNICO",
  "peerPhone": "5537..."
}
```

### `call-status`

```json
{
  "type": "call-status",
  "sessionId": "...",
  "id": "CALL_ID",
  "owner": "CLIENT_ID",
  "status": "starting|ringing|connected|ended",
  "peer": "JID_TECNICO",
  "peerPhone": "5537...",
  "media": "audio",
  "startedAt": 0
}
```

### `call-ended`

```json
{
  "type": "call-ended",
  "sessionId": "...",
  "id": "CALL_ID",
  "owner": "CLIENT_ID",
  "peer": "JID_TECNICO",
  "peerPhone": "5537...",
  "reason": "...",
  "endReason": "...",
  "endedAt": 0
}
```

### `call-list`

Snapshot das chamadas ativas. Cada item preserva `peer`, `peerPhone`, `media`, owner, status e timestamps.

---

# Regra de identidade: `peer` x `peerPhone`

Essa regra é obrigatória.

- `peer` é a identidade técnica do WhatsApp e pode ser `@lid`.
- `peerPhone` é o telefone real em formato somente dígitos quando o backend consegue resolvê-lo.
- **Nunca remover os dígitos de um `@lid` e tratar o resultado como telefone.**
- Se `peerPhone` estiver vazio, o frontend deve mostrar `Número não identificado` ou equivalente.
- Para localizar contato/conversa no Chatspot, usar `peerPhone`, nunca um LID convertido artificialmente.

---

# Gravação oficial do Chatspot Calls

## Responsabilidade do backend

A gravação é feita no servidor durante a chamada:

```text
microfone do atendente → trilha agent
WhatsApp / cliente     → trilha customer
                         ↓
alinhamento temporal + silêncio onde necessário
                         ↓
WAV estéreo
L = atendente
R = cliente
```

A gravação local é temporária. O backend não é o armazenamento final.

## Destino final

O arquivo final fica no **Chatspot** através do endpoint do Chatspot Calls:

```text
https://calls.chatspot.com.br/api/public/wacalls/recording
```

Autenticação:

```text
X-Signature = HMAC-SHA256(raw_body, WACALLS_PASSWORD)
```

O segredo nunca vai para o browser.

## Fluxo de entrega

1. `POST ?action=upload-url`
2. upload binário para a URL pré-assinada retornada pelo Chatspot
3. `POST ?action=confirm`
4. envio das faixas mono para `POST ?action=transcribe&callId=...&speaker=agent|customer&offset=...`
5. `POST ?action=done`
6. somente após sucesso completo, apagar áudio temporário local

Em erro temporário, o job fica em `/data/recordings`, usa retry progressivo e pode continuar após restart.

A transcrição, resumo com IA e criação da nota privada continuam sendo responsabilidade do **Chatspot Calls/Lovable**, não do motor Meowcaller.

---

# Browser recorder

Durante a migração da v2, manter o fallback atual do browser disponível.

Só considerar a gravação server-side como fonte única quando a matriz de paridade v2 tiver sido testada em chamada real.

A flag atual do Lovable é:

```text
WACALLS_SERVER_RECORDING=1
```

Ela não deve ser alterada automaticamente durante desenvolvimento da v2.

---

# Recursos novos já existentes na base v2, mas fora da etapa de paridade

A arquitetura Meowcaller já permite adicionar depois:

### Iniciar vídeo

```json
POST /api/sessions/{sid}/calls
{
  "phone": "5537...",
  "video": true
}
```

### Chamada vinculada a grupo do WhatsApp

```json
POST /api/sessions/{sid}/calls
{
  "group": "...@g.us",
  "video": false
}
```

### Adicionar participante à chamada atual

```text
POST /api/sessions/{sid}/calls/{callId}/participants
```

```json
{ "target": "5537..." }
```

Isso permite promover uma chamada 1:1 para ad-hoc multiparte.

### Upgrade áudio → vídeo

```text
POST /api/sessions/{sid}/calls/{callId}/webrtc/renegotiate
POST /api/sessions/{sid}/calls/{callId}/video/start
POST /api/sessions/{sid}/calls/{callId}/video/accept
POST /api/sessions/{sid}/calls/{callId}/video/stop
```

Também existem eventos aditivos como `call-peer-video`, `call-reaction`, `call-hand`, `call-held`, `call-unheld`, `call-picked-up` e eventos de transferência.

**Não habilitar UX de vídeo/grupo no Chatspot Calls antes da validação da etapa de paridade.**

---

# Matriz obrigatória antes de substituir a v1

- restaurar sessões já pareadas;
- criar sessão nova;
- gerar QR;
- parear e reconectar após restart;
- ligação de saída 1:1;
- ligação de entrada 1:1;
- aceitar/rejeitar/encerrar;
- áudio bidirecional;
- `peerPhone` correto para chamada recebida com `@lid`;
- ownership entre operadores;
- chamada encerrada refletida corretamente no SSE;
- gravação com voz do atendente no canal L;
- gravação com voz do cliente no canal R;
- upload final no Chatspot;
- transcrição dos dois lados;
- resumo/nota privada;
- fila de gravação sobrevivendo a restart;
- nenhuma gravação duplicada pelo navegador.

Somente depois disso testar vídeo, terceiro participante e group call.
