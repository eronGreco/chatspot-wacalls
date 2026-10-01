# Prompt para o Lovable — Chatspot Calls v2 / Meowcaller

Use este prompt quando o backend v2 estiver pronto para integração de paridade.

---

Estamos migrando o backend de chamadas do Chatspot Calls.

A implementação antiga usava o motor VoIP próprio herdado do WaCalls. A nova v2 usa **Meowcaller + HyperMeow** internamente.

IMPORTANTE: isso é uma troca interna do backend. **Não reescreva o Chatspot Calls do zero e não altere contratos que não precisam mudar.** Primeiro queremos PARIDADE total com tudo que já funciona hoje. Vídeo e chamadas em grupo serão uma segunda etapa.

Antes de alterar qualquer coisa, leia e entenda o código atual, especialmente:

- `src/lib/wacalls.server.ts`
- `src/lib/wacalls.functions.ts`
- `src/routes/api/public/wacalls/relay.ts`
- `src/routes/api/public/wacalls/recording.ts`
- fluxo atual do discador e WebRTC
- fluxo atual de criação/gerenciamento das conexões WhatsApp e QR code
- fluxo atual de gravação, transcrição, resumo e nota privada

## 1. O contrato de paridade do backend continua o mesmo

Continuar usando as rotas atuais:

```text
GET    /api/sessions
POST   /api/sessions
POST   /api/sessions/{sid}/pair
POST   /api/sessions/{sid}/logout
DELETE /api/sessions/{sid}

POST   /api/sessions/{sid}/calls
POST   /api/sessions/{sid}/calls/{callId}/webrtc
POST   /api/sessions/{sid}/calls/{callId}/accept
POST   /api/sessions/{sid}/calls/{callId}/reject
DELETE /api/sessions/{sid}/calls/{callId}

GET    /api/events?clientId=...
```

A comunicação server-side continua usando a configuração já existente de `WACALLS_URL`, `WACALLS_USERNAME`, `WACALLS_PASSWORD` e o mecanismo atual de BasicAuth/API key quando aplicável.

Não exponha credenciais no frontend.

## 2. Sessões e QR code

O fluxo continua:

```text
POST /api/sessions
        ↓
backend cria sessão
        ↓
SSE session-qr
        ↓
Lovable renderiza QR
        ↓
SSE auth-state / session-list confirma pareamento
```

Eventos relevantes:

```json
{
  "type": "session-qr",
  "sessionId": "...",
  "qr": "PAYLOAD_DO_QR"
}
```

`qr` é o conteúdo a ser convertido/renderizado como QR code. Não assuma imagem base64.

Também continuar tratando:

```json
{
  "type": "auth-state",
  "sessionId": "...",
  "paired": true,
  "state": "open"
}
```

E:

```json
{
  "type": "session-list",
  "sessions": []
}
```

Mantenha o armazenamento de metadados de conexões que o Chatspot Calls já possui. Não tente mover dados privados/sessões do WhatsApp para o banco do Lovable; as credenciais de sessão continuam no backend.

## 3. Eventos de chamada

Continuar tratando os eventos atuais:

```text
incoming
incoming-claimed
call-status
call-ended
call-list
```

Agora eles podem conter campos ADITIVOS como `media`, mas isso não deve quebrar o fluxo de áudio existente.

Exemplo de `incoming`:

```json
{
  "type": "incoming",
  "sessionId": "...",
  "id": "CALL_ID",
  "peer": "123456789@lid",
  "peerPhone": "5537999999999",
  "media": "audio",
  "offeredAt": 0
}
```

## 4. REGRA CRÍTICA: peerPhone

`peer` e `peerPhone` NÃO são a mesma coisa.

- `peer` é o identificador técnico do WhatsApp e pode ser um `@lid`.
- `peerPhone` é o telefone real resolvido pelo backend.
- Para localizar contato/conversa no Chatspot, SEMPRE prefira `peerPhone`.
- NUNCA remova dígitos de um `@lid` para inventar um telefone.
- Se `peerPhone` não existir, exiba algo como `Número não identificado` em vez de usar os dígitos do LID.

Essa regra deve valer para `incoming`, `incoming-claimed`, `call-status`, `call-ended` e `call-list`.

## 5. WebRTC do discador continua existindo

A migração para Meowcaller NÃO elimina a ponte WebRTC entre o navegador e nosso backend.

O fluxo de áudio continua usando:

```text
browser → SDP offer → backend → SDP answer
```

via:

```text
POST /api/sessions/{sid}/calls/{callId}/webrtc
```

Preserve o código de microfone, alto-falante, AudioContext e lifecycle atual até que haja motivo concreto para alterá-lo.

## 6. Gravação: não voltar a depender do browser como fonte oficial

A gravação oficial continua sendo **server-side**.

O backend grava:

```text
L = atendente
R = cliente
```

O arquivo é processado temporariamente no servidor, mas o armazenamento final é o Chatspot.

O backend continuará chamando:

```text
/api/public/wacalls/recording?action=upload-url
/api/public/wacalls/recording?action=confirm
/api/public/wacalls/recording?action=transcribe&...
/api/public/wacalls/recording?action=done
```

O endpoint atual `src/routes/api/public/wacalls/recording.ts` deve continuar sendo a autoridade para:

- pedir URL pré-assinada ao Chatspot;
- confirmar o arquivo;
- transcrever os canais agent/customer;
- montar a transcrição;
- gerar resumo com IA;
- criar nota privada no atendimento.

Não mova token do Chatspot para o backend de chamadas nem para o browser.

O HMAC continua usando `WACALLS_PASSWORD` server-side e os bytes exatos do request.

## 7. Não ativar novas features ainda

A base v2 já tem suporte arquitetural para recursos como:

```text
vídeo
upgrade áudio → vídeo
group call
adicionar participante
reações
mão levantada
hold
transferência
pickup
```

Mas nesta tarefa NÃO crie UX nova para esses recursos.

Primeiro precisamos provar que a nova v2 mantém tudo que já funcionava no fluxo de áudio atual.

Apenas deixe o parser/event handling tolerante a campos aditivos, principalmente:

```text
media: "audio" | "video"
```

sem mudar comportamento atual de áudio.

## 8. Browser recorder

Não remova definitivamente o código de fallback do browser ainda.

A configuração `WACALLS_SERVER_RECORDING=1` continua indicando que a fonte oficial é o backend.

Durante os testes da v2, preserve a capacidade de fallback, mas não deixe as duas gravações gerarem uploads duplicados.

## 9. Resultado esperado desta tarefa

Quero que você:

1. compare este contrato com o código atual do projeto;
2. ajuste apenas o que for necessário para compatibilidade com a v2;
3. preserve todas as telas/fluxos que já funcionam;
4. preserve QR, sessões e gerenciamento de conexões;
5. preserve chamada 1:1 de áudio;
6. preserve incoming/outgoing/accept/reject/end;
7. preserve `peerPhone` corretamente;
8. preserve a gravação server-side e todo o pipeline Chatspot → transcrição → resumo → nota;
9. não habilite vídeo/grupo ainda;
10. documente no final exatamente quais arquivos mudou e por quê.

Se após revisar o código você concluir que **nenhuma mudança é necessária para a etapa de paridade**, diga isso claramente e não faça alterações artificiais só porque o motor interno mudou.

O backend deve ser tratado como uma API. O fato de ele ter trocado WaCalls-internal por Meowcaller/HyperMeow não é motivo para o frontend conhecer essa implementação.

---

## Próxima etapa, NÃO executar ainda

Depois que a paridade estiver testada em chamada real, faremos outro pedido específico para adicionar:

- chamada de vídeo 1:1;
- receber videochamada;
- upgrade áudio ↔ vídeo;
- adicionar terceiro participante a uma chamada atual;
- chamadas de grupo;
- UX de vídeo e multiparte no softphone.

Não antecipe essa etapa nesta tarefa.
