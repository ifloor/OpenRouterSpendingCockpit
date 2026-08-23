# CONTEXT — OpenRouter Spending Cockpit (handoff)

Este arquivo resume o projeto para retomar o trabalho numa nova sessão sem gastar
tokens re-explorando o código. Leia só o que precisar.

## O que é

Dashboard **web local** em Go (stdlib only, zero deps) que acompanha em tempo real o
gasto no OpenRouter durante uso de agentes (kilocode etc.): saldo de créditos, uso
agregado por (minuto+modelo) e drilldown por chamada individual. Roda **junto** do
kilocode.

- **Caminho real do projeto:** `/home/igor/Development/Go/Mikha-el/OpenRouterSpendingCockpit`
  (o repo `.../OpenRouterSpendingCockpitDeepseek` foi esvaziado/abandonado — NÃO usar).
- Módulo: `github.com/igor/openrouter-costwatch`.

## Build / run

```sh
go build -o orcmon .          # requer Go instalado
OPENROUTER_MANAGEMENT_KEY=sk-or-... ./orcmon -port 8080 -interval 5s -v
# abrir http://127.0.0.1:8080
```

Flags: `-api-key` (ou env `OPENROUTER_MANAGEMENT_KEY`), `-port` (8080), `-interval` (5s), `-v`.
A chave nunca é logada completa (mascarada).

## Arquitetura / fluxo

```
main.go                 flags, wiring, HTTP server local (SSE + embed via //go:embed)
internal/openrouter/    cliente REST p/ API OpenRouter (auth Bearer mgmt key)
  client.go             GET/POST; NEW NewClientWithBaseURL(baseURL, key) p/ testes (httptest)
  analytics.go          GET /analytics/meta + POST /analytics/query + parse defensivo
  generation.go         GET /generation?id= + ParseGenerationTime
  credits.go            GET /credits
  pricing.go            GET /models (público) -> ModelCatalog (preço por token por modelo)
internal/collector/     goroutine ticker: descobre meta no boot, polla e enriquece
internal/store/         estado em memória (mutex), diffs por (minuto,modelo), cap de listas
web/                    index.html + app.js + style.css (embutidos no binário)
```

Coleta por tick: `GET /credits` (saldo) → `POST /analytics/query` agregado (dims
model+provider, granularity minute) → `POST /analytics/query` drilldown (dim
`generation_id`) → enriquece novos IDs via `GET /generation` (máx 5/tick, dedup).

Endpoints HTTP: `GET /` (HTML), `/app.js`, `/style.css`, `/api/state` (JSON snapshot),
`/api/stream` (SSE, evento `tick`). UI vanilla JS + `EventSource`.

## Decisões fechadas (importantes)

- **Fonte:** apenas management key + `analytics` + `credits` + `generation` + `models`.
  SEM proxy local, SEM alterar baseUrl do kilocode.
- **Persistência:** apenas memória (reiniciar zera). Cap de buckets (~500), gerações (~200)
  e preços (map, sem cap).
- **Drilldown não é tempo real por chamada** (eventual consistency ~30s–2min; janela máx 31 dias).

## Features adicionadas nesta rodada

1. **In tks = prompt − cached** — a coluna "In tks" da tabela de gerações mostra
   `tokens_prompt - tokens_cached`; o cacheado fica na coluna "In tks Cached".
2. **Cálculo de custo por operação (colunas de tokens)** — para cada geração com preço
   conhecido, `applyPricing` calcula `cost_in_cached`, `cost_in`, `cost_reasoning`,
   `cost_out` e `cost_calc` (soma). A UI mostra `$<custo>` ao lado da contagem de tokens
   de cada coluna (com 6 casas), e na coluna `$` mostra `valor_api (calc: <soma>)`.
   - Preço de **cached** (input_cache_read), **reasoning** (internal_reasoning).
   - Fallbacks em `pricing.go`: cached→prompt, reasoning→completion quando ausentes.
3. **Card fundido Usado/Comprado** — 1 card "Créditos — comprado / usado" com duas linhas
   rotuladas `Comprado` e `Usado` (flex `.balance-pair` em style.css).
4. **Tabela "Modelos — preço por token (in / in-cached / out)"** — card à direita dos
   cards de saldo, listando os modelos COM preço buscado, colunas Model/In/In-cached/Out
   (US$/token, `fmtPerToken`). Deve listar SÓ modelos realmente usados (não o catálogo todo).

> **ATENÇÃO — feature 4 está com problema pendente (ver seção "BUG pendente" abaixo).**

## Pricing — como funciona (importante)

- `openrouter/pricing.go` → `ListModels(ctx)` faz `GET /models` (público; o Bearer da mgmt
  key é inofensivo). Retorna `*openrouter.ModelCatalog`:
  - `Pricing map[string]ModelPricing`: preço por token, **chave = slug canônico** (`author/model`,
    ex. `deepseek/deepseek-v4-flash`).
  - `Aliases map[string]string`: **nome de exibição → slug** (cada item do /models tem `id`
    (slug) e `name` (ex. `DeepSeek: DeepSeek V4 Flash`)). Existe porque a API pode devolver
    o modelo como nome de exibição ou como slug.
  - `ModelPricing{Prompt, Completion, Cached, Reasoning}` (US$/token).
  - `normalizeModelID` (exportado): remove prefixo `~` e sufixo `:variant` (ex. `:free`).
    ATENÇÃO: também corta no primeiro `:` (necessário p/ alias de nomes com ":"), mantém a
    consistência pois aplica o mesmo normalizador em alias E lookup.
- `internal/collector/collector.go`:
  - Cache preguiçoso: `catalog *openrouter.ModelCatalog` + `pricingLoaded bool` (mutex).
    `pricingFor(ctx, model)` carrega o catálogo NO MÁXIMO 1x, depois busca por slug OU
    nome de exibição (`catalogLookup` resolve alias), e quando acha `store.SetPrice(...)`
    sob o **slug canônico**.
  - `pollAggregate` chama `c.pricingFor(ctx, r.Model)` para cada modelo dos rows agregados
    (uso real dos últimos 5 min) — para popular a tabela de preços sem depender do drilldown.
  - `applyPricing(ctx, gi)` (no drilldown/enriquecimento) usa `gi.ModelPermaSlug` (campo do
    /generation) de preferência a `gi.Model` (nome de exibição) para o lookup.
  - `GenerationInfo` (store) ganhou `ModelPermaSlug`, `HasPricing` e os campos de custo
    (`cost_in_cached`, `cost_in`, `cost_reasoning`, `cost_out`, `cost_calc`) — ver
    `internal/store/store.go`.
- Teste de integração: `internal/collector/collector_test.go` (`TestPricingFlowsToSnapshot`)
  monta um `httptest.Server` fake (`/credits`, `/analytics/meta`, `/analytics/query`,
  `/generation`, `/models`) e verifica que o preço flui para `snapshot.Prices` e que a
  geração fica `HasPricing=true` com `CostCalc>0`. **Passa localmente.**

## BUG PENDENTE — ~~tabela de preços vazia no runtime~~ (RESOLVIDO em 2026-08-22)

> **ATUALIZAÇÃO:** a tabela estava vazia porque o `pricingFor` quebrado NUNCA
> compilava (referenciava `catalogLookup` e `cat` inexistentes) — `go build`
> falhava antes de gerar binário. A refatoração do mapeamento corrigiu o fluxo:
> - `openrouter.ModelCatalog` agora é um tipo com `Lookup(model) (price, canonicalSlug, ok)`
>   e índice de nome de exibição exato (sem truncar em `:`), em `pricing.go`.
> - O pacote `internal/collector/cmodel` (rascunho) foi REMOVIDO; o collector
>   usa direto `store.ModelPrice`.
> - Carregamento preguiçoso do catálogo em `ensureCatalog` (máx. 1x, sem mutex;
>   roda na goroutine do poll); erro de fetch não é cacheado (retenta no tick).
> - Sobre provider em pricing: `/models` do OpenRouter só expõe preço agregado
>   (não por-provider), então o preço NÃO é indexado por provider — a decisão
>   de design é `(slug) -> preço`, resolvido por slug OU nome de exibição.
>
> Se a tabela ainda vier vazia, é o diagnóstico (b): o modelo usado realmente
> não está no catálogo — ver `pricing: model not found in /models catalog: <id>`.

SINTOMA: `renderPrices` recebe `s.prices == []` sempre. No SSE o handler está correto
(`render(JSON.parse(ev.data))` — app.js:172); o `{"isTrusted":true}` antigo era só o log
do objeto `ev`. Ou seja, o **backend** envia `prices` vazio no `/api/state` / SSE.

O teste de integração passa, então a diferença está no **dado real** (sua conta/servidor).

DIAGNÓSTICO JÁ ADICIONADO (sempre ativo, em `pricingFor`/`logPricingMiss`):
- Ao carregar o catálogo: `pricing: loaded N model prices from /models`
- Em falha de fetch: `pricing: catalog fetch failed: <err>`
- Para cada modelo usado que não consta no catálogo (uma vez por modelo):
  `pricing: model not found in /models catalog: <id>`

PRÓXIMO PASSO para diagnosticar:
1. `go build -o orcmon . && OPENROUTER_MANAGEMENT_KEY=sk-or-... ./orcmon -port 8080 -interval 5s`
2. Fazer uso real (rodar o agente).
3. Verificar no **log do servidor**: se aparece `loaded N ...` e se há `model not found: <id>`.
4. Abrir `http://127.0.0.1:8080/api/state` e conferir `"prices"` e `"last_error"`.

HIPÓTESES em ordem de probabilidade:
- (a) Os modelos usados realmente não estão no catálogo `/models` (ex.: custom). Se for isso,
      a tabela ficará vazia por definição da política "só usados".
- (b) O fetch de `/models` falha no seu ambiente (rede/auth) → banner de erro com
      `pricing: ...`.
- (c) Algum descompasso de id entre o que a API entrega e o catálogo (permaslug/nome).

Caso queira que a tabela SEMPRE mostre algo, a alternativa é popular com **todo o catálogo**
(`go c.loadPricing(ctx)` no início de `Run`) — isso já foi testado e funcional, mas o usuário
questionou carregar tudo de início; por isso ficou "só usados".

## Gotchas / correções já aplicadas (NÃO regredir)

1. **`/analytics/meta` retorna itens como OBJETOS**, não strings:
   `{"name":"request_count","display_label":...,"display_format":...}`.
   `NameList` (UnmarshalJSON custom em `analytics.go`) aceita string OU objeto com campo `name`.
2. **`time.Time` pode estourar o JSON** (`year outside of range [0,9999]`): `cachedAt` e
   `created_at` podem vir em escala s/ms/µs/ns ou fora de faixa. `normalizeUnix` +
   `validTime` (ano entre 2000–2200) sanitizam; fora → `time.Time{}` (zero).
3. **Counts no analytics podem vir como string** — `RowValue` parse defensivo.
4. **Time Key do row**: aceita `date__*` OU `created_at__*` (`RowTimeKey`).
5. **Provider como dimensão** só é incluído se `meta.Dimensions` contém `provider`
   (nomes validados contra o meta no boot — não assumir nomes fixos).
6. **Dims reais conhecidas:** model, variant, api_key_id, provider, origin, country,
   data_region, finish_reason, workspace, app, user, external_user,
   context_length_bucket, generation_id, session_id.
   **Granularidades:** minute, hour, day, week, month. **Ops:** eq, neq, in, not_in, gt, gte, lt, lte.
   **Métricas:** request_count, total_usage, tokens_total, tokens_prompt, tokens_completion, reasoning_tokens (…).
7. **Preço encontrado por slug OU nome de exibição** — NÃO usar só o `model` (nome) no
   lookup; o catálogo é chaveado por slug. Usar `model_permaslug` do /generation (ver pricing acima).
8. **`NewClientWithBaseURL`** foi adicionado a `client.go` para poder apontar o client a um
   servidor fake em testes (httptest). Usar nos testes.

## UI — estado atual

- Cards no topo (flex row, `.cards-row`): Sobra, Créditos comprado/usado (fundido), e o card
  de Modelos/preços.
- Barra de reload no topo (`#reloadBar`) que enche até o `interval_ms` e pisca a cada tick;
  header mostra `#lastUpdate` "atualizado <hh:mm:ss>".
- Card "Atraso dos dados (cachedAt)" foi REMOVIDO pedido pelo usuário.
- Rodapé "Dados voláteis…" foi REMOVIDO a pedido.
- Texto "API OK — metrics:[…].; dims:[…]…" no header foi trocado por "conectado à API OpenRouter".
- Colunas da tabela de gerações: Criada em, Modelo, Provedor, In tks Cached(inclui $calc),
  In tks(inclui $calc; já subtraído o cache), Reasoning tks($calc), Out tks($calc), $(com " (calc: …)").
- **Ambas as tabelas são ordenadas por data DESC no app.js** (hits por `minute`,
  generations por `created_at`).
- `fmtPerToken` em app.js formata US$/token sem zeros à direita (até 10 casas).

## Estado de versionamento

`git status` no repo Mikha-el mostra tudo **untracked** (go.mod, main.go, internal/, web/)
e README.md modificado — **nada commitado ainda**. Origem está em `origin/main` (2 commits
antigos "Initial idea files"). Considere `git add` + commit inicial do que está pronto.

## CI / Release (adicionado 2026-07-23)

- **NÃO há Docker** — o usuário corrigiu: sem push de imagem. Em vez disso: binários
  + GitHub Release pública.
- `VERSION` (root) = valor da versão; workflow lê com `tr -d '[:space:]'` → tag `v<version>`.
- `.github/workflows/release.yml`: push em `main` (ou `workflow_dispatch` com
  `force=true`) → se a release `v<version>` JÁ existir, faz skip; senão
  `setup-go 1.26.5` → build `CGO_ENABLED=0` para linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64, windows/amd64 (`.exe`) → `gh release create
  v<version>` com os binários `openrouter-spending-cockpit-<v>-<os>-<arch>`.
- Release = editar `VERSION` + push. Recriar mesma versão = Actions → Run workflow
  → `force=true`.

## Próximos passos candidatos (não finalizados)

- **Diagnosticar e corrigir o BUG pendente da tabela de preços vazia** (ver seção acima).
- Fazer o **primeiro commit** do estado atual.
- Endpoints opcionais da spec: `GET /key` e `GET /keys` (uso por chave) para atribuir gasto por key na UI.
- Indicador de `truncated`/partial mais visível (já existe banner).
- Testar com `-interval` 2s e 60s; reconciliar env vs flag da chave.
- Filtros na UI por janela temporal / busca.

## Para validar ao vivo

Com a management key real, boot deve logar `analytics meta OK: …` (sem WARNING) e as
tabelas devem encher dentro de ~1–2 min após uso real no kilocode. Para a tabela de preços,
observar os logs de `pricing:` descritos na seção BUG.
