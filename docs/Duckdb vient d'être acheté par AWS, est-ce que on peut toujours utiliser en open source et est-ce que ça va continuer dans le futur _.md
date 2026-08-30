> From: https://www.perplexity.ai/search/2288d523-14fa-4428-9bf2-6009e7d8e587

# you asked

Duckdb vient d'être acheté par AWS, est-ce que on peut toujours utiliser en open source et est-ce que ça va continuer dans le futur ?

---

# perplexity response

Oui. AWS rachète **DuckLabs**, la société et l’équipe derrière DuckDB, mais **pas le projet open source DuckDB lui-même**. DuckDB reste gratuit, open source sous licence **MIT**, et gouverné par la fondation indépendante à but non lucratif **DuckDB Foundation**.[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)[[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs)

## Ce qui reste inchangé

- Vous pouvez continuer à installer DuckDB localement, l’embarquer dans Python, R, .NET, Java ou une application C/C++, et l’utiliser sans compte AWS ni dépendance à un service AWS.[[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs)
- La licence MIT autorise l’usage commercial, la modification, la redistribution et le fork, avec très peu de contraintes.[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)
- Les projets de l’écosystème annoncés — notamment DuckLake et Quack — doivent eux aussi rester open source sous MIT, sous la responsabilité de la fondation.[[2]](https://siliconangle.com/2026/08/26/aws-buys-ducklabs-to-bring-duckdbs-embeddable-analytics-to-more-enterprises/)

## Pourquoi c’est une garantie assez solide

Le point juridique le plus fort est que les versions déjà publiées sous MIT le restent irrévocablement : AWS ne peut pas rendre propriétaire le code que vous utilisez aujourd’hui, ni retirer le droit de le redistribuer ou de le forker.[[3]](https://layerbase.com/blog/aws-ducklabs-duckdb-governance)

Le point institutionnel est également favorable : AWS affirme explicitement ne pas acquérir le projet open source, et la gouvernance doit rester portée par la DuckDB Foundation plutôt que par DuckLabs/AWS.[[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs)

## Ce qui peut tout de même évoluer

L’incertitude porte moins sur l’accès au code que sur la **trajectoire du projet** :

| Sujet | Risque / évolution possible |
|---|---|
| Priorités de développement | L’équipe pourrait investir davantage dans les cas d’usage AWS, S3, Iceberg, lakehouse et intégrations cloud. [[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/) |
| Répartition des contributions | Si la majorité des mainteneurs devient salariée AWS, AWS pourra influencer de fait la roadmap, même si la fondation demeure indépendante. [[4]](https://www.theregister.com/databases/2026/08/26/aws-buys-ducklabs-the-people-behind-the-popular-in-process-olap-database/5292590) |
| Produits autour de DuckDB | AWS peut construire des offres managées, connecteurs ou fonctions propriétaires autour de DuckDB ; cela ne rend pas le cœur de DuckDB propriétaire. [[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs) |
| Hypothèse défavorable | Si la gouvernance ou la roadmap se détérioraient, le projet peut être forké grâce à MIT. [[3]](https://layerbase.com/blog/aws-ducklabs-duckdb-governance) |

## Lecture pratique

Pour un usage technique — par exemple DuckDB intégré à des scripts d’analyse, à un pipeline Parquet, à PostgreSQL/SQL Server via export, ou à un outil .NET — il n’y a pas de raison immédiate de changer de stratégie. Le risque de verrouillage est très faible pour le **moteur local** : un binaire et du code MIT restent utilisables indépendamment d’AWS.[[3]](https://layerbase.com/blog/aws-ducklabs-duckdb-governance)[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)

En revanche, si vous construisez un produit qui dépend fortement de la roadmap DuckDB, surveillez dans les prochains mois : la composition effective de la fondation, les droits de décision sur les releases, la politique de contribution, et la part des nouveautés strictement orientées AWS. Aujourd’hui, l’annonce est plutôt rassurante : les fondateurs restent à la tête de la direction technique et AWS s’engage à poursuivre le développement open source.[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)

Oui. AWS rachète **DuckLabs**, la société et l’équipe derrière DuckDB, mais **pas le projet open source DuckDB lui-même**. DuckDB reste gratuit, open source sous licence **MIT**, et gouverné par la fondation indépendante à but non lucratif **DuckDB Foundation**.[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)[[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs)

## Ce qui reste inchangé

- Vous pouvez continuer à installer DuckDB localement, l’embarquer dans Python, R, .NET, Java ou une application C/C++, et l’utiliser sans compte AWS ni dépendance à un service AWS.[[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs)
- La licence MIT autorise l’usage commercial, la modification, la redistribution et le fork, avec très peu de contraintes.[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)
- Les projets de l’écosystème annoncés — notamment DuckLake et Quack — doivent eux aussi rester open source sous MIT, sous la responsabilité de la fondation.[[2]](https://siliconangle.com/2026/08/26/aws-buys-ducklabs-to-bring-duckdbs-embeddable-analytics-to-more-enterprises/)

## Pourquoi c’est une garantie assez solide

Le point juridique le plus fort est que les versions déjà publiées sous MIT le restent irrévocablement : AWS ne peut pas rendre propriétaire le code que vous utilisez aujourd’hui, ni retirer le droit de le redistribuer ou de le forker.[[3]](https://layerbase.com/blog/aws-ducklabs-duckdb-governance)

Le point institutionnel est également favorable : AWS affirme explicitement ne pas acquérir le projet open source, et la gouvernance doit rester portée par la DuckDB Foundation plutôt que par DuckLabs/AWS.[[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs)

## Ce qui peut tout de même évoluer

L’incertitude porte moins sur l’accès au code que sur la **trajectoire du projet** :

| Sujet | Risque / évolution possible |
|---|---|
| Priorités de développement | L’équipe pourrait investir davantage dans les cas d’usage AWS, S3, Iceberg, lakehouse et intégrations cloud. [[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/) |
| Répartition des contributions | Si la majorité des mainteneurs devient salariée AWS, AWS pourra influencer de fait la roadmap, même si la fondation demeure indépendante. [[4]](https://www.theregister.com/databases/2026/08/26/aws-buys-ducklabs-the-people-behind-the-popular-in-process-olap-database/5292590) |
| Produits autour de DuckDB | AWS peut construire des offres managées, connecteurs ou fonctions propriétaires autour de DuckDB ; cela ne rend pas le cœur de DuckDB propriétaire. [[1]](https://www.aboutamazon.com/news/company-news/aws-ducklabs) |
| Hypothèse défavorable | Si la gouvernance ou la roadmap se détérioraient, le projet peut être forké grâce à MIT. [[3]](https://layerbase.com/blog/aws-ducklabs-duckdb-governance) |

## Lecture pratique

Pour un usage technique — par exemple DuckDB intégré à des scripts d’analyse, à un pipeline Parquet, à PostgreSQL/SQL Server via export, ou à un outil .NET — il n’y a pas de raison immédiate de changer de stratégie. Le risque de verrouillage est très faible pour le **moteur local** : un binaire et du code MIT restent utilisables indépendamment d’AWS.[[3]](https://layerbase.com/blog/aws-ducklabs-duckdb-governance)[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)

En revanche, si vous construisez un produit qui dépend fortement de la roadmap DuckDB, surveillez dans les prochains mois : la composition effective de la fondation, les droits de décision sur les releases, la politique de contribution, et la part des nouveautés strictement orientées AWS. Aujourd’hui, l’annonce est plutôt rassurante : les fondateurs restent à la tête de la direction technique et AWS s’engage à poursuivre le développement open source.[[0]](https://aws.amazon.com/blogs/big-data/aws-and-ducklabs-building-the-future-of-analytics-together/)

---

# you asked

Est-ce qu'il y a une bibliothèque native pour golang ?

---

# perplexity response

Oui : le driver officiel est **`duckdb-go`**, maintenu sous l’organisation GitHub `duckdb`. Il permet d’utiliser le moteur DuckDB natif depuis Go, via l’API standard `database/sql`.[[0]](https://duckdb.org/docs/current/clients/go.html)[[1]](https://github.com/duckdb/duckdb-go)

```bash
go get github.com/duckdb/duckdb-go/v2
```

Exemple minimal :

```go
package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/duckdb/duckdb-go/v2"
)

func main() {
	db, err := sql.Open("duckdb", "analyse.duckdb")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var total int
	err = db.QueryRow(`
		SELECT sum(i)
		FROM range(1, 101) AS t(i)
	`).Scan(&total)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(total) // 5050
}
```

## Nature du binding

Ce n’est pas une réimplémentation pure Go du moteur : `duckdb-go` s’appuie sur l’API C native de DuckDB, donc il faut en pratique compiler avec **CGO activé**. Le module fournit l’intégration `database/sql` et couvre aussi des fonctionnalités spécifiques à DuckDB, dont l’**Appender API** pour les insertions massives.[[1]](https://github.com/duckdb/duckdb-go)[[0]](https://duckdb.org/docs/current/clients/go.html)

Pour la plupart des builds locaux Linux, Windows ou macOS :

```bash
CGO_ENABLED=1 go build .
```

## Liaison statique ou dynamique

Par défaut, le driver peut embarquer/builder DuckDB de manière statique selon les bindings fournis. Il est aussi possible de le lier dynamiquement à une bibliothèque `libduckdb` externe avec le tag `duckdb_use_lib`.[[1]](https://github.com/duckdb/duckdb-go)

```bash
CGO_ENABLED=1 go build -tags=duckdb_use_lib .
```

Cette seconde option exige que `libduckdb` soit présente et trouvable par l’éditeur de liens et le chargeur dynamique.[[2]](https://ithub.global.ssl.fastly.net/marcboeker/go-duckdb/blob/main/README.md)

## Point d’attention

Pour un outil CLI Go distribué sur plusieurs plateformes, CGO complique un peu le cross-compiling — il faut un compilateur C adapté à chaque cible, ou produire les binaires dans des builds natifs/CI par plateforme. C’est le coût à accepter pour utiliser le vrai moteur vectorisé DuckDB dans le processus, plutôt qu’un client réseau.[[2]](https://ithub.global.ssl.fastly.net/marcboeker/go-duckdb/blob/main/README.md)

Pour un nouveau projet, utilisez bien `github.com/duckdb/duckdb-go/v2` : l’ancien dépôt `marcboeker/go-duckdb` a été migré vers l’organisation officielle DuckDB.[[3]](https://x.com/duckdb/status/1978813083729740088)[[2]](https://ithub.global.ssl.fastly.net/marcboeker/go-duckdb/blob/main/README.md)

Oui : le driver officiel est **`duckdb-go`**, maintenu sous l’organisation GitHub `duckdb`. Il permet d’utiliser le moteur DuckDB natif depuis Go, via l’API standard `database/sql`.[[0]](https://duckdb.org/docs/current/clients/go.html)[[1]](https://github.com/duckdb/duckdb-go)

```bash
go get github.com/duckdb/duckdb-go/v2
```

Exemple minimal :

```go
package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/duckdb/duckdb-go/v2"
)

func main() {
	db, err := sql.Open("duckdb", "analyse.duckdb")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var total int
	err = db.QueryRow(`
		SELECT sum(i)
		FROM range(1, 101) AS t(i)
	`).Scan(&total)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(total) // 5050
}
```

## Nature du binding

Ce n’est pas une réimplémentation pure Go du moteur : `duckdb-go` s’appuie sur l’API C native de DuckDB, donc il faut en pratique compiler avec **CGO activé**. Le module fournit l’intégration `database/sql` et couvre aussi des fonctionnalités spécifiques à DuckDB, dont l’**Appender API** pour les insertions massives.[[1]](https://github.com/duckdb/duckdb-go)[[0]](https://duckdb.org/docs/current/clients/go.html)

Pour la plupart des builds locaux Linux, Windows ou macOS :

```bash
CGO_ENABLED=1 go build .
```

## Liaison statique ou dynamique

Par défaut, le driver peut embarquer/builder DuckDB de manière statique selon les bindings fournis. Il est aussi possible de le lier dynamiquement à une bibliothèque `libduckdb` externe avec le tag `duckdb_use_lib`.[[1]](https://github.com/duckdb/duckdb-go)

```bash
CGO_ENABLED=1 go build -tags=duckdb_use_lib .
```

Cette seconde option exige que `libduckdb` soit présente et trouvable par l’éditeur de liens et le chargeur dynamique.[[2]](https://ithub.global.ssl.fastly.net/marcboeker/go-duckdb/blob/main/README.md)

## Point d’attention

Pour un outil CLI Go distribué sur plusieurs plateformes, CGO complique un peu le cross-compiling — il faut un compilateur C adapté à chaque cible, ou produire les binaires dans des builds natifs/CI par plateforme. C’est le coût à accepter pour utiliser le vrai moteur vectorisé DuckDB dans le processus, plutôt qu’un client réseau.[[2]](https://ithub.global.ssl.fastly.net/marcboeker/go-duckdb/blob/main/README.md)

Pour un nouveau projet, utilisez bien `github.com/duckdb/duckdb-go/v2` : l’ancien dépôt `marcboeker/go-duckdb` a été migré vers l’organisation officielle DuckDB.[[3]](https://x.com/duckdb/status/1978813083729740088)[[2]](https://ithub.global.ssl.fastly.net/marcboeker/go-duckdb/blob/main/README.md)

---

# you asked

Est-ce qu'il y a des moteurs comparables directement en go que je peux utiliser pour remplacer duckdb ?

---

# perplexity response

Oui, mais **aucun moteur 100% Go ne constitue aujourd’hui un véritable remplaçant fonctionnel et mature de DuckDB** pour l’OLAP SQL, Parquet et l’analyse ad hoc. Si votre objectif est d’éviter CGO, il faut plutôt arbitrer entre un SQL embarqué généraliste et un moteur colonne encore jeune.[[0]](https://github.com/polarsignals/frostdb)[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md)

## Les options crédibles

| Moteur | Pur Go | Positionnement | Remplace DuckDB ? |
|---|---:|---|---|
| **modernc.org/sqlite** | Oui | SQLite embarqué, SQL transactionnel, sans CGO | Oui pour stockage local et requêtes modestes ; non pour l’OLAP/Parquet massif. [[2]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)[[3]](https://news.ycombinator.com/item?id=30130938) |
| **RovaDB** | Oui | Base relationnelle SQL locale, mono-processus | Potentiellement pour un outil local simple ; trop récent pour substituer DuckDB dans de l’analytique exigeante. [[4]](https://rovadb.org/) |
| **FrostDB** | Oui | Base colonne embarquée, Parquet pour le stockage, Arrow en mémoire | La plus proche conceptuellement, mais API orientée query builder/DataFrame, projet explicitement non production-ready. [[0]](https://github.com/polarsignals/frostdb)[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md) |
| **Stoolap** | Non pour le moteur | Base embarquée HTAP, moteur Rust avec binding Go | Option intéressante si CGO/binding est acceptable ; pas une solution native Go. [[5]](https://github.com/stoolap/stoolap)[[6]](https://github.com/stoolap) |

## Le choix pragmatique

Pour tes outils Go locaux — par exemple un outil d’analyse de corpus, de logs ou de documents — je distinguerais nettement les besoins :

- **Base locale fiable, binaire statique et zéro CGO** : `modernc.org/sqlite`. Tu obtiens un SQL solide et un fichier unique, mais pas le moteur vectorisé colonne de DuckDB ni son ergonomie d’interrogation des fichiers Parquet.[[2]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)[[3]](https://news.ycombinator.com/item?id=30130938)
- **Données événementielles/observabilité que tu écris surtout, puis analyses occasionnellement** : FrostDB peut être intéressant grâce à son modèle colonne et son intégration Parquet/Arrow, mais il faut l’aborder comme une dépendance expérimentale plutôt que comme une fondation durable.[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md)[[0]](https://github.com/polarsignals/frostdb)
- **OLAP sérieux sur Parquet/CSV/JSON, jointures, agrégations et SQL exploratoire** : garde DuckDB. La dépendance CGO est un coût de packaging, mais c’est très inférieur au coût technique et fonctionnel d’un remplacement aujourd’hui. DuckDB reste embarqué dans le processus, sans serveur ni dépendance AWS à l’exécution.[[7]](https://duckdb.org/docs/current/clients/go.html)[[8]](https://github.com/duckdb/duckdb-go)

## Alternative architecturale

Si le but est surtout de publier facilement un CLI multi-plateforme, tu peux isoler DuckDB derrière une interface Go, puis faire des builds natifs par cible dans GitHub Actions : Linux, macOS Intel/ARM et Windows. Le reste de ton application demeure pur Go, et seule la couche d’exécution analytique utilise CGO.

```go
type AnalyticsStore interface {
    Exec(ctx context.Context, sql string, args ...any) error
    Query(ctx context.Context, sql string, args ...any) (*sql.Rows, error)
}
```

Cette frontière permet de commencer avec DuckDB, tout en rendant un futur backend SQLite ou FrostDB techniquement possible. Pour ton type de projets — outils CLI, traitement de fichiers et analyse locale — ce compromis est plus robuste qu’un pari immédiat sur un moteur Go immature.[[0]](https://github.com/polarsignals/frostdb)[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md)

Oui, mais **aucun moteur 100% Go ne constitue aujourd’hui un véritable remplaçant fonctionnel et mature de DuckDB** pour l’OLAP SQL, Parquet et l’analyse ad hoc. Si votre objectif est d’éviter CGO, il faut plutôt arbitrer entre un SQL embarqué généraliste et un moteur colonne encore jeune.[[0]](https://github.com/polarsignals/frostdb)[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md)

## Les options crédibles

| Moteur | Pur Go | Positionnement | Remplace DuckDB ? |
|---|---:|---|---|
| **modernc.org/sqlite** | Oui | SQLite embarqué, SQL transactionnel, sans CGO | Oui pour stockage local et requêtes modestes ; non pour l’OLAP/Parquet massif. [[2]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)[[3]](https://news.ycombinator.com/item?id=30130938) |
| **RovaDB** | Oui | Base relationnelle SQL locale, mono-processus | Potentiellement pour un outil local simple ; trop récent pour substituer DuckDB dans de l’analytique exigeante. [[4]](https://rovadb.org/) |
| **FrostDB** | Oui | Base colonne embarquée, Parquet pour le stockage, Arrow en mémoire | La plus proche conceptuellement, mais API orientée query builder/DataFrame, projet explicitement non production-ready. [[0]](https://github.com/polarsignals/frostdb)[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md) |
| **Stoolap** | Non pour le moteur | Base embarquée HTAP, moteur Rust avec binding Go | Option intéressante si CGO/binding est acceptable ; pas une solution native Go. [[5]](https://github.com/stoolap/stoolap)[[6]](https://github.com/stoolap) |

## Le choix pragmatique

Pour tes outils Go locaux — par exemple un outil d’analyse de corpus, de logs ou de documents — je distinguerais nettement les besoins :

- **Base locale fiable, binaire statique et zéro CGO** : `modernc.org/sqlite`. Tu obtiens un SQL solide et un fichier unique, mais pas le moteur vectorisé colonne de DuckDB ni son ergonomie d’interrogation des fichiers Parquet.[[2]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)[[3]](https://news.ycombinator.com/item?id=30130938)
- **Données événementielles/observabilité que tu écris surtout, puis analyses occasionnellement** : FrostDB peut être intéressant grâce à son modèle colonne et son intégration Parquet/Arrow, mais il faut l’aborder comme une dépendance expérimentale plutôt que comme une fondation durable.[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md)[[0]](https://github.com/polarsignals/frostdb)
- **OLAP sérieux sur Parquet/CSV/JSON, jointures, agrégations et SQL exploratoire** : garde DuckDB. La dépendance CGO est un coût de packaging, mais c’est très inférieur au coût technique et fonctionnel d’un remplacement aujourd’hui. DuckDB reste embarqué dans le processus, sans serveur ni dépendance AWS à l’exécution.[[7]](https://duckdb.org/docs/current/clients/go.html)[[8]](https://github.com/duckdb/duckdb-go)

## Alternative architecturale

Si le but est surtout de publier facilement un CLI multi-plateforme, tu peux isoler DuckDB derrière une interface Go, puis faire des builds natifs par cible dans GitHub Actions : Linux, macOS Intel/ARM et Windows. Le reste de ton application demeure pur Go, et seule la couche d’exécution analytique utilise CGO.

```go
type AnalyticsStore interface {
    Exec(ctx context.Context, sql string, args ...any) error
    Query(ctx context.Context, sql string, args ...any) (*sql.Rows, error)
}
```

Cette frontière permet de commencer avec DuckDB, tout en rendant un futur backend SQLite ou FrostDB techniquement possible. Pour ton type de projets — outils CLI, traitement de fichiers et analyse locale — ce compromis est plus robuste qu’un pari immédiat sur un moteur Go immature.[[0]](https://github.com/polarsignals/frostdb)[[1]](https://github.com/polarsignals/frostdb/blob/main/README.md)

---

# you asked

Mon besoin est de bâtir un outil d'analyse en temps réel des requêtes qui conservent un peu d'historique par exemple l'historique des requêtes sur une session pendant quelques minutes voir quelques dizaines de minutes et qui permet ensuite de revoir toutes les requêtes de cette session et de garder aussi des plans d'exécution des requêtes en cours , c'est un outil genre top qui va suivre les requêtes par exemple pendant une heure sur un serveur qui peut-être sollicité

---

# perplexity response

Oui, ce besoin est réaliste, mais la bonne réponse est **non pas “remplacer DuckDB”**, plutôt **séparer le moteur de capture temps réel du moteur d’analyse**. Pour un outil type `top` qui suit les requêtes actives, garde quelques minutes à une heure d’historique, et permet de revoir le texte SQL, la session, les waits et parfois le plan courant, un collecteur Go + stockage léger local est plus adapté qu’un moteur analytique pur Go censé tout faire.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)

## Architecture

Je bâtirais l’outil en trois couches :

- **Collecte live** : polling fréquent, par exemple toutes les 500 ms à 2 s, sur les vues/runtime APIs du SGBD pour les requêtes en cours, sessions, waits et blocages. Sur SQL Server, la base est `sys.dm_exec_requests`, `sys.dm_exec_sessions`, le texte via `sys.dm_exec_sql_text`, et les plans via `sys.dm_exec_query_plan`, `sys.dm_exec_query_profiles` ou les mécanismes de live query statistics.[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)[[3]](https://www.sqldbanow.com/2019/10/performance-dmv-queries-in-sql-server.html)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)
- **Mémoire court terme** : un buffer temporel en mémoire, indexé par `(session_id, request_id, sample_time)`, avec rétention glissante de 10 à 60 minutes. C’est cela qui donne l’effet “top avec historique de session”.[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)[[4]](https://minidba.com/docs/sql-server-activity)
- **Persistance locale** : stockage sur disque pour rejouer la dernière heure, agréger les échantillons, et rouvrir une session d’analyse après coup. Pour cette couche, SQLite pur Go est bien plus plausible qu’un “DuckDB de remplacement” natif Go.[[5]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)[[6]](https://news.ycombinator.com/item?id=30130938)

## Choix du stockage

Pour ce cas d’usage, **SQLite en pur Go** est probablement le meilleur compromis si tu veux éviter CGO. `modernc.org/sqlite` te donne un vrai SQL embarqué et transactionnel, suffisant pour stocker un flux d’échantillons, des snapshots de plans et des métadonnées de session ; en revanche, ce n’est pas un moteur OLAP colonne comparable à DuckDB.[[6]](https://news.ycombinator.com/item?id=30130938)[[5]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)

Concrètement :
- **En mémoire** : ring buffer ou tables temporelles Go pour l’écran live.
- **Sur disque** : SQLite pour `samples`, `sessions`, `statements`, `plans`.
- **En option** : export périodique en Parquet/CSV pour analyse offline plus lourde ailleurs. Cette séparation colle bien à ton goût pour les outils Go locaux et reproductibles. 

## Ce qu’il faut capturer

Le point clé est de ne pas raisonner “une ligne par requête”, mais **une ligne par échantillon**. Une même requête active pendant 12 minutes doit produire une série temporelle permettant de reconstituer son histoire : durée, CPU, reads, waits, blocages, évolution du plan visible, changements d’état.[[4]](https://minidba.com/docs/sql-server-activity)[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)

Je prévoirais au minimum :

- `sessions`: session id, login, host, app, database, opened_at, closed_at.
- `requests`: session id, request id, statement hash, start_time, end_time, statut final.
- `samples`: sample_time, elapsed_ms, cpu_ms, reads, writes, logical_reads, wait_type, wait_time_ms, blocking_session_id, dop, memory grant.
- `sql_texts`: texte brut, texte normalisé, hash.
- `plans`: plan_handle ou équivalent, XML/JSON/text, type (`estimated`, `actual-last`, `live`), horodatage de capture.[[3]](https://www.sqldbanow.com/2019/10/performance-dmv-queries-in-sql-server.html)[[7]](https://kendralittle.com/2025/11/24/how-to-see-rowcounts-execution-time-in-flight-queries-sql-server/)[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)

Exemple de schéma mental :

```text
Session 57
 ├── Request 1
 │    ├── sample 08:00:01
 │    ├── sample 08:00:02
 │    ├── sample 08:00:03
 │    └── live plan snapshots
 └── Request 2
      ├── sample ...
      └── final status
```

## Plans d’exécution

C’est ici que la base cible compte beaucoup. Sur **SQL Server**, ton besoin est bien couvert : on peut voir les requêtes en cours avec leurs métriques, récupérer le plan courant ou le dernier plan réel connu selon les mécanismes activés, et exploiter l’infrastructure de profiling qui sous-tend Live Query Statistics.[[7]](https://kendralittle.com/2025/11/24/how-to-see-rowcounts-execution-time-in-flight-queries-sql-server/)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)

En pratique, pour SQL Server :
- pour le live “top”, `sys.dm_exec_requests` est la base ;
- pour le texte, `sys.dm_exec_sql_text` ;
- pour les plans en cours ou enrichis, `sys.dm_exec_query_profiles`, `sys.dm_exec_query_statistics_xml` et `sys.dm_exec_query_plan_stats` selon version/configuration ;  
- pour l’historique plus large, tu peux compléter avec Query Store ou Extended Events, mais ce n’est pas indispensable pour un premier outil orienté session live.[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)[[3]](https://www.sqldbanow.com/2019/10/performance-dmv-queries-in-sql-server.html)[[7]](https://kendralittle.com/2025/11/24/how-to-see-rowcounts-execution-time-in-flight-queries-sql-server/)

Ma recommandation nette : **Go pur pour l’agent + SQLite pur Go pour l’historique + abstraction de source par SGBD**. Si tu vises surtout SQL Server au départ, tu peux faire un outil très utile sans DuckDB, et sans chercher un clone Go de DuckDB qui n’existe pas encore à maturité. Si tu veux, je peux te proposer juste après un design concret des tables, des interfaces Go et de la boucle d’échantillonnage.

Oui, ce besoin est réaliste, mais la bonne réponse est **non pas “remplacer DuckDB”**, plutôt **séparer le moteur de capture temps réel du moteur d’analyse**. Pour un outil type `top` qui suit les requêtes actives, garde quelques minutes à une heure d’historique, et permet de revoir le texte SQL, la session, les waits et parfois le plan courant, un collecteur Go + stockage léger local est plus adapté qu’un moteur analytique pur Go censé tout faire.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)

## Architecture

Je bâtirais l’outil en trois couches :

- **Collecte live** : polling fréquent, par exemple toutes les 500 ms à 2 s, sur les vues/runtime APIs du SGBD pour les requêtes en cours, sessions, waits et blocages. Sur SQL Server, la base est `sys.dm_exec_requests`, `sys.dm_exec_sessions`, le texte via `sys.dm_exec_sql_text`, et les plans via `sys.dm_exec_query_plan`, `sys.dm_exec_query_profiles` ou les mécanismes de live query statistics.[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)[[3]](https://www.sqldbanow.com/2019/10/performance-dmv-queries-in-sql-server.html)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)
- **Mémoire court terme** : un buffer temporel en mémoire, indexé par `(session_id, request_id, sample_time)`, avec rétention glissante de 10 à 60 minutes. C’est cela qui donne l’effet “top avec historique de session”.[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)[[4]](https://minidba.com/docs/sql-server-activity)
- **Persistance locale** : stockage sur disque pour rejouer la dernière heure, agréger les échantillons, et rouvrir une session d’analyse après coup. Pour cette couche, SQLite pur Go est bien plus plausible qu’un “DuckDB de remplacement” natif Go.[[5]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)[[6]](https://news.ycombinator.com/item?id=30130938)

## Choix du stockage

Pour ce cas d’usage, **SQLite en pur Go** est probablement le meilleur compromis si tu veux éviter CGO. `modernc.org/sqlite` te donne un vrai SQL embarqué et transactionnel, suffisant pour stocker un flux d’échantillons, des snapshots de plans et des métadonnées de session ; en revanche, ce n’est pas un moteur OLAP colonne comparable à DuckDB.[[6]](https://news.ycombinator.com/item?id=30130938)[[5]](https://www.libhunt.com/posts/586843-a-pure-go-embedded-sql-database)

Concrètement :
- **En mémoire** : ring buffer ou tables temporelles Go pour l’écran live.
- **Sur disque** : SQLite pour `samples`, `sessions`, `statements`, `plans`.
- **En option** : export périodique en Parquet/CSV pour analyse offline plus lourde ailleurs. Cette séparation colle bien à ton goût pour les outils Go locaux et reproductibles. 

## Ce qu’il faut capturer

Le point clé est de ne pas raisonner “une ligne par requête”, mais **une ligne par échantillon**. Une même requête active pendant 12 minutes doit produire une série temporelle permettant de reconstituer son histoire : durée, CPU, reads, waits, blocages, évolution du plan visible, changements d’état.[[4]](https://minidba.com/docs/sql-server-activity)[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)

Je prévoirais au minimum :

- `sessions`: session id, login, host, app, database, opened_at, closed_at.
- `requests`: session id, request id, statement hash, start_time, end_time, statut final.
- `samples`: sample_time, elapsed_ms, cpu_ms, reads, writes, logical_reads, wait_type, wait_time_ms, blocking_session_id, dop, memory grant.
- `sql_texts`: texte brut, texte normalisé, hash.
- `plans`: plan_handle ou équivalent, XML/JSON/text, type (`estimated`, `actual-last`, `live`), horodatage de capture.[[3]](https://www.sqldbanow.com/2019/10/performance-dmv-queries-in-sql-server.html)[[7]](https://kendralittle.com/2025/11/24/how-to-see-rowcounts-execution-time-in-flight-queries-sql-server/)[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)

Exemple de schéma mental :

```text
Session 57
 ├── Request 1
 │    ├── sample 08:00:01
 │    ├── sample 08:00:02
 │    ├── sample 08:00:03
 │    └── live plan snapshots
 └── Request 2
      ├── sample ...
      └── final status
```

## Plans d’exécution

C’est ici que la base cible compte beaucoup. Sur **SQL Server**, ton besoin est bien couvert : on peut voir les requêtes en cours avec leurs métriques, récupérer le plan courant ou le dernier plan réel connu selon les mécanismes activés, et exploiter l’infrastructure de profiling qui sous-tend Live Query Statistics.[[7]](https://kendralittle.com/2025/11/24/how-to-see-rowcounts-execution-time-in-flight-queries-sql-server/)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)

En pratique, pour SQL Server :
- pour le live “top”, `sys.dm_exec_requests` est la base ;
- pour le texte, `sys.dm_exec_sql_text` ;
- pour les plans en cours ou enrichis, `sys.dm_exec_query_profiles`, `sys.dm_exec_query_statistics_xml` et `sys.dm_exec_query_plan_stats` selon version/configuration ;  
- pour l’historique plus large, tu peux compléter avec Query Store ou Extended Events, mais ce n’est pas indispensable pour un premier outil orienté session live.[[1]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)[[2]](https://www.site24x7.com/learn/troubleshoot-slow-sql-queries.html)[[3]](https://www.sqldbanow.com/2019/10/performance-dmv-queries-in-sql-server.html)[[7]](https://kendralittle.com/2025/11/24/how-to-see-rowcounts-execution-time-in-flight-queries-sql-server/)

Ma recommandation nette : **Go pur pour l’agent + SQLite pur Go pour l’historique + abstraction de source par SGBD**. Si tu vises surtout SQL Server au départ, tu peux faire un outil très utile sans DuckDB, et sans chercher un clone Go de DuckDB qui n’existe pas encore à maturité. Si tu veux, je peux te proposer juste après un design concret des tables, des interfaces Go et de la boucle d’échantillonnage.

---

# you asked

Tu as raison il faut prévoir l'abstraction de source dès le départ , le MVP sera purement sql server mais il faudrait ajouter postgresql et mysql ensuite. Je veux pouvoir faire en sorte que l'outil crée une session d'événements étendus et puisse lire le ring buffer, c'est possible en go facilement ?

---

# perplexity response

Oui, c’est **tout à fait possible en Go**, et même assez simple sur le principe : Go n’a pas d’API spéciale “Extended Events”, mais il n’en a pas besoin, car tout passe par du **T-SQL** envoyé via le driver SQL Server, puis par la lecture de XML renvoyé par les DMVs `sys.dm_xe_sessions` et `sys.dm_xe_session_targets`. Le `ring_buffer` se lit en récupérant `target_data`, en le convertissant en XML, puis en extrayant les nœuds `/RingBufferTarget/event`.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)[[3]](https://learn.microsoft.com/en-us/archive/blogs/extended_events/reading-event-data-101-whats-up-with-the-xml)

## Ce qui est facile

Avec Go, tu peux sans difficulté :

- créer une session XE avec `CREATE EVENT SESSION ... ADD EVENT ... ADD TARGET package0.ring_buffer`;[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)[[4]](https://www.brentozar.com/archive/2015/01/query-extended-events-target-xml/)
- la démarrer et l’arrêter avec `ALTER EVENT SESSION ... STATE = START|STOP`; [[4]](https://www.brentozar.com/archive/2015/01/query-extended-events-target-xml/)[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- lire périodiquement le ring buffer en interrogeant `sys.dm_xe_session_targets` joint à `sys.dm_xe_sessions`;[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)[[6]](https://andreas-wolter.com/en/how-to-import-extended-events-session-event_file-target/)[[7]](https://www.sqlskills.com/blogs/jonathan/extended-events-ring_buffer/)
- parser soit côté SQL avec XQuery pour renvoyer déjà un rowset propre, soit côté Go en récupérant le XML brut puis en le décodant.[[3]](https://learn.microsoft.com/en-us/archive/blogs/extended_events/reading-event-data-101-whats-up-with-the-xml)[[8]](https://learn.microsoft.com/ar-sa/archive/blogs/dataplatform/deconstructing-extended-event-xml)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)

Autrement dit, pour un MVP SQL Server, l’abstraction “source” peut exposer des opérations comme `EnsureSession`, `StartCapture`, `ReadEvents`, `StopCapture`, et l’implémentation SQL Server les réalisera entièrement avec SQL standard + parsing XML.[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)

## Ce qui est moins agréable

Le point pénible n’est pas Go, c’est **le ring buffer lui-même**. Microsoft le présente comme un target utile pour un démarrage rapide en mémoire, mais précise que lors de la conversion en XML, toute donnée qui dépasse un document XML de 4 MB est omise, et recommande de régler `MAX_MEMORY` à 1024 KB ou moins pour ce target.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

En plus, le ring buffer est volatile : quand la mémoire allouée est pleine, les anciens événements sont écrasés, et quand la session est arrêtée, son contenu est perdu. Plusieurs sources soulignent aussi que la lecture est moins pratique, car il faut scanner du XML et connaître les événements/actions capturés pour bien le projeter.[[9]](https://www.sqlskills.com/blogs/jonathan/why-i-hate-the-ring_buffer-target-in-extended-events/)[[10]](https://techcommunity.microsoft.com/blog/sqlserversupport/you-may-not-see-the-data-you-expect-in-extended-event-ring-buffer-targets8230-/315838)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

## Recommandation MVP

Pour ton besoin “genre top pendant une heure”, je ferais ceci :

- **Pour le live immédiat** : DMVs (`dm_exec_requests`, waits, blocages, texte SQL), rafraîchies toutes les 1–2 s.[[11]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)[[12]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)
- **Pour l’historique court** : une session XE dédiée, très ciblée, lue régulièrement depuis Go.[[4]](https://www.brentozar.com/archive/2015/01/query-extended-events-target-xml/)[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- **Pour la conservation fiable** : dès la lecture, insertion des événements normalisés dans ton stockage local ; ne jamais considérer le ring buffer comme stockage d’historique.[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

Donc : **oui au ring buffer pour le MVP**, mais comme **tampon de capture**, pas comme dépôt principal. Pour une charge plus soutenue ou une fenêtre d’une heure avec beaucoup d’activité, `event_file` sera souvent plus robuste que `ring_buffer`, même si un peu moins “immédiat”.[[13]](https://www.sqlservercentral.com/forums/topic/extended-events-8)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

## Design Go

L’approche la plus propre est de cacher complètement XE derrière une interface source SQL Server :

```go
type CaptureSource interface {
    EnsureSession(ctx context.Context) error
    StartSession(ctx context.Context) error
    StopSession(ctx context.Context) error
    ReadEvents(ctx context.Context, since time.Time) ([]Event, error)
}
```

L’implémentation SQL Server peut :
- générer un nom de session dédié à l’outil ;
- créer la session si absente via `CREATE EVENT SESSION`;[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- interroger `sys.server_event_sessions` ou `sys.dm_xe_sessions` pour savoir si elle existe et tourne ;[[6]](https://andreas-wolter.com/en/how-to-import-extended-events-session-event_file-target/)[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- lire le XML du ring buffer puis le transformer en événements Go structurés.[[7]](https://www.sqlskills.com/blogs/jonathan/extended-events-ring_buffer/)[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)

## Conseils pratiques

Quelques points rendent la vie plus simple :

- Fais projeter un maximum de colonnes **côté SQL** avec XQuery, afin d’éviter un parsing XML trop générique en Go. Microsoft montre explicitement la transformation du ring buffer XML en rowset relationnel via `nodes()` et `value()`.[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)[[3]](https://learn.microsoft.com/en-us/archive/blogs/extended_events/reading-event-data-101-whats-up-with-the-xml)
- Garde la session XE **très ciblée** : peu d’événements, peu d’actions, sinon tu satures vite la mémoire du ring buffer.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)
- Ajoute un mécanisme de watermark, par exemple sur le timestamp XE, pour ne pas retraiter les mêmes événements à chaque poll. Le contenu du ring buffer étant relu comme un ensemble XML, cette déduplication est importante côté outil.[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)
- Prévois dès maintenant une interface distincte entre **capture événementielle** et **snapshot DMV live** ; PostgreSQL et MySQL n’auront pas l’équivalent exact de XE, donc l’abstraction ne doit pas supposer un ring buffer partout.[[14]](https://www.postgresql.org/docs/current/runtime-config-statistics.html)

Oui, je dirais donc que c’est **facilement faisable en Go**, à condition de considérer que la complexité réelle est dans le modèle de capture SQL Server et dans le XML XE, pas dans le langage. Je peux te proposer un squelette concret en Go : interface, DDL de session XE, requête de lecture du ring buffer, et modèle de données normalisé multi-SGBD.

Oui, c’est **tout à fait possible en Go**, et même assez simple sur le principe : Go n’a pas d’API spéciale “Extended Events”, mais il n’en a pas besoin, car tout passe par du **T-SQL** envoyé via le driver SQL Server, puis par la lecture de XML renvoyé par les DMVs `sys.dm_xe_sessions` et `sys.dm_xe_session_targets`. Le `ring_buffer` se lit en récupérant `target_data`, en le convertissant en XML, puis en extrayant les nœuds `/RingBufferTarget/event`.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)[[3]](https://learn.microsoft.com/en-us/archive/blogs/extended_events/reading-event-data-101-whats-up-with-the-xml)

## Ce qui est facile

Avec Go, tu peux sans difficulté :

- créer une session XE avec `CREATE EVENT SESSION ... ADD EVENT ... ADD TARGET package0.ring_buffer`;[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)[[4]](https://www.brentozar.com/archive/2015/01/query-extended-events-target-xml/)
- la démarrer et l’arrêter avec `ALTER EVENT SESSION ... STATE = START|STOP`; [[4]](https://www.brentozar.com/archive/2015/01/query-extended-events-target-xml/)[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- lire périodiquement le ring buffer en interrogeant `sys.dm_xe_session_targets` joint à `sys.dm_xe_sessions`;[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)[[6]](https://andreas-wolter.com/en/how-to-import-extended-events-session-event_file-target/)[[7]](https://www.sqlskills.com/blogs/jonathan/extended-events-ring_buffer/)
- parser soit côté SQL avec XQuery pour renvoyer déjà un rowset propre, soit côté Go en récupérant le XML brut puis en le décodant.[[3]](https://learn.microsoft.com/en-us/archive/blogs/extended_events/reading-event-data-101-whats-up-with-the-xml)[[8]](https://learn.microsoft.com/ar-sa/archive/blogs/dataplatform/deconstructing-extended-event-xml)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)

Autrement dit, pour un MVP SQL Server, l’abstraction “source” peut exposer des opérations comme `EnsureSession`, `StartCapture`, `ReadEvents`, `StopCapture`, et l’implémentation SQL Server les réalisera entièrement avec SQL standard + parsing XML.[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)

## Ce qui est moins agréable

Le point pénible n’est pas Go, c’est **le ring buffer lui-même**. Microsoft le présente comme un target utile pour un démarrage rapide en mémoire, mais précise que lors de la conversion en XML, toute donnée qui dépasse un document XML de 4 MB est omise, et recommande de régler `MAX_MEMORY` à 1024 KB ou moins pour ce target.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

En plus, le ring buffer est volatile : quand la mémoire allouée est pleine, les anciens événements sont écrasés, et quand la session est arrêtée, son contenu est perdu. Plusieurs sources soulignent aussi que la lecture est moins pratique, car il faut scanner du XML et connaître les événements/actions capturés pour bien le projeter.[[9]](https://www.sqlskills.com/blogs/jonathan/why-i-hate-the-ring_buffer-target-in-extended-events/)[[10]](https://techcommunity.microsoft.com/blog/sqlserversupport/you-may-not-see-the-data-you-expect-in-extended-event-ring-buffer-targets8230-/315838)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

## Recommandation MVP

Pour ton besoin “genre top pendant une heure”, je ferais ceci :

- **Pour le live immédiat** : DMVs (`dm_exec_requests`, waits, blocages, texte SQL), rafraîchies toutes les 1–2 s.[[11]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/live-query-statistics?view=sql-server-ver17)[[12]](https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-profiling-infrastructure?view=sql-server-ver17)
- **Pour l’historique court** : une session XE dédiée, très ciblée, lue régulièrement depuis Go.[[4]](https://www.brentozar.com/archive/2015/01/query-extended-events-target-xml/)[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- **Pour la conservation fiable** : dès la lecture, insertion des événements normalisés dans ton stockage local ; ne jamais considérer le ring buffer comme stockage d’historique.[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

Donc : **oui au ring buffer pour le MVP**, mais comme **tampon de capture**, pas comme dépôt principal. Pour une charge plus soutenue ou une fenêtre d’une heure avec beaucoup d’activité, `event_file` sera souvent plus robuste que `ring_buffer`, même si un peu moins “immédiat”.[[13]](https://www.sqlservercentral.com/forums/topic/extended-events-8)[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)

## Design Go

L’approche la plus propre est de cacher complètement XE derrière une interface source SQL Server :

```go
type CaptureSource interface {
    EnsureSession(ctx context.Context) error
    StartSession(ctx context.Context) error
    StopSession(ctx context.Context) error
    ReadEvents(ctx context.Context, since time.Time) ([]Event, error)
}
```

L’implémentation SQL Server peut :
- générer un nom de session dédié à l’outil ;
- créer la session si absente via `CREATE EVENT SESSION`;[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- interroger `sys.server_event_sessions` ou `sys.dm_xe_sessions` pour savoir si elle existe et tourne ;[[6]](https://andreas-wolter.com/en/how-to-import-extended-events-session-event_file-target/)[[5]](https://www.red-gate.com/simple-talk/databases/sql-server/performance-sql-server/automating-extended-events-data-collection/)
- lire le XML du ring buffer puis le transformer en événements Go structurés.[[7]](https://www.sqlskills.com/blogs/jonathan/extended-events-ring_buffer/)[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)

## Conseils pratiques

Quelques points rendent la vie plus simple :

- Fais projeter un maximum de colonnes **côté SQL** avec XQuery, afin d’éviter un parsing XML trop générique en Go. Microsoft montre explicitement la transformation du ring buffer XML en rowset relationnel via `nodes()` et `value()`.[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)[[3]](https://learn.microsoft.com/en-us/archive/blogs/extended_events/reading-event-data-101-whats-up-with-the-xml)
- Garde la session XE **très ciblée** : peu d’événements, peu d’actions, sinon tu satures vite la mémoire du ring buffer.[[0]](https://learn.microsoft.com/en-us/sql/relational-databases/extended-events/targets-for-extended-events-in-sql-server?view=sql-server-ver17)
- Ajoute un mécanisme de watermark, par exemple sur le timestamp XE, pour ne pas retraiter les mêmes événements à chaque poll. Le contenu du ring buffer étant relu comme un ensemble XML, cette déduplication est importante côté outil.[[2]](https://www.sqlserverscience.com/extended-events/reading-the-ring-buffer-target/)[[1]](https://learn.microsoft.com/en-us/azure/azure-sql/database/xevent-code-ring-buffer?view=azuresql)
- Prévois dès maintenant une interface distincte entre **capture événementielle** et **snapshot DMV live** ; PostgreSQL et MySQL n’auront pas l’équivalent exact de XE, donc l’abstraction ne doit pas supposer un ring buffer partout.[[14]](https://www.postgresql.org/docs/current/runtime-config-statistics.html)

Oui, je dirais donc que c’est **facilement faisable en Go**, à condition de considérer que la complexité réelle est dans le modèle de capture SQL Server et dans le XML XE, pas dans le langage. Je peux te proposer un squelette concret en Go : interface, DDL de session XE, requête de lecture du ring buffer, et modèle de données normalisé multi-SGBD.