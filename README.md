# OpenRouterSpendingCockpit

Dashboard web local para acompanhar em tempo real o gasto no OpenRouter durante o uso de agentes (kilocode e outros). Serve crédito restante, uso agregado por minuto+modelo e drilldown por chamada individual.

## Como criar a management key (pré-requisito)

Necessário para `/analytics/*`, `/credits` e `/generation`:

1. Acesse https://openrouter.ai/settings/keys
2. Crie uma **management key**.
3. **Cuidado:** a management key tem poder elevado — não commitar nem expor.

## Build

```sh
go build -o orcmon .
```

Requisito: Go instalado (`go version`).

## Run

```sh
# via env
OPENROUTER_MANAGEMENT_KEY=sk-or-... ./orcmon -port 8080 -interval 5s -v

# ou via flag
./orcmon -api-key sk-or-... -port 8080 -interval 5s
```

Abra http://127.0.0.1:8080

### Flags

| Flag | Default | Descrição |
|------|---------|-----------|
| `-api-key` | env `OPENROUTER_MANAGEMENT_KEY` | Chave de gerenciamento (nunca logada completa; mascarada). |
| `-port` | `8080` | Porta HTTP local. |
| `-interval` | `5s` | Intervalo de polling (teste com `2s` ou `60s`). |
| `-v` | off | Logs verbosos do collector. |

## Release / CI

A cada push para `main`, o GitHub Actions (`.github/workflows/release.yml`) lê o
ficheiro `VERSION` na raiz do repo, gera binários estáticos (sem dependências) e
cria uma **GitHub Release** pública com a tag `v<version>`:

- Linux: `amd64`, `arm64`
- macOS: `amd64` (Intel), `arm64` (Apple Silicon)
- Windows: `amd64`

Para fazer um novo release, basta editar o `VERSION` (ex.: `0.1.0` → `0.1.1`) e
fazer push — a release é criada automaticamente em
https://github.com/ifloor/OpenRouterSpendingCockpit/releases.

Se a release da versão já existir, o workflow faz *skip* (para forçar a
recriação, correr o workflow manualmente com `force=true`).

```sh
# correr um binário da release
./openrouter-spending-cockpit-0.1.0-linux-amd64 -api-key sk-or-...
```

## Como funciona

- Coleta periodicamente `GET /credits` (saldo), `POST /analytics/query` em dois modos e `GET /generation` para enriquecer chamadas.
- Estado **só em memória** — reiniciar zera o histórico.
- Página servida pelo próprio binário (embutida via `//go:embed`), com atualização via SSE (`/api/stream`) e snapshot JSON (`/api/state`).
- No boot, chama `GET /analytics/meta` para enumerar `metrics`/`dimensions`/`granularities`/`operators` reais e usa exatamente esses nomes (não assume).

## Limitações conhecidas

- **Drilldown não é tempo real por chamada:** `generation_id` (analytics) e `/generation` são eventualmente consistentes; chamadas recentes aparecem com atraso de ~30s–2min. Janela máxima de **31 dias** para a dimensão `generation_id`.
- **Custo de enriquecimento:** cada chamada nova gera um `GET /generation`; limitado por tick (máx. 5 IDs/tick) e dedup por ID para evitar rajadas/rate limit (429 → backoff no próximo tick).
- **`metadata.truncated`:** em volume alto a resposta pode ser parcial — a UI avisa e totais não devem ser lidos como absolutos.
- **Atraso de propagação:** analytics eventualmente consistente; o `request_count`/`cost` do minuto corrente é parcial até o minuto fechar. `cachedAt` mostra a idade dos dados.
- **Requer management key**, que não é a chave normal de billing/uso.
- **Histórico volátil** (somente memória; cap de buckets e de gerações).

## Estrutura

```
main.go                          flags, wiring, HTTP (SSE + embed)
internal/openrouter/client.go    cliente REST (auth, erros HTTP)
internal/openrouter/analytics.go meta + query + parsing defensivo
internal/openrouter/generation.go GET /generation
internal/openrouter/credits.go    GET /credits
internal/collector/collector.go   ticker, diff, enriquecimento
internal/store/store.go           estado em memória (mutex)
web/                             index.html, app.js, style.css (embed)
```