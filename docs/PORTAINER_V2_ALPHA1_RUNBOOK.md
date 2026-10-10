# Chatspot Calls v2.0.0-alpha.1 — NÃO USAR

Esta build foi gerada para a primeira checagem do pipeline, mas foi **substituída antes do deploy de homologação**.

Durante a revisão de paridade foi identificado que o registro final de uma chamada de saída podia sobrescrever o `peerPhone` já resolvido. A correção entrou na linha `v2/meowcaller` antes de qualquer troca no Portainer.

Use somente a release `v2.0.0-alpha.2` e o documento:

`docs/PORTAINER_V2_ALPHA2_RUNBOOK.md`

Também foi corrigida a documentação das variáveis da gravação. O backend usa as mesmas variáveis da implementação atual:

```text
RECORDING_ENABLED=1
RECORDING_DIR=/data/recordings
CHATSPOT_CALLS_URL=https://calls.chatspot.com.br
WACALLS_PASSWORD=<preservar o valor atual>
```

Não introduzir `WACALLS_API_KEY` nesta primeira homologação e não substituir as variáveis acima por nomes novos.
