# Revue de `docs/plans/2026-08-29-collector.md`

Date de la revue: 2026-08-29
Document examiné: `docs/plans/2026-08-29-collector.md` (4349 lignes)

## Résumé

Le plan est exceptionnellement détaillé et prêt à être exécuté. Chaque tâche suit une discipline TDD claire (test qui échoue, implémentation, vérification, commit), les interfaces sont bien séparées, et les contraintes du spec sont rappelées explicitement. La couverture du spec est largement complète pour ce qui est dans le scope du plan.

Cette revue relève néanmoins quatre problèmes concrets qui casseraient ou fausseraient le comportement, quelques incohérences avec `docs/SPECS.md`, et plusieurs points de vigilance avant de passer à l'exécution.

## Verdict global

Le plan est approuvé avec modifications recommandées. Les bugs signalés doivent être corrigés avant ou pendant l'implémentation; les incohérences avec le spec doivent être explicitement justifiées ou corrigées. Hormis cela, la découpe des tâches et les tests proposés permettent une exécution fiable.

## Points solides

- Discipline TDD appliquée systématiquement: test rouge, implémentation, test vert, commit, vérification des contraintes.
- Découpage en packages cohérents (`config`, `dotenv`, `model`, `window`, `source`, `collector`, `web`) avec des responsabilités uniques.
- Tests unitaires ciblés sur les parties où une erreur serait silencieuse: arithmétique des compteurs, aplatissement des blocking chains, éviction de la fenêtre, throttle.
- Tests d'intégration contre un vrai SQL Server en Podman, avec `t.Skip` quand `SQLTOP_TEST_DSN` est absent.
- Respect des contraintes fortes: pas de CGO, binaire statique, lecture seule sur le serveur, secrets via `.env`, pas de mock de base de données.
- Correction de l'ambiguïté du budget: le plan mesure le coût en CPU serveur via `cpu_time` de la propre session, pas le round-trip réseau. C'est plus fidèle au spec section 10 qu'une lecture littérale.
- Sécurité du serveur HTTP bien pensée: bind `127.0.0.1` uniquement, token par run, comparaison en temps constant.
- Protocole optimisé par la table de référence par session, conforme au gain de 47 % mesuré dans le bench.
- `Kill` est délibérément absent du scope, avec une justification claire liée à la sécurité et au flux UI futur.

## Bugs et risques concrets

### 1. `web.Encoder.fingerprint` peut mélanger deux requêtes différentes

Dans `internal/web/protocol.go` (task 12), quand `QueryHash` est vide, le fallback est `strconv.Itoa(len(r.SQLText))`. Deux sessions avec des textes SQL différents mais de même longueur obtiennent le même fingerprint, donc la même `refKey`. La seconde session réutilisera la référence de la première: le grid affichera le mauvais SQL text.

Correction suggérée: utiliser un hash du texte SQL (par exemple `fmt.Sprintf("%x", sha256.Sum256([]byte(r.SQLText)))[:16]`) ou utiliser le texte complet comme clé si la taille du payload n'est pas critique.

### 2. La requête d'historique CPU risque de ne rien retourner à cause du namespace XML

Dans `internal/source/mssql/server.go` (task 9), `cpuHistoryQuery` utilise `record.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int')`. Le XML du ring buffer `RING_BUFFER_SCHEDULER_MONITOR` contient un namespace par défaut. Une requête sans prise en compte du namespace retourne généralement `NULL`. Cela fera passer le test `TestUnavailableFigureIsMarkedNotOmitted` car la figure sera `Available: false`, mais masquera un dysfonctionnement réel sur le terrain.

Vérification recommandée: exécuter cette requête telle quelle sur le container SQL Server 2022 avant de valider l'implémentation. Si nécessaire, utiliser `WITH XMLNAMESPACES` ou la syntaxe `/*[local-name()=...]`.

### 3. La détection de `CapInstanceWideView` peut échouer à tort

Dans `internal/source/mssql/mssql.go` (task 7), la capability `CapInstanceWideView` est activée uniquement si `COUNT(*) FROM sys.dm_exec_sessions WHERE session_id <> @@SPID` retourne plus que zéro. Sur un serveur sans autre session active au moment du probe, la capability ne sera pas activée même si l'utilisateur a `VIEW SERVER STATE`. Le grid affichera alors une vue tronquée.

Correction suggérée: utiliser `HAS_PERMS_BY_NAME` sur la vue, ou tester l'accès à la vue sans dépendre de la présence d'autres sessions.

### 4. Le stream SSE est hardcodé à une seconde

Dans `internal/web/stream.go` (task 14), le ticker du stream est `time.NewTicker(time.Second)`. Le spec section 10 rend la tier requests configurable (`"requests": "1s"` par défaut). Si l'utilisateur change cette valeur, le stream continuera à pousser à 1 Hz indépendamment.

Correction suggérée: passer la période `cfg.Tiers.Requests` au serveur ou au stream handler.

### 5. La fenêtre utilise son propre timestamp au lieu de celui de l'échantillon

Dans `internal/collector/collector.go` (task 11), `c.win.Append(time.Now(), window.Flatten(rows))` utilise le temps local du collector. Le `At` de chaque `RequestSample` a été positionné dans `SampleRequests` par la source. Ces deux horodatages peuvent diverger, en particulier sous charge ou après un ralentissement. Cela perturbe l'éviction par âge et l'affichage de l'historique.

Correction suggérée: utiliser le `At` des samples (en supposant qu'ils partagent tous le même `At` pour un tick) ou passer un timestamp explicite cohérent avec la source.

### 6. Redondance entre `version_store_kb` du compteur et `version_store_mb` de la DMV

Task 5 définit un compteur `version_store_kb` dans `counterDefs` (object `Transactions`, counter `Version Store Size (KB)`). Task 9 lit aussi `version_store_mb` depuis `sys.dm_tran_version_store_space_usage`. Le spec section 6 mentionne explicitement `sys.dm_tran_version_store_space_usage` comme source. Avoir deux sources pour la même métrique sans distinction claire est confus.

Suggestion: supprimer `version_store_kb` du catalogue des compteurs, ou expliquer la différence sémantique entre les deux.

## Questions ouvertes et ambiguïtés

| Task | Point | Pourquoi cela importe |
|---|---|---|
| 1 | `config.Save` est défini mais jamais testé ni utilisé dans ce plan. | Acceptable si l'UI plan s'en charge, mais le plan devrait le noter explicitement pour éviter qu'un agent ne l'implémente comme du code mort. |
| 1 | `Duration.Minutes()` est ajouté uniquement pour le test `TestLoadPartialFileKeepsDefaults`. | C'est une méthode utilitaire qui n'a pas de raison métier. Elle n'est pas nuisible, mais elle indique que le test aurait pu utiliser `time.Duration(got.Retention).Minutes()`. |
| 2 | `model.RequestSample` contient `IsolationLevel` et `QueryHash`, mais la requête de task 8 ne remplit pas `IsolationLevel`. | Le spec section 8.1 liste `isolation_level` comme colonne. Soit elle est volontairement différée, soit il manque un champ dans `requestsQuery`. |
| 7 / 9 | `CapSchedulerLoad` est probe, mais aucune figure de scheduler load n'est produite par `SampleServer`. | Le spec section 6 liste "Scheduler load" comme figure du dashboard. Elle n'apparaît pas dans le plan. |
| 9 | Les figures de memory clerks et cache counters du spec section 6 ne sont pas implémentées. | Le plan se limite à `total_server_memory_mb` et `target_server_memory_mb`. C'est un scope partiel acceptable, mais il doit être listé dans les écarts. |
| 14 | Le plan cite "spec 9.1" pour justifier l'absence de `Kill`. | `docs/SPECS.md` n'a pas de section 9.1. C'est soit une référence à une version interne du spec, soit une erreur de numérotation. |

## Incohérences mineures

- Task 1: la résolution de configuration utilise des variables d'environnement `SQLTOP_BINARY_DIR` et `SQLTOP_USER_CONFIG_DIR` comme seams de test. Ces variables ne sont pas documentées dans `cmd/sqltop/main.go` ni dans `.env.example`. Ce n'est pas un bug, mais cela mérite un commentaire.
- Task 3: le test `TestEvictsByCountAndReportsCapped` vérifie `samples <= 10`, pas `samples == 10`. Avec 25 ticks d'un sample, on s'attend à exactement 10; un test plus strict renforcerait la confiance.
- Task 4: `Flatten` trie les enfants par `session_id`. C'est stable mais arbitraire; le spec ne précise pas l'ordre entre frères d'une chaîne de blocage.
- Task 5: `counterState.apply` stocke dans `s.prev` toutes les clés reçues, y compris les counters base. C'est correct, mais cela signifie que les compteurs base sont conservés d'un tick à l'autre même s'ils disparaissent temporairement de la vue.
- Task 10: le message de recovery indique "space tier slowed to half rate" au level 1, mais `Period` multiplie par 2 la période, ce qui divise la fréquence par deux. Le message est donc correct.
- Task 11: `costLoop` ignore silencieusement une erreur de `Cost`. Si la connexion est perdue, le budget ne se mettra pas à jour et le throttle ne réagira pas.
- Task 12: le test `TestReferenceTableCutsThePayloadSubstantially` attend une réduction d'au moins 40 %. Le bench annonçait 47 %. Le seuil de 40 % est prudent mais mérite d'être re-mesuré avec les vraies données.
- Task 14: le JS ne gère pas explicitement la fermeture propre de l'`EventSource`. Si le serveur s'arrête proprement, le navigateur affichera le message d'erreur et tentera de se reconnecter, ce qui est acceptable mais bruyant.

## Risques et points de vigilance

- `mssql.Source` utilise `db.SetMaxOpenConns(1)` pour garantir que `@@SPID` et `cpu_time` correspondent à la bonne session. C'est correct, mais cela interdit toute parallélisation future des tiers sur une seule source. Le plan ne prévoit pas cette évolution, donc ce n'est pas un problème ici.
- Le `window.Window` réalloue `w.ticks` à chaque éviction via `append([]tick(nil), w.ticks[drop:]...)`. Sur un serveur très chargé avec des échantillons fréquents, cela pourrait devenir coûteux. Le principe KISS justifie le choix, mais une mesure future serait utile.
- Le renderer du bench est copié dans `internal/web/assets/`. Le plan précise que `bench/` reste intact, ce qui est bien. Cependant, toute correction de bug dans le renderer devra être appliquée aux deux copies jusqu'à ce qu'on décide de les fusionner.
- La stratégie d'authentification par token dans l'URL est raisonnable pour un usage local, mais l'URL peut apparaître dans l'historique du navigateur ou dans les logs du terminal. Ce n'est pas critique car le serveur est en loopback, mais c'est un point à surveiller.
- Le plan ne traite pas la reconnexion SQL Server en cas de perte de connexion. Le spec section 4.4 mentionne "reconnection with backoff" comme deferred; le plan confirme qu'elle est deferred, ce qui est cohérent.
- `model.RequestSample.SQLText` est inclus dans la référence par session. Si une session exécute une requête très longue, la référence devient elle-même lourde. Le plan ne prévoit pas de troncature ou de limite de taille.

## Suggestions concrètes

1. Corriger `fingerprint` pour éviter les collisions sur la longueur du texte SQL.
2. Vérifier et corriger `cpuHistoryQuery` contre un vrai SQL Server 2022, en particulier le namespace XML.
3. Remplacer la détection de `CapInstanceWideView` par un test de permission plutôt qu'un comptage de sessions.
4. Paramétrer la période du stream SSE par `cfg.Tiers.Requests`.
5. Uniformiser le timestamp passé à `window.Append` avec celui des `RequestSample`.
6. Retirer `version_store_kb` du catalogue des compteurs ou documenter la différence avec `version_store_mb`.
7. Ajouter un commentaire dans `config.go` expliquant `SQLTOP_BINARY_DIR` et `SQLTOP_USER_CONFIG_DIR` comme seams de test.
8. Documenter explicitement dans le plan les figures du dashboard qui sont partielles ou absentes (memory clerks, scheduler load, isolation level).
9. Corriger la référence "spec 9.1" en "spec section 9" ou ajouter la section manquante au spec.
10. Ajouter un test pour `config.Save`, même minimal, ou indiquer clairement qu'il est hors scope du collector et appartient au UI plan.

## Vérifications recommandées avant exécution

1. Exécuter `cpuHistoryQuery` manuellement sur le container SQL Server 2022 pour confirmer qu'elle retourne des valeurs non nulles.
2. Tester le scénario de deux sessions avec des SQL texts différents mais de même longueur pour vérifier la correction de `fingerprint`.
3. Vérifier le comportement de `probe` sur un container frais sans autre connexion que celle de sqltop.
4. Mesurer le vrai gain de payload du protocole optimisé sur des données réelles.
5. Confirmer que le renderer tient sous 16 ms avec le lookup de référence ajouté.

## Conclusion

`docs/plans/2026-08-29-collector.md` est un plan de haute qualité, traçable et exécutable. Les problèmes trouvés sont corrigibles sans remettre en cause l'architecture. Les priorités avant de lancer l'exécution sont:

1. Corriger le bug de collision dans la clé de référence du protocole.
2. Valider la requête d'historique CPU contre un vrai moteur.
3. Corriger la détection de `CapInstanceWideView`.
4. Paramétrer la période du stream SSE.

Une fois ces points traités, le plan peut être confié à un agent d'exécution, en mode subagent-driven ou inline, sans risque majeur.
