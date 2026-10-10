<div align="center">

# 📞 Chatspot Calls Engine

**Backend de chamadas WhatsApp para navegador, construído em Go sobre Meowcaller + HyperMeow.**

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![Meowcaller](https://img.shields.io/badge/Meowcaller-VoIP-25D366?logo=whatsapp&logoColor=white)](https://github.com/purpshell/meowcaller)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

</div>

---

## O que é

Este repositório é um fork do [WaCalls](https://github.com/JotaDev66/WaCalls) usado como base para o backend do **Chatspot Calls**.

Na arquitetura v2, o servidor **não mantém mais um motor próprio do protocolo de chamadas do WhatsApp**. Sinalização, MLow, RTP/SRTP, relay, vídeo e recursos multiparte ficam a cargo do [Meowcaller](https://github.com/purpshell/meowcaller), atualmente sobre [HyperMeow](https://github.com/polymorfa/hypermeow).

O código deste projeto fica responsável pela camada de aplicação:

```text
Browser / Chatspot Calls
        │
        │ HTTP + SSE + WebRTC local
        ▼
┌────────────────────────────────────┐
│ Chatspot Calls Engine              │
│                                    │
│ sessões + QR                       │
│ API HTTP / SSE                     │
│ ownership de chamadas              │
│ bridge Browser ↔ backend           │
│ peerPhone / identidade             │
│ gravação e entrega resiliente      │
└─────────────────┬──────────────────┘
                  │
                  ▼
             Meowcaller
                  │
                  ▼
              HyperMeow
                  │
                  ▼
               WhatsApp
```

## Estado da v2

A branch `v2/meowcaller` está em desenvolvimento e **ainda não substitui a versão de teste atual**.

Os testes automatizados validam compilação, vet, formatação, race tests, TypeScript e build do cliente. A validação real contra WhatsApp e Chatspot ainda é obrigatória antes do merge.

Objetivo imediato: alcançar paridade com o fluxo de áudio que já funciona hoje antes de ativar funcionalidades novas.

### Paridade que precisa funcionar

- criar/restaurar sessões;
- QR code e pareamento;
- chamada 1:1 de entrada e saída;
- áudio bidirecional;
- aceitar, rejeitar e encerrar;
- ownership por operador;
- telefone real em `peerPhone`, sem converter LID em telefone falso;
- gravação server-side;
- WAV estéreo com atendente e cliente separados;
- fila persistente e recovery após restart;
- upload final no Chatspot;
- transcrição, resumo e nota privada pelo Chatspot Calls.

## Recursos do novo motor

A base Meowcaller usada pela v2 já oferece primitivas para:

- chamada de áudio 1:1;
- videochamada 1:1;
- upgrade áudio ↔ vídeo durante a chamada;
- adicionar participante a uma chamada existente;
- chamadas vinculadas a grupos do WhatsApp;
- reações;
- mão levantada;
- vídeo multiparte e outros recursos experimentais.

**Chamadas em grupo continuam marcadas como experimentais no Meowcaller e não são tratadas aqui como estáveis sem teste real.**

## Por que não manter o antigo motor WaCalls em paralelo?

A v2 removeu a implementação manual anterior de:

- signaling `<call>`;
- CallManager próprio;
- MLow próprio;
- RTP/SRTP/RTCP próprios;
- transport/relay próprio;
- helpers de JID específicos do motor antigo.

Manter dois motores teria aumentado duplicação, risco de divergência e custo de manutenção.

Restaram em `internal/voip/media` somente pequenos helpers genéricos de PCM e envelope de frames usados na ponte com o navegador. Eles não implementam o protocolo de chamadas do WhatsApp.

## Gravação server-side

A gravação do Chatspot Calls não depende do browser como armazenamento final.

Durante a chamada:

```text
atendente ──► agent.pcm
cliente   ──► customer.pcm
                  │
                  ▼
          alinhamento temporal
                  │
                  ▼
          WAV estéreo 16 kHz
       L = atendente / R = cliente
                  │
                  ▼
           fila persistente
                  │
                  ▼
             Chatspot Calls
                  │
                  ▼
       arquivo definitivo no Chatspot
       + transcrição + resumo + nota
```

O servidor mantém o áudio apenas enquanto precisa entregá-lo. A fila sobrevive a restart e o áudio local só é removido depois da conclusão da entrega.

A integração é opcional e fica desligada quando `RECORDING_ENABLED` não está habilitado.

Variáveis usadas pela integração:

```text
RECORDING_ENABLED
CHATSPOT_CALLS_URL
WACALLS_PASSWORD
RECORDING_DIR (opcional)
```

Nenhum valor secreto deve ser commitado no repositório.

## API de compatibilidade

O objetivo da v2 é manter a fronteira que o Chatspot Calls já consome, por exemplo:

```text
GET    /api/sessions
POST   /api/sessions
POST   /api/sessions/{sid}/pair
DELETE /api/sessions/{sid}

POST   /api/sessions/{sid}/calls
POST   /api/sessions/{sid}/calls/{id}/webrtc
POST   /api/sessions/{sid}/calls/{id}/accept
POST   /api/sessions/{sid}/calls/{id}/reject
DELETE /api/sessions/{sid}/calls/{id}

GET    /api/events?clientId=...
```

Recursos novos são aditivos; o frontend não precisa conhecer a implementação interna Meowcaller/HyperMeow.

## Identidade: `peer` e `peerPhone`

Uma chamada pode chegar com um identificador técnico como:

```text
123456789012345@lid
```

Esse valor **não é um número de telefone**.

O backend mantém:

```json
{
  "peer": "123456789012345@lid",
  "peerPhone": "5537999999999"
}
```

Quando o telefone real não puder ser resolvido, `peerPhone` fica vazio. Consumidores nunca devem extrair os dígitos do LID e tratá-los como telefone.

## Desenvolvimento

Requisitos:

- Go 1.26+
- Node 22+
- npm

```bash
go mod download
cd client && npm ci && cd ..

go run ./cmd/server -addr :8080
```

Build do cliente:

```bash
cd client
npm run build
```

Testes equivalentes ao CI:

```bash
go vet ./...
go build ./...
go test -race -count=1 ./...

cd client
npx tsc -b
npm run build
```

## Documentação da integração Chatspot

- [`docs/CHATSPOT_CALLS_V2_CONTRACT.md`](./docs/CHATSPOT_CALLS_V2_CONTRACT.md)
- [`docs/LOVABLE_V2_MIGRATION_PROMPT.md`](./docs/LOVABLE_V2_MIGRATION_PROMPT.md)
- [`docs/V2_IMPLEMENTATION_STATUS.md`](./docs/V2_IMPLEMENTATION_STATUS.md)

## Segurança

O banco de sessões contém material sensível do WhatsApp e nunca deve ser publicado.

Não commitar:

- bancos SQLite de sessão;
- backups de sessão;
- tokens;
- cookies;
- chaves privadas;
- HMAC secrets;
- `.env` de produção;
- BasicAuth real;
- gravações de chamadas.

O código pode ser público sem tornar esses valores públicos. Credenciais devem existir apenas no ambiente de runtime/deploy.

## Créditos

Este trabalho existe graças aos projetos e pesquisadores que construíram as camadas anteriores:

- [JotaDev66/WaCalls](https://github.com/JotaDev66/WaCalls), projeto original deste fork;
- [purpshell/meowcaller](https://github.com/purpshell/meowcaller), motor VoIP usado pela v2;
- [polymorfa/hypermeow](https://github.com/polymorfa/hypermeow), camada WhatsApp Web usada pelo Meowcaller atual;
- [oxidezap/whatsapp-rust](https://github.com/oxidezap/whatsapp-rust), implementação e pesquisa de referência do protocolo;
- [WhatsApp Calls Research Group](https://wacrg.org), pesquisa aberta sobre chamadas WhatsApp;
- [Pion WebRTC](https://github.com/pion/webrtc), bridge WebRTC em Go.

Contribuições genéricas que não dependam do Chatspot devem, sempre que possível, ser separadas para facilitar contribuição de volta aos projetos upstream.

## Licença

[MIT](./LICENSE)
