<!-- updated: 2026-09-09 -->
# SDK — idéation de features (cartographie écosystème Go ↔ SDK ↔ framework)

> Référencé par `docs/adr/0013-sdk-crypto-domain.md:157`. Ce document a vécu
> sous `.claude/contexts/`, que `.gitignore` exclut ; il a été **déplacé sous
> `docs/`** — voir §11 pour la raison, qui n'existait pas quand la question
> a été posée.

## 1. Méthode et sources

Trois corpus croisés, tous relevés le 2026-09-09 :

1. **GitHub, dépôts Go les plus étoilés** — top 300 (`stars:>3000`), puis
   ~50 recherches par `topic:` pour isoler les *bibliothèques* du bruit
   applicatif (le top brut est dominé par Kubernetes, Docker, Ollama… qui
   n'apprennent rien à un SDK).
2. **Découpage en composants des « Symfony du Go »** — `gogf/gf` (13,3k),
   `go-kratos/kratos` (25,9k), `zeromicro/go-zero` (33,3k), `gofr-dev/gofr`
   (20,9k). `gf` est le plus proche du modèle SF : son arborescence
   (`container/ crypto/ database/ encoding/ errors/ frame/ i18n/ net/ os/ test/
   text/ util/`) est une liste de courses directement exploitable.
3. **Liste officielle des composants Symfony** (symfony.com/components, 141
   entrées) — dépouillée des polyfills, bundles, bridges et packs, il reste
   ~55 composants réels.

État réel du SDK constaté sur disque (pas d'après la doc) : 9 primitives kernel,
11 domaines core, 26 packages publics `pkg/v1/*`, 33 ADR.

## 2. Le principe de partage SDK / framework

C'est la décision structurante, et elle doit être prise **avant** la première
feature, sinon le SDK absorbe le framework.

> **Le SDK livre des mécanismes et des ports. Le framework livre du câblage, des
> conventions et des défauts.**

Symfony illustre exactement ça : `Validator`, `Console`, `Lock`, `Messenger`
sont des *composants* utilisables sans SF ; `FrameworkBundle`, `MakerBundle`, la
convention `config/packages/*.yaml` et le profiler sont le *framework*. Doctrine
n'est même pas dans SF — il y entre par un bridge.

Cinq questions, une seule réponse « non » suffit à exclure du SDK :

1. Utilisable **hors** du framework, seul, par un tiers ?
2. C'est un **mécanisme**, pas une **convention** ?
3. Faisable stdlib-only — ou la dépendance est-elle isolable dans `third-party/` ?
4. Correct sur les 8 GOOS (ADR 0018), ou refus typé `UnsupportedPlatform` ?
5. S'exprime en **port + registre**, pas en opinion figée ?

## 3. Ce que le SDK couvre déjà (référence, pour ne pas re-proposer)

> **Instantané du 2026-09-09**, pris avant la vague Phase-B que ce document a
> lancée : `codec` compte depuis 24 formats (`form`, `multipart`), `transform`
> ajoute `zlib`, `id` ajoute NanoID, KSUID et TypeID, et les plages `0.3.40`
> et `0.3.41` sont prises. L'inventaire vivant est la liste des domaines du
> `CLAUDE.md` racine, et la table des plages est `codeRangeOwners`
> (ADR 0035) — pas ce tableau, laissé tel qu'il était pour garder le point de
> départ des décisions qui suivent.

| Besoin | Couvert par | Réserve |
|---|---|---|
| Logging structuré | `logger` (1 alloc/emit, multi-sink, 8 middlewares) | — |
| Sérialisation format | `codec` (22 formats) | niveau **octet**, pas objet |
| Erreurs typées | `errs` (quad pointé, public/privé, trail) | — |
| Crypto | AEAD, hash, sign, MAC, KDF, agreement, password | — |
| Compression | `transform` | **gzip + flate uniquement** |
| Process OS | `proc` (6 façades) | — |
| Identifiants | `id` (UUIDv4/v7, ULID, snowflake) | — |
| Résilience | retry, breaker, ratelimit, bulkhead, timeout | — |
| Métriques | counter/gauge/histogram + exporter texte | **pas de labels** (ADR 0027 différé) |
| Configuration | env + fichier + merge + poll-watch | pas de schéma, pas de secrets |
| Réseau | `net` : serveur TCP/UDP/TLS + middlewares + phases, client à policy, identité TLS | pas de WS/SSE, pas de discovery |
| Cache | `kernel/cache` LRU+TTL | primitive in-proc, **pas un domaine** |
| Horloge, ring, worker, batcher, recycler, snapshot, buffer | kernel | — |

Slots de code d'erreur libres : **core `0.2.12`–`0.2.15` puis `0.2.18+`**
(`0.2.16` réservé logger, `0.2.17` pris par `level`) ; **service `0.3.40+`**.

## 4. Tier A — primitives `kernel` (générique + stdlib-only)

Passent la règle 1. Garde-fou : le précédent ADR 0010 exige **≥ 2 consommateurs
concrets** avant d'admettre une primitive. Ne pas ouvrir ce tier en grappe.

| Primitive | Demande observée | Consommateurs SDK attendus |
|---|---|---|
| **`singleflight[K,V]`** | `x/sync/singleflight`, universel | cache anti-stampede, config, resolver DNS |
| **`group`** (concurrence structurée + capture de panic) | `sourcegraph/conc` 10,4k ; `x/sync/errgroup` | lifecycle, queue, health |
| **`semaphore`** (pondéré) | `x/sync/semaphore` | bulkhead, queue, vfs |
| **`set[T]`** | `golang-set` 4,7k ; `gods` 17,5k | validation, authz, config |
| **`orderedmap[K,V]`** | `gf/gmap` ; JSON à ordre stable | codec, config, mapper |
| **`heap[T]`** / file à priorité | `container/heap` non générique | scheduler, queue |
| **`deque[T]`** | `gods` | queue, batcher |
| **`radix[T]`** (trie de préfixes) | routage, ACL, préfixes IP, clés de config | net, authz, config |
| **`fsm[S,E]`** | `qmuntal/stateless` ; SF `Workflow` | queue, lifecycle, breaker |
| **`bloom`** | section dédiée dans awesome-go | cache, dedup queue |
| **`topic[T]`** (pub/sub in-proc typé) | socle d'un event bus | events, metrics, logger |
| **`stopwatch`** | SF `Stopwatch` | metrics, trace |

**À refuser ici** : un fourre-tout façon `samber/lo` (21,4k) ou `duke-git/lancet`
(5,3k). La règle 5 (« pas de stub, pas de placeholder ») et la règle 1 le
proscrivent : un package `utils` n'a pas de vocabulaire, donc pas de frontière,
donc il grossit sans fin. Chaque helper doit entrer par la porte d'une primitive
nommée.

## 5. Tier B — nouveaux domaines `core` + `service` (un ADR chacun)

Classés par valeur pour le futur framework.

### B1. `lifecycle` — le plus haut rendement, à faire en premier

Démarrage/arrêt ordonnés par dépendances, drain, readiness/liveness, câblage des
signaux. C'est la vraie valeur de `uber-go/fx` (7,7k) une fois la DI mise de
côté, et c'est le pendant de `HttpKernel` côté cycle de vie.

Le SDK a déjà **toutes les pièces** et aucun assemblage : `proc/signal`,
`proc/sdnotify`, `net/server` (drain), `kernel/clock`. Un framework ne peut pas
exister sans cette couche, et chaque consommateur la réécrit mal.
→ core `0.2.12`. Aucune dépendance.

### B2. `validation`

SF `Validator` ; `go-playground/validator` **20,1k**, `ozzo-validation` 4,1k.
Port `Constraint` + registre (le modèle codec/writer s'y transpose tel quel) +
valeur `Violation` portant un `errs.Code`. API programmatique **et** par tags.
Consommé par : config (schéma), le futur `mapper`, les forms du framework.
→ core `0.2.13`.

### B3. `cache` promu de primitive à domaine

`kernel/cache` reste la primitive. Il manque le domaine : port `Store` +
registre, **tags et invalidation par tag**, **protection anti-stampede** (via
`kernel/singleflight`), chaînage L1/L2. C'est exactement le delta entre une
`map` et SF `Cache` / `bigcache` (8,2k) / `fastcache` (2,4k). Redis en
`third-party/`.

### B4. `session`

`alexedwards/scs` 2,6k ; SF sessions. Port `Store` + impls mémoire/fichier ;
cookie scellé via le `crypto` AEAD déjà présent. Le framework y branche son
firewall.

### B5. `token`

`golang-jwt/jwt` **9,2k** ; PASETO. Entièrement faisable sur la crypto stdlib
que le SDK expose déjà (ECDSA, Ed25519, HMAC). Ports `Issuer`/`Verifier` +
registre par algorithme, JWK/JWKS en extension `crypto`.

### B6. `authz`

`casbin` **20,4k**, `openfga` 5,7k, `spicedb` 7,0k ; SF voters. Port `Policy` →
`Decision`, évaluateurs RBAC et ABAC. **Garder le moteur simple** : pas de DSL,
pas de Zanzibar. Le DSL est une opinion → framework ou third-party.

### B7. `scheduler`

`robfig/cron` **14,2k**, `go-co-op/gocron` 7,2k, `reugn/go-quartz` 2,0k ; SF
`Scheduler`. Parseur d'expressions cron + roue de timers + port `Job`. Se
compose sur `kernel/clock` → **testable sans dormir**, ce que presque aucune
bibliothèque Go ne permet. Différenciant.

### B8. `queue` et B9. `events`

`asynq` 13,7k, `river` 5,6k, `watermill` 9,9k, `machinery` 8,0k ; SF `Messenger`
et `EventDispatcher`. Deux domaines distincts : `events` = bus in-process typé
(listeners priorisés, arrêt de propagation) sur `kernel/topic` ; `queue` = ports
`Broker`/`Handler`/`Middleware`, impls mémoire et fichier en service, Redis/PG
en `third-party/`.

### B10. `vfs`

`spf13/afero` **6,7k** ; SF `Filesystem` + `Finder`. `io/fs` couvre la lecture
depuis Go 1.16 ; manquent l'écriture, le walk/glob, l'écriture atomique
(`rename(2)`), les répertoires temporaires. Impls `os` et `mem` ; S3 en
`third-party/` — la lane AWS existe déjà.

### B11. `lock`

SF `Lock` ; `redislock` 1,8k. Port `Locker` + TTL ; in-proc et `flock`.
Distribué en `third-party/`. Fort contenu ADR 0018 : `flock` diverge nettement
entre Linux, BSD et Windows.

### B12. `trace`

`opentelemetry-go` 6,5k, Jaeger 23,2k. Le SDK a metrics et logs — il manque le
troisième pilier. Propagation **W3C traceparent** et port `Span` sont
stdlib-only ; l'export OTLP part en `third-party/`. Gain immédiat : corrélation
`trace_id` dans chaque record du logger.

### B13. `health`

Pas de bibliothèque dominante, mais besoin universel (probes k8s). Port
`Checker`, agrégation, handler HTTP. Très peu cher, se branche sur `lifecycle`.

### B14. `sql` — **des ports, pas un ORM**

`golang-migrate` **18,9k**, `sqlx` 17,7k, `sqlc` 18,3k. Symfony ne livre pas
Doctrine : elle l'intègre par un bridge. Même discipline ici. Le SDK livre :
ports au-dessus de `database/sql`, **gestionnaire de transactions** (imbriquées
via savepoints), **runner de migrations**, health check, politique de pool.
Les drivers et tout ORM restent dehors. C'est le plus gros manque fonctionnel
du SDK aujourd'hui.

### B15. `mapper` (normalisation objet)

SF `Serializer` + `ObjectMapper` + `PropertyAccess`. `codec` s'arrête aux
octets ; il manque struct ↔ map, groupes de sérialisation, renommage, champs
ignorés. Indispensable dès qu'une API expose des DTO versionnés.

### B16. `i18n`

`nicksnyder/go-i18n` 3,5k ; SF `Translation` + `Intl`. Catalogue de messages et
règles de pluriel faisables stdlib ; les données CLDR complètes sont lourdes →
`third-party/`.

### B17. `mail`

SF `Mailer` + `Mime` ; `nikoksr/notify` 3,8k. Constructeur MIME + port
`Transport` + SMTP (`net/smtp`, `net/mail`, `mime/multipart` sont tous stdlib).
SES/Mailgun/Postmark en `third-party/`.

### B18. `view`

SF `Twig` ; `pongo2` 3,1k, `quicktemplate` 3,3k, `jet` 1,4k. Port `Renderer` +
registre, `html/template` en implémentation par défaut. Surface volontairement
mince — le moteur de template est une opinion.

### B19. `cli`

`spf13/cobra` **44,6k**, `urfave/cli` 24,2k ; SF `Console`. Bâti sur le `flag`
stdlib. **Avertissement** : c'est la plus grosse surface d'API de toute la
liste, et le framework en fera son `bin/console`. À traiter tard, ou à laisser
au framework si la volumétrie effraie.

## 6. Tier C — extensions de domaines existants (le meilleur rapport effort/gain)

| Domaine | Manque constaté | Note |
|---|---|---|
| **`transform`** | **seulement gzip + flate.** Ajouter zlib (stdlib) ; zstd, brotli, lz4, snappy, s2, xz en `third-party/` | bloque `Content-Encoding` HTTP |
| **`codec`** | **`form-urlencoded` et `multipart/form-data`** — indispensables au web, et tous deux stdlib. Puis INI, `.properties`, JSON5, plist, RESP. Avro/Parquet/Arrow/Cap'n Proto restent différés (ADR 0023) | le duo form/multipart est le manque le plus criant |
| **`metrics`** | **labels/dimensions** (différé ADR 0027) — sans eux, pas d'exporter Prometheus crédible. Exporter Prometheus texte = stdlib. OTLP en `third-party/` | débloque toute l'observabilité |
| **`config`** | schéma + défauts + validation (via B2), résolution de secrets, sources distantes (etcd/consul → third-party), watcher natif fsnotify en complément du poll | SF `Config` TreeBuilder est le modèle |
| **`resilience`** | fallback, hedging, concurrence adaptative (AIMD), propagation d'échéance | `failsafe-go` 2,2k comme repère |
| **`net`** | **WebSocket (RFC 6455) et SSE** — `gorilla/websocket` 24,9k, implémentable stdlib ; puis résolveur + load-balancer (cf. `gf/gsvc`+`gsel`, `kratos/registry`+`selector`). QUIC/HTTP3 → third-party | WS/SSE = attente forte pour un framework moderne |
| **`crypto`** | JWK/JWKS, helpers X.509 (génération, rotation), HPKE, PASETO | alimente B5 |
| **`logger`** | corrélation trace/span, échantillonnage adaptatif, sink OTLP | dépend de B12 |
| **`id`** | NanoID, KSUID, TypeID (préfixé, façon Stripe) | très bon marché |
| **`proc`** | politiques de redémarrage d'arbre de process | recoupe B1 — arbitrer |

## 7. Tier D — `third-party/` (dépendances lourdes, en quarantaine)

zstd/brotli/lz4/snappy · Redis (cache, lock, queue, ratelimit distribué) ·
drivers Postgres/MySQL/SQLite · OTLP + client Prometheus · CLDR/ICU · gRPC ·
GraphQL · vfs S3 · Vault / 1Password · fsnotify · QUIC/HTTP3 · sanitizer HTML
(`x/net/html`).

**Filtre obligatoire, précédent ADR 0022 corrigé par ADR 0034** : `hcl/v2`
(via `x/tools`) **introduirait** `x/sys` — banni — dans `internal/service` ;
il ne rétrograde rien, MVS ne sélectionnant jamais une version inférieure
(§12.2 B). La quarantaine tient pour cette raison et parce qu'elle n'impose
pas un graphe aux consommateurs qui ne s'en servent pas. Mesurer l'impact sur
le graphe de dépendances **avant** de décider du placement, jamais après.

## 8. Tier E — framework, explicitement PAS le SDK

- **Conteneur DI avec autowiring par réflexion** (`uber-go/fx` 7,7k, `dig` 4,5k,
  `wire` 14,4k). Le SDK fournit `lifecycle` (B1) et, si besoin, un registre
  typé ; **l'autowiring est une opinion**, et la réflexion contredit l'éthos
  1-alloc du SDK. `wire` (génération à la compilation) est le modèle compatible.
- **Routage par attributs/annotations**, conventions de contrôleurs.
- **Forms** (SF `Form`) — trop couplé au rendu.
- **Scaffolding / generators** (MakerBundle), fixtures, seeders.
- **Structure de projet, bundles/modules, conventions de nommage.**
- **Profiler / debug toolbar**, AssetMapper, pipeline d'assets.
- **ORM complet** (entités, mapping, lazy loading) — cf. B14.
- **Un `Request`/`Response` façon HttpFoundation.** Piège spécifique à Go :
  `net/http.Request` existe et tout l'écosystème s'y branche. Un type concurrent
  isolerait le framework. Rester sur `net/http`, enrichir par le contexte.

## 9. Plan d'exécution — 40 tâches, T01→T50

Miroir des notes Kepler de la tâche (une note par tâche, plus un plan maître).
Ouvrir 18 domaines d'un coup triplerait la taille du SDK — d'où le découpage.

### La parallélisation est logique, pas physique
`CLAUDE.md` global : « Rien de parallèle sur cette VM. Un dépôt, un job, à la
fois. » Deux `bazel build` concurrents partent en OOM sans avertissement. Le
plan achète donc des **branches indépendantes, mergeables dans n'importe quel
ordre sans conflit** — pas du CPU concurrent. Les builds restent sérialisés.

### Les 7 points de sérialisation
Sans neutralisation préalable, 4 branches parallèles produisent 4 conflits dans
les 3 mêmes fichiers : les slots `PP` (collision **silencieuse**, vue seulement
au merge par l'audit AST) · la table de `internal/core/CLAUDE.md` · les listes
du `CLAUDE.md` racine · `registry_external_test.go` ·
`tools/alloc-lane-targets.txt` · `MODULE.bazel`/`go.mod` · la numérotation des
ADR. **T02 les pré-alloue tous ; rien ne part en parallèle avant son merge.**

### V0 — séquentielle, bloque tout
| # | Tâche |
|---|---|
| T01 | Intendance : `.claude/contexts/` tracké ou déplacé, lien ADR 0013 réparé, ADR 0030/0031 ajoutés à la Reference racine |
| T02 | **ADR 0034 — pré-allocation** des slots core/service + des numéros d'ADR (précédent : ADR 0006) |
| T03 | Convention anti-collision sur les tables partagées (ordre par slot, une branche = sa ligne) |

### V1 — 4 pistes disjointes
| Piste | Tâches |
|---|---|
| A — cache | T10 `kernel/singleflight` → T11 `cache` domaine (tags, anti-stampede) |
| B — lifecycle | T12 `kernel/group` → **T13 `lifecycle`** |
| C — validation | T14 `validation` |
| D — formats | T15 `transform`:zlib · T16 codec form-urlencoded · T17 codec multipart |

### V2 — 4 pistes (ne dépend que de V0 ; ordre libre vis-à-vis de V1)
| Piste | Tâches |
|---|---|
| A — auth | T20 crypto JWK/JWKS → T21 `token` → T22 `session` |
| B — net temps réel | T23 WebSocket → T24 SSE |
| C — temps | T25 `kernel/heap` → T26 `scheduler` |
| D — metrics | **T27 labels** → T28 exporter Prometheus |

### V3 — 4 pistes
| Piste | Tâches |
|---|---|
| A — messagerie | T30 `kernel/topic` → T31 `events` → T32 `queue` |
| B — trace | T33 `trace` → T34 corrélation logger |
| C — données | T35 `sql` (ports + tx) → T36 migrations |
| D — système | T37 `vfs` · T38 `lock` |

### V4 — largement indépendantes
T40 `authz` · T41 `view` · T42 `mapper` · T43 `health` · T44 `config` schéma ·
T45 `i18n` · T46 `mail` · T47 `cli` · T48 `id` NanoID/KSUID/TypeID ·
T49 `resilience` fallback/hedging · T50 third-party zstd/brotli/lz4.

### Trois arbitrages à trancher avant d'ouvrir la tâche concernée
- **T47 `cli`** — plus grosse surface d'API du lot, et aucun gain d'intégration
  avec le reste du SDK. Le laisser au framework (qui prendra `cobra`) est
  défendable. Décider avant, pas pendant.
- **T36 vs T38** — le verrou de migration passe-t-il par `lock`, ou par le
  verrouillage consultatif natif du SGBD ? La seconde évite un couplage.
- **T30** — n'entre en kernel que si le second consommateur (fan-out
  logger/metrics) se matérialise ; sinon il fusionne dans T31 (précédent ADR 0010).

### Worktrees
À créer après le merge de T02, un par piste. Les créer avant obligerait à tout
rebaser dès que T02 touche le registre.

## 10. Garde-fous à ne pas perdre de vue

- **Règle 1 (kernel gate)** : `session`, `token`, `mail`, `authz` portent du
  vocabulaire métier — ils n'ont rien à faire dans kernel, jamais.
- **Précédent ADR 0010** : ≥ 2 consommateurs concrets avant d'admettre une
  primitive kernel. Le tier A est une réserve, pas un backlog.
- **Règle 5** : un domaine qui ne livre qu'un port et zéro implémentation est un
  stub et ne se merge pas.
- **Règle 11** : la doc voyage dans le même commit — `CLAUDE.md` du package, les
  tables du parent, la liste des domaines à la racine, et le commentaire de
  package que `gomarkdoc` rend (règle 10).
- **Règle 12** : toute exclusion de test (`!race`, `manual`, linter) exige sa
  lane nommée et verte dans le même commit.
- **ADR 0018** : 8 GOOS. `lock` (flock), `vfs` (permissions, liens), `scheduler`
  (timers), `queue` (fichiers) divergent tous selon l'OS.
- **ADR 0030** : aucun défaut n'écrit sur stdout. S'applique à `cli`, `view`,
  `health`.
- **ADR 0031** : pas de politique inerte. Un `scheduler` sans job est légitime ;
  une expression cron invalide doit refuser, pas se taire. Idem `validation`
  avec zéro contrainte.
- **ADR 0022** : vérifier l'impact sur `x/sys` avant tout placement de dépendance.

## 11. Point d'intendance

**Tranché : déplacé sous `docs/`, et `.claude/contexts/` reste ignoré.**

La question posait deux options — suivre `.claude/contexts/` par une exception
`.gitignore`, ou déplacer ce document sous `docs/`. La première a d'abord été
retenue (`!/.claude/contexts/`), puis abandonnée pour une raison qui n'existait
pas quand la question a été écrite : le gate `post-commit` du dépôt refuse tout
fichier suivi sous un répertoire `.claude/` comme « artefact d'agent ». La PR
#143 a borné son exemption à trois chemins exacts, ceux que le `Dockerfile` du
devcontainer copie réellement, et a documenté pourquoi l'élargir aveuglerait le
gate sur des fichiers d'agent ajoutés ailleurs.

Un document qu'un ADR cite est de la documentation, et la documentation vit sous
`docs/`. Le lien de l'ADR 0013 pointe désormais ici et ne peut plus disparaître
d'un clone. Ne pas rouvrir l'exception : un fichier suivi sous `.claude/contexts/`
fait échouer `post-commit`, et `post-commit` est un check requis par ruleset.

---

# 12. REVUE ADVERSE (codex, 3 tours, 2026-09-09) — ce document est déclassé

**Statut : les §4 à §9 ne sont plus un backlog. Ils deviennent un catalogue de
référence** — ce que l'écosystème offre, ce qui a été considéré, pourquoi
l'essentiel a été écarté. La §2 (frontière SDK/framework) survit, amendée.

Relecteur : `codex` 0.153.3 (session `01a086a2-5c13-75f3-92f9-35768411d559`),
mandat explicitement adverse, 3 tours.

## 12.1 Affirmations de ce document réfutées, vérification à l'appui

| Affirmation d'origine | Verdict | Preuve |
|---|---|---|
| « `scheduler` testable sans dormir via `kernel/clock` » | **FAUX** | `internal/kernel/clock/clock.go:9-13` — `Clock` n'expose que `Now()` et `Since()`. Ni `After`, ni `Timer`, ni `Sleep` à injecter. `testing/synctest` couvre déjà ce besoin. |
| « `codec` s'arrête aux octets, pas objet » | **FAUX** | `internal/core/codec/codec_interface.go:19-20` — le contrat est `Marshal(v any)` / `Unmarshal(data []byte, v any)`. Retire au `mapper` sa justification. |
| « `wire` est le modèle compatible » | **FAUX** | `google/wire` est **archivé** (dernier push 2025-08-22). |
| « `lifecycle` est le pendant de `HttpKernel` » | **FAUX** | HttpKernel transforme une requête en réponse ; il ne supervise pas un processus. |
| « port + registre » comme critère d'admission (§2 Q5) | **FAUX** | `proc`, `resilience` et `net` n'ont pas de registre — `internal/core/CLAUDE.md`. |
| « la réflexion contredit l'éthos 1-alloc » (§8) | **FAUX** | Confond réflexion au démarrage et allocation par émission. |
| « un port sans implémentation viole la règle 5 » | **SUR-ÉTENDU** | La règle 5 vise les fichiers et répertoires placeholders. |
| « 18 domaines » (§9) | **INCOHÉRENT** | Le Tier B en liste 19. |
| « collision de slot détectée au merge par l'audit AST » | **FAUX** | Voir §12.2 — l'audit ne voit pas ça du tout. |

Le compte de 40 tâches, lui, est **exact** (codex l'avait contesté à tort).

## 12.2 Deux découvertes indépendantes de toute feuille de route

**A. Rien n'enforce la propriété des plages `PP`.**
`TestAuditCodeUniqueness` (`internal/kernel/errs/registry_external_test.go:376`)
clé sur la **valeur numérique résolue**. Deux packages partageant le même
`MM.LL.PP` avec des `SS` différents passent donc au vert, alors qu'ADR 0005
promet l'exclusivité des plages. La table du registre est déclarative et non
vérifiée. C'est une **lacune de vérification**, pas un incident constaté :
l'inventaire dira s'il existe aussi un défaut de données.

**B. Le récit causal d'ADR 0022 n'est pas reproductible.**
L'ADR justifie la quarantaine de HCL par « `hcl/v2`+`go-cty` rétrogradent
`x/sys` dans `internal/service` ». Expérience exécutée (Go 1.27.1, module neuf,
`GOWORK=off`) :

```
exigence x/sys@v0.33.0            → sélection v0.33.0
+ hcl/v2@v2.23.0 (version du commit ce5c0a1, qui déclare x/sys v0.5.0 indirect)
                                  → sélection v0.33.0, INCHANGÉE
```

**MVS sélectionne le maximum** : ajouter une dépendance réclamant une version
inférieure ne rétrograde rien. L'historique le confirme —
`internal/service/go.mod` ne déclarait pas `x/sys` avant `ce5c0a1`, ne le
déclare pas après, et le commit ajoute HCL au **module racine**.

La justification d'isolation opt-in tient (ne pas imposer un graphe aux
consommateurs qui ne l'utilisent pas). Le **mécanisme** allégué est faux, et ce
document le citait comme règle de placement dans quatre fiches.

## 12.3 Les trois erreurs de fond du plan d'origine

1. **Prendre un catalogue de composants populaires pour un backlog nécessaire.**
   Les étoiles GitHub mesurent la visibilité, pas la demande des consommateurs
   de ce SDK. Une bibliothèque populaire est souvent une raison de **l'intégrer**,
   pas de la réimplémenter.
2. **Confondre « stdlib, ports et registres » avec « simple et interchangeable »** —
   en particulier pour `token`, `queue`, `lock`, `sql`, `multipart` et le tracing.
3. **Faire de la préallocation spéculative et des branches parallèles le
   préalable au travail**, alors qu'elles ne suppriment ni les dépendances
   sémantiques ni le poste de validation unique.

## 12.4 Ce qui remplace la feuille de route

**Corrections, indépendantes et non bloquantes** — elles ne conditionnent pas le
spike, sinon l'intendance remplace encore l'épreuve d'usage :
- **C1** Amender ADR 0022 : conserver l'isolation opt-in, qualifier le récit de
  downgrade de **non reproduit**, joindre l'expérience.
- **C2** Fermer la lacune §12.2-A : exclusivité du préfixe `code & 0xFFFFFF00`
  par package + table `préfixe → propriétaire` **indépendante des constantes
  auditées**. Inventaire d'abord ; aucune renumérotation de code publié.
- **C3** Corriger les affirmations de §12.1 ici et dans les notes ; ajouter
  ADR 0030/0031 à la liste racine ; trancher `.claude/contexts/`.

**Le spike, qui remplace les vagues 1 à 4**
- **S1** Module consommateur **hors workspace**, important les versions publiées
  sans `replace` local. S'il ne construit même pas, c'est le premier défaut réel.
- **S2** Un service HTTP de réservation, stockage mémoire, exerçant sept
  parcours : création JSON · conflit de créneau · adresse et limites
  configurables · journalisation avec identifiant de requête · comptage
  succès/refus (compteurs non labellisés) · arrêt pendant une requête en cours ·
  adresse déjà occupée au démarrage.
- **S3** Journal de friction : code appelant, comportement attendu,
  contournement actuel. Pas de liste de composants supposés manquants.

**Condition d'arrêt du spike** : les sept parcours passent depuis le module
extérieur, avec démarrage et arrêt du vrai processus. **Ne pas ajouter de
fonctionnalités jusqu'à obtenir les frictions espérées.**

**Critère d'extraction vers le SDK** (Phase 2, pilotée par S3 seule) :
problème démontré par un parcours ou un échec reproductible · solution
formulable sans le vocabulaire du framework · apport au-delà d'un appel direct à
la stdlib ou à une bibliothèque · garanties explicites et testables · seconde
utilisation indépendante confirmant la forme commune, **ou** défaut avéré d'un
contrat SDK existant.

**Si le spike ne révèle aucune friction** : cela signifie « aucune extension SDK
justifiée par cet usage » — ni « SDK fini », ni « spike trop petit ». Le signal
discriminant est la représentativité des exigences, **établie avant**
l'expérience. Chercher après coup un spike plus gros serait un prétexte pour
rouvrir le catalogue.

## 12.5 Amendement à la §2 (frontière SDK/framework)

- Le critère Q5 « port + registre » est **retiré** (contredit par le dépôt).
- Le critère Q3 « faisable stdlib-only » est une **contrainte technique, pas une
  justification produit** ; seule la contrainte kernel est absolue.
- Critère utile en remplacement : **quel contrat stable et réutilisable justifie
  que le SDK en assume la maintenance ?**
- Un `Request`/`Response` concurrent de `net/http` est à écarter **aussi du
  framework**, pas seulement du SDK.
- Autowiring et ORM sont des **possibilités d'intégration**, pas des composants
  promis au framework.
