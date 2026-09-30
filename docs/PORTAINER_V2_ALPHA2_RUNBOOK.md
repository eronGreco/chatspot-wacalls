# Chatspot Calls v2.0.0-alpha.2 — homologação no Portainer

Esta é a primeira build autorizada para teste real da migração **Meowcaller + HyperMeow**.

A `v2.0.0-alpha.1` foi substituída antes do deploy e não deve ser usada.

## Release

- Tag: `v2.0.0-alpha.2`
- Linux/amd64: `https://github.com/eronGreco/chatspot-wacalls/releases/download/v2.0.0-alpha.2/wacalls-server-linux-amd64`
- SHA-256: `cb0adc3ce2f8746530218bad936c06904e210852893ca02b1219ddf9f72e2290`
- Checksum: `https://github.com/eronGreco/chatspot-wacalls/releases/download/v2.0.0-alpha.2/wacalls-server-linux-amd64.sha256`

## O que NÃO muda nesta etapa

Preservar:

- stack `wacalls`;
- volume externo `wacalls_data` montado em `/data`;
- `/data/wacalls.db`;
- `/data/recordings`;
- serviço proxy `wacalls_proxy` / socat;
- rede `FOCOSnet`;
- Traefik e domínio `calls.focos.app`;
- relay atual do Chatspot;
- porta do backend `:18080`;
- limite `-max-calls-per-session 8`;
- autenticação existente;
- secrets atuais.

Nesta homologação trocamos **somente o binário do backend**. O diretório estático atual `/data/runtime/client` pode continuar no volume; ele não precisa ser atualizado para validar o Lovable/Chatspot Calls.

## Regra crítica

**Nunca rode v1 e v2 simultaneamente usando o mesmo `/data/wacalls.db`.**

O serviço principal deve usar atualização `stop-first`, com uma única réplica. Isso evita duas instâncias tentando operar as mesmas sessões do WhatsApp.

## 1. Backup antes da troca

Com o serviço principal parado ou durante a janela em que não há processo usando o banco:

```bash
mkdir -p /root/wacalls-backups

docker run --rm \
  -v wacalls_data:/data:ro \
  -v /root/wacalls-backups:/backup \
  alpine:3.20 \
  sh -c 'tar czf /backup/wacalls-before-v2-alpha2-$(date +%Y%m%d-%H%M%S).tar.gz -C /data .'

ls -lh /root/wacalls-backups
```

Não prossiga se o arquivo não existir ou estiver vazio.

## 2. Variáveis de gravação

A v2 mantém os mesmos nomes já usados na linha atual. Confirmar no serviço principal:

```text
RECORDING_ENABLED=1
RECORDING_DIR=/data/recordings
CHATSPOT_CALLS_URL=https://calls.chatspot.com.br
WACALLS_PASSWORD=<preservar exatamente o secret atual>
```

`RECORDING_DIR` pode ser omitida porque o padrão, com o banco em `/data/wacalls.db`, já resulta em `/data/recordings`. Manter explícita facilita auditoria.

**Não adicionar `WACALLS_API_KEY` nesta primeira homologação.** Isso mudaria o contrato de autenticação e não faz parte da migração do motor de chamadas.

`WACALLS_USERNAME` e demais valores atuais de autenticação/Traefik devem permanecer como estão.

## 3. Launcher da alpha 2

No serviço principal `wacalls_wacalls`, mantenha `alpine:3.20`, `network_mode: host`, o volume `/data` e as demais opções existentes. O launcher pode ser substituído pelo bloco abaixo:

```sh
set -eu

apk add --no-cache ca-certificates curl >/dev/null

VERSION="v2.0.0-alpha.2"
RUNTIME="/data/runtime"
BIN="$RUNTIME/wacalls-server"
MARKER="$RUNTIME/.wacalls-version"
TMP="$RUNTIME/wacalls-server.new"
URL="https://github.com/eronGreco/chatspot-wacalls/releases/download/${VERSION}/wacalls-server-linux-amd64"
EXPECTED_SHA="cb0adc3ce2f8746530218bad936c06904e210852893ca02b1219ddf9f72e2290"

mkdir -p "$RUNTIME" /data/recordings

CURRENT=""
if [ -f "$MARKER" ]; then
  CURRENT="$(cat "$MARKER" 2>/dev/null || true)"
fi

if [ ! -x "$BIN" ] || [ "$CURRENT" != "$VERSION" ]; then
  echo "Baixando Chatspot Calls $VERSION..."
  rm -f "$TMP"
  curl -fL --retry 5 --retry-delay 2 "$URL" -o "$TMP"
  echo "$EXPECTED_SHA  $TMP" | sha256sum -c -
  chmod +x "$TMP"
  mv -f "$TMP" "$BIN"
  printf '%s' "$VERSION" > "$MARKER"
  echo "Chatspot Calls $VERSION instalado."
fi

exec "$BIN" \
  -addr :18080 \
  -db /data/wacalls.db \
  -static /data/runtime/client \
  -max-calls-per-session 8
```

O checksum é obrigatório. Se não bater, o processo deve falhar antes de substituir o binário anterior.

## 4. Política de update do serviço

No `deploy` do serviço principal, usar uma única réplica e `stop-first` durante a homologação:

```yaml
deploy:
  replicas: 1
  update_config:
    parallelism: 1
    order: stop-first
    failure_action: rollback
```

Preserve as constraints já existentes da stack.

## 5. Primeira subida

Depois de atualizar a stack, verificar os logs:

```bash
docker service logs -f --tail 200 wacalls_wacalls
```

A validação inicial termina somente quando:

- o binário alpha 2 é baixado e o SHA passa;
- o processo escuta em `:18080`;
- o banco abre sem erro fatal;
- as sessões existentes são restauradas;
- `GET /api/sessions` responde pela rota já usada pelo sistema.

Se qualquer item falhar, parar a homologação e fazer rollback antes de testar chamadas.

## 6. Matriz de paridade

Executar nesta ordem:

1. sessões existentes restauradas;
2. criação de sessão nova e QR;
3. chamada 1:1 de saída;
4. áudio atendente → cliente;
5. áudio cliente → atendente;
6. encerramento iniciado pelo atendente;
7. encerramento iniciado pelo cliente;
8. chamada 1:1 de entrada;
9. aceitar;
10. rejeitar;
11. `peerPhone` correto em chamadas normais e quando `peer` for LID;
12. ownership por operador;
13. gravação server-side;
14. WAV estéreo com L=atendente e R=cliente;
15. upload definitivo para o Chatspot;
16. transcrição, resumo e nota privada esperados;
17. remoção dos artefatos locais após sucesso;
18. falha artificial de entrega + restart + recovery/retry.

Não usar vídeo, grupo, add-participant, hold, transferência ou pickup como critério desta primeira aprovação. Eles serão testados depois da paridade de voz.

## 7. Gravação

A arquitetura continua:

```text
atendente/browser PCM
  → servidor grava trilha agent
  → Meowcaller
  → WhatsApp

WhatsApp
  → Meowcaller
  → servidor grava trilha customer
  → browser
```

No fim:

```text
WAV estéreo temporário no servidor
  → upload-url no Chatspot Calls
  → upload do arquivo
  → confirmação
  → transcrição agent/customer
  → done
  → exclusão local somente após sucesso
```

O servidor **não é storage definitivo** das gravações.

## 8. Rollback

Se a alpha 2 falhar:

1. pare a tarefa v2;
2. não apague `wacalls_data`;
3. restaure o launcher/versão anterior;
4. se houver qualquer dúvida sobre alteração do banco, restaure o backup feito antes da homologação;
5. suba somente a v1;
6. confirme sessões e uma chamada de voz antes de considerar o rollback concluído.

Nunca inicie a v1 enquanto a v2 ainda estiver conectada às mesmas sessões.

## Critério de promoção

O PR `#3` permanece Draft. A v2 só poderá substituir definitivamente a linha atual depois de todos os gates de paridade acima passarem em chamada real.
