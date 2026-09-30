# Chatspot Calls v2.0.0-alpha.1 — homologação no Portainer

Este runbook é para validar a migração para **Meowcaller + HyperMeow** sem promover a v2 para produção antes da paridade com a versão atual.

## Release de homologação

- Tag: `v2.0.0-alpha.1`
- Binário Linux/amd64: `https://github.com/eronGreco/chatspot-wacalls/releases/download/v2.0.0-alpha.1/wacalls-server-linux-amd64`
- SHA-256 esperado: `0c26db58458664d5065609052500b0ad62dfac3d4f555d952f8684ac2630da56`
- Arquivo de checksum: `https://github.com/eronGreco/chatspot-wacalls/releases/download/v2.0.0-alpha.1/wacalls-server-linux-amd64.sha256`

## Regra crítica

**Nunca execute v1 e v2 simultaneamente apontando para o mesmo `/data/wacalls.db`.**

As duas instâncias tentariam operar as mesmas sessões vinculadas do WhatsApp. Para testar restauração real das sessões atuais, pare a v1 antes de iniciar a v2.

## Estratégia de homologação

A primeira homologação deve trocar somente o binário, mantendo o volume `/data` e a mesma porta interna/host já usada pela instalação atual.

Assim validamos exatamente o que importa:

- restauração das sessões existentes;
- QR e novas sessões;
- compatibilidade HTTP/SSE;
- chamadas reais;
- `peerPhone`;
- gravações e fila persistente;
- restart/recovery.

Traefik, proxy socat e domínio público não precisam mudar nesta etapa se continuarem apontando para a mesma porta do backend.

## 1. Backup antes da troca

Antes de iniciar a v2, faça um snapshot do volume atual. No manager do Swarm, com a v1 parada:

```bash
mkdir -p /root/wacalls-backups

docker run --rm \
  -v wacalls_data:/data:ro \
  -v /root/wacalls-backups:/backup \
  alpine:3.20 \
  sh -c 'tar czf /backup/wacalls-before-v2-alpha1-$(date +%Y%m%d-%H%M%S).tar.gz -C /data .'

ls -lh /root/wacalls-backups
```

Não prossiga se o arquivo de backup não existir ou estiver vazio.

## 2. Download e validação do binário

O launcher da stack pode usar este fluxo:

```sh
set -eu

VERSION="v2.0.0-alpha.1"
BIN="/tmp/wacalls-server"
URL="https://github.com/eronGreco/chatspot-wacalls/releases/download/${VERSION}/wacalls-server-linux-amd64"
EXPECTED_SHA="0c26db58458664d5065609052500b0ad62dfac3d4f555d952f8684ac2630da56"

curl -fL --retry 5 --retry-delay 2 "$URL" -o "$BIN"
echo "$EXPECTED_SHA  $BIN" | sha256sum -c -
chmod +x "$BIN"

exec "$BIN" -addr :18080 -db /data/wacalls.db
```

Se a stack atual já possui um mecanismo de version marker/download, preserve-o e troque apenas a versão, URL e SHA esperados.

## 3. Volume e porta

Preservar:

```text
/data/wacalls.db
/data/recordings
```

E manter o backend ouvindo na mesma porta usada hoje. Na instalação Chatspot atual, o launcher usa `:18080` no serviço principal e o proxy encaminha para ele.

Não crie um banco vazio para este primeiro teste, pois isso impediria validar a restauração das sessões atuais.

## 4. Variáveis de ambiente que não podem sumir

Preserve as variáveis atuais de segurança/API e, para o pipeline de gravação da v2, confirme:

```text
WACALLS_CHATSPOT_BASE_URL
WACALLS_CHATSPOT_HMAC_SECRET
WACALLS_RECORDING_TOKEN          # se utilizado atualmente
WACALLS_RECORDING_DIR=/data/recordings
WACALLS_API_KEY                  # se utilizado atualmente
```

A gravação não deve ter storage definitivo no servidor. O servidor apenas mantém os artefatos temporários/fila até concluir a entrega ao Chatspot.

## 5. Ordem de validação

Execute nesta ordem e interrompa a homologação no primeiro erro estrutural:

1. backend inicia sem erro fatal;
2. `GET /api/sessions` responde;
3. sessões previamente pareadas aparecem e reconectam;
4. criar uma sessão nova e receber QR via SSE;
5. ligação 1:1 de saída;
6. áudio atendente → cliente;
7. áudio cliente → atendente;
8. encerramento iniciado por cada lado;
9. ligação 1:1 de entrada;
10. aceitar, rejeitar e encerrar;
11. `peerPhone` correto quando `peer` é LID;
12. ownership por operador;
13. gravação estéreo gerada;
14. canal L = atendente e R = cliente;
15. upload final no Chatspot;
16. transcrição/resumo/nota esperados;
17. diretório temporário removido após sucesso;
18. simular falha de entrega, reiniciar o backend e confirmar recovery/retry.

Vídeo, grupo, add-participant, hold, transfer e demais recursos novos ficam fora desta primeira aprovação de paridade.

## 6. Logs úteis durante a homologação

Acompanhe o serviço principal pelo Portainer ou via Docker:

```bash
docker service logs -f --tail 200 wacalls_wacalls
```

Para falhas de gravação, procurar por mensagens relacionadas a `recording`, `delivery`, `retry`, `recovery`, `upload`, `transcribe` e `done`.

## 7. Rollback

Se a v2 falhar:

1. pare a v2;
2. não apague o volume;
3. restaure o launcher/binário da versão anterior;
4. se o banco tiver sido alterado de forma incompatível ou houver qualquer dúvida sobre integridade, restaure o snapshot criado antes da homologação;
5. suba novamente apenas a v1;
6. confirme sessões e chamada de voz antes de encerrar o rollback.

Nunca inicie a v1 novamente enquanto a v2 ainda estiver conectada ao mesmo banco/sessões.

## Critério para promover a v2

A v2 só pode substituir a linha atual depois de todos os gates de paridade acima passarem em chamada real. O PR `#3` deve continuar Draft até isso acontecer.
